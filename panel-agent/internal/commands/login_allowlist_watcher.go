package commands

// login_allowlist_watcher.go — GH #598 SSH half of auto-allowlist-on-login.
//
// A root-side background worker tails sshd's own success log (journald) and
// keeps the source IP of every successful root SSH login off CrowdSec's bans
// for the configured window (login_ban_exempt.go; the WAF still inspects it).
// This is the trusted, zero-auth-path-risk
// capture point: the tenant login shell (jabali-ssh-shell) is unprivileged and
// forgeable, and a PAM hook would sit in the auth path (lockout risk). Failure
// here degrades to "no allowlist entry", never a lockout.
//
// The panel owns the toggle/TTL in server_settings and pushes it to
// /etc/jabali-panel/login-allowlist.conf via security.crowdsec.login_allowlist.apply;
// the watcher reads that file per hit (cheap) so a toggle takes effect without
// an agent restart. install.sh drops a default (enabled) conf for fresh hosts.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// Dedup window: re-allowlist a given source IP at most once per window so a
	// reconnect storm doesn't hammer cscli. Matches the panel-side 24h dedup.
	loginAllowlistDedupWindow = 24 * time.Hour
	loginAllowlistDefaultTTL  = "168h" // 7d; overridden by the pushed conf
)

// loginAllowlistConfPath is a var (not const) so tests can point it at a temp
// file. Production value is fixed.
var loginAllowlistConfPath = "/etc/jabali-panel/login-allowlist.conf"

// loginAllowlistConf is the on-disk policy the panel pushes.
type loginAllowlistConf struct {
	Enabled bool   `json:"enabled"`
	TTL     string `json:"ttl"` // Go duration, e.g. "168h"
}

// sshAcceptedRe matches the MESSAGE of sshd's success line, as `journalctl
// -o cat` prints it, capturing the login user and source IP. Covers publickey /
// password / keyboard-interactive[/pam]. Example:
//
//	Accepted publickey for alice from 203.0.113.7 port 51234 ssh2: RSA SHA256:...
//
// Anchored at both ends: sshd logs a client-chosen username verbatim
// ("Invalid user <name> from <ip> ..."), so an unanchored match let a
// pre-auth client spell out an Accepted line in its username.
var sshAcceptedRe = regexp.MustCompile(
	`^Accepted (?:publickey|password|keyboard-interactive(?:/pam)?) for (\S+) from (\S+) port \d+ ssh2(?:: .*)?$`)

// parseSSHAccepted returns the user and IP of an sshd success message.
func parseSSHAccepted(line string) (user, ip string, ok bool) {
	m := sshAcceptedRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// readLoginAllowlistConf returns the pushed policy, defaulting to enabled with
// the 7d TTL when the file is absent/unreadable (fresh host before first push,
// or a transient read error — fail safe toward protection, matching the panel).
func readLoginAllowlistConf() loginAllowlistConf {
	def := loginAllowlistConf{Enabled: true, TTL: loginAllowlistDefaultTTL}
	b, err := os.ReadFile(loginAllowlistConfPath)
	if err != nil {
		return def
	}
	var c loginAllowlistConf
	if json.Unmarshal(b, &c) != nil {
		return def
	}
	if strings.TrimSpace(c.TTL) == "" {
		c.TTL = loginAllowlistDefaultTTL
	}
	if _, err := time.ParseDuration(c.TTL); err != nil {
		c.TTL = loginAllowlistDefaultTTL
	}
	return c
}

// loginWhitelistableIP: only a routable public address is worth exempting (a
// CrowdSec public-IP decision never targets loopback/private/link-local), and
// never carrier-grade NAT, which many unrelated clients share.
func loginWhitelistableIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	_, ok := loginBanExemptIP(s)
	return ok
}

// loginDedup is a small mutex-guarded IP→lastSeen map, pruned on access so it
// never grows unbounded under a long-lived watcher.
type loginDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newLoginDedup() *loginDedup { return &loginDedup{seen: map[string]time.Time{}} }

// allow reports whether ip should be (re)allowlisted now, and records it if so.
// now is passed in for testability.
func (d *loginDedup) allow(ip string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Prune expired entries.
	for k, t := range d.seen {
		if now.Sub(t) >= loginAllowlistDedupWindow {
			delete(d.seen, k)
		}
	}
	if t, ok := d.seen[ip]; ok && now.Sub(t) < loginAllowlistDedupWindow {
		return false
	}
	d.seen[ip] = now
	return true
}

// StartLoginAllowlistWatcher launches the supervised sshd-success watcher. It
// returns immediately; the loop runs until ctx is cancelled. The journalctl -f
// child can die (journald restart, log rotation) — the supervisor restarts it
// with a short backoff rather than assuming it runs forever.
func StartLoginAllowlistWatcher(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	dedup := newLoginDedup()
	go dropAutoAllowlistEntriesUntilDone(ctx, log)
	go runLoginBanExemptMaintenance(ctx, log)
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			if err := runLoginAllowlistTail(ctx, dedup, log); err != nil && ctx.Err() == nil {
				log.Warn("login-allowlist: tail exited, restarting", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

// runLoginAllowlistTail follows the sshd journal (auth.log fallback) once,
// dispatching each matching Accepted line. Returns when the child exits or ctx
// is cancelled.
func runLoginAllowlistTail(ctx context.Context, dedup *loginDedup, log *slog.Logger) error {
	cmd := loginTailCmd(ctx)
	if cmd == nil {
		log.Warn("login-allowlist: journalctl not found; ssh logins are not auto-allowlisted")
		<-ctx.Done()
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start tail: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		handleSSHLogLine(ctx, sc.Text(), dedup, log, time.Now())
	}
	// Kill the child if we broke out via ctx; reap it either way.
	_ = cmd.Process.Kill()
	return cmd.Wait()
}

// loginTailCmd builds the log-follow command: journald only, both unit
// names (distro-dependent), sshd's root processes only (_UID=0). A plain
// syslog file is not read: its "sshd[pid]:" tag is whatever the sender wrote
// (`logger -t`), so any local account could forge an Accepted line there.
// Nil when journalctl is absent.
func loginTailCmd(ctx context.Context) *exec.Cmd {
	if _, err := exec.LookPath("journalctl"); err != nil {
		return nil
	}
	// --since now avoids re-scanning historical log on every restart (a busy
	// host would otherwise re-allowlist churn on each boot).
	return execCommandContext(ctx, "journalctl",
		"-u", "ssh.service", "-u", "sshd.service", "_UID=0",
		"-f", "-n", "0", "--since", "now", "-o", "cat")
}

// handleSSHLogLine parses one log line and, on a successful root login from a
// routable IP not seen within the dedup window, keeps that IP off CrowdSec's
// bans per the pushed policy. Exported-package-internal for the unit test.
func handleSSHLogLine(ctx context.Context, line string, dedup *loginDedup, log *slog.Logger, now time.Time) {
	user, ip, ok := parseSSHAccepted(line)
	if !ok {
		return
	}
	// GH #709: only the operator's root SSH is auto-allowlisted. A TENANT SSH
	// login (their own OS username via jabali-ssh-shell) must NOT add a
	// server-wide CrowdSec allowlist entry — a compromised tenant key would
	// otherwise shield the attacker's IP. Mirrors the panel-side admin-only gate.
	if user != "root" {
		log.Info("login-allowlist: ssh login observed, skipped (non-root/tenant)", "ip", ip, "user", user)
		return
	}
	if !loginWhitelistableIP(ip) {
		// Observed but intentionally skipped (private/loopback). Logged at INFO
		// (not Debug) so a live-verify on an RFC1918 test host can distinguish
		// "working, correctly skipped" from "watcher broken/never started".
		log.Info("login-allowlist: ssh login observed, skipped (non-routable ip)", "ip", ip, "user", user)
		return
	}
	conf := readLoginAllowlistConf()
	if !conf.Enabled {
		return
	}
	if !dedup.allow(ip, now) {
		return
	}
	ttl, _ := time.ParseDuration(conf.TTL) // readLoginAllowlistConf checked it
	if err := addLoginBanExempt(ip, "ssh login "+truncateReason(user, 120), ttl, now); err != nil {
		log.Warn("login-allowlist: ssh login IP not exempted from bans", "ip", ip, "user", user, "err", err)
		return
	}
	log.Info("login-allowlist: ssh login IP exempted from bans (WAF still applies)", "ip", ip, "user", user, "ttl", conf.TTL)
}

// dropAutoAllowlistEntries removes every login entry older agents and panels
// added to the jabali allowlist ("auto-whitelist: ssh …" / "auto-whitelist:
// login …"). An allowlisted IP skips the WAF too; login IPs now only skip
// bans (login_ban_exempt.go), and an older watcher matched an Accepted line
// anywhere in a log line, so its entries may not be real root logins. A
// genuine login exempts its IP again. Entries the admin added by hand (any
// other description) stay. Returns done=false when cscli could not be asked
// or an entry could not be removed (a slow cscli, LAPI restarting), so the
// caller tries again: an entry left behind would keep its address off the WAF
// until it expires. Each cscli call gets its own time limit.
func dropAutoAllowlistEntries(ctx context.Context) (removed int, done bool) {
	ictx, icancel := context.WithTimeout(ctx, 30*time.Second)
	out, err := runCscliJSON(ictx, "allowlists", "inspect", jabaliAllowlistName)
	icancel()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return 0, true // no CrowdSec on this box
		}
		if msg := strings.ToLower(err.Error()); strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist") || strings.Contains(msg, "no such") {
			return 0, true // no such allowlist yet: nothing to drop
		}
		return 0, false // LAPI not up yet: retry
	}
	var raw rawAllowlistInspect
	if json.Unmarshal(out, &raw) != nil {
		return 0, false
	}
	failed := 0
	for _, it := range raw.Items {
		if !strings.HasPrefix(it.Description, loginAutoReasonPrefix) {
			continue
		}
		rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
		out, err := execCommandContext(rctx, "cscli", "allowlists", "remove", jabaliAllowlistName, it.Value).CombinedOutput()
		rcancel()
		switch msg := strings.ToLower(string(out)); {
		case err == nil:
			removed++
		case strings.Contains(msg, "not found") || strings.Contains(msg, "no such"):
			// already gone (expired since the inspect)
		default:
			failed++
		}
	}
	if removed > 0 {
		requestListsRerender()
	}
	return removed, failed == 0
}

// dropAutoAllowlistEntriesUntilDone retries dropAutoAllowlistEntries every
// five minutes until every login entry is gone (CrowdSec may start after the
// agent, or answer slowly).
func dropAutoAllowlistEntriesUntilDone(ctx context.Context, log *slog.Logger) {
	for {
		removed, done := dropAutoAllowlistEntries(ctx)
		if removed > 0 {
			log.Info("login-allowlist: dropped login entries from the allowlist; logins now only exempt their IP from bans", "removed", removed)
		}
		if done {
			return
		}
		log.Warn("login-allowlist: login entries left on the allowlist; trying again in 5 minutes")
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

func truncateReason(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// ---- security.crowdsec.login_allowlist.apply -------------------------------

type csLoginAllowlistApplyParams struct {
	Enabled  bool `json:"enabled"`
	TTLHours int  `json:"ttl_hours"`
}

// csLoginAllowlistApplyHandler writes the pushed policy to the conf file the
// watcher reads. Atomic (temp + rename). The panel reconciler calls this
// write-on-diff when the server setting changes.
func csLoginAllowlistApplyHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p csLoginAllowlistApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	ttlHours := p.TTLHours
	if ttlHours <= 0 {
		ttlHours = 168
	}
	conf := loginAllowlistConf{Enabled: p.Enabled, TTL: fmt.Sprintf("%dh", ttlHours)}
	b, err := json.Marshal(conf)
	if err != nil {
		return nil, csInternal("marshal conf", err)
	}
	if err := os.MkdirAll(filepath.Dir(loginAllowlistConfPath), 0o755); err != nil {
		return nil, csInternal("mkdir conf dir", err)
	}
	tmp := loginAllowlistConfPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return nil, csInternal("write temp conf", err)
	}
	if err := os.Rename(tmp, loginAllowlistConfPath); err != nil {
		_ = os.Remove(tmp)
		return nil, csInternal("rename conf", err)
	}
	if !p.Enabled {
		// Off means no login IP stays exempt from bans.
		if err := clearLoginBanExempt(); err != nil {
			return nil, csInternal("clear login IPs", err)
		}
	}
	return map[string]any{"enabled": p.Enabled, "ttl_hours": ttlHours}, nil
}

func init() {
	Default.Register("security.crowdsec.login_allowlist.apply", csLoginAllowlistApplyHandler)
}
