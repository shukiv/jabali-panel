package commands

// login_allowlist_watcher.go — GH #598 SSH half of auto-allowlist-on-login.
//
// A root-side background worker tails sshd's own success log (journald, with an
// auth.log fallback) and time-boxes the source IP of every successful SSH login
// into the jabali CrowdSec allowlist. This is the trusted, zero-auth-path-risk
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

// sshAcceptedRe matches sshd's success line (journald `-o cat` or auth.log),
// capturing the login user and source IP. Covers publickey / password /
// keyboard-interactive[/pam]. Example:
//
//	Accepted publickey for alice from 203.0.113.7 port 51234 ssh2: RSA SHA256:...
var sshAcceptedRe = regexp.MustCompile(
	`Accepted (?:publickey|password|keyboard-interactive(?:/pam)?) for (\S+) from (\S+) port \d+`)

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

// loginWhitelistableIP mirrors the panel's whitelistableIP: only routable
// public addresses are worth allowlisting (a CrowdSec public-IP decision never
// targets loopback/private/link-local).
func loginWhitelistableIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	return true
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

// loginTailCmd builds the log-follow command. Prefer journald (Debian default);
// try both ssh.service and sshd.service unit names (distro-dependent). If
// journalctl is absent, fall back to `tail -F /var/log/auth.log`.
func loginTailCmd(ctx context.Context) *exec.Cmd {
	if _, err := exec.LookPath("journalctl"); err == nil {
		// --since now avoids re-scanning historical log on every restart (a busy
		// host would otherwise re-allowlist churn on each boot).
		return execCommandContext(ctx, "journalctl",
			"-u", "ssh.service", "-u", "sshd.service",
			"-f", "-n", "0", "--since", "now", "-o", "cat")
	}
	return execCommandContext(ctx, "tail", "-F", "/var/log/auth.log")
}

// handleSSHLogLine parses one log line and, on a successful-login match for a
// routable IP not seen within the dedup window, allowlists it per the pushed
// policy. Exported-package-internal for the unit test.
func handleSSHLogLine(ctx context.Context, line string, dedup *loginDedup, log *slog.Logger, now time.Time) {
	m := sshAcceptedRe.FindStringSubmatch(line)
	if m == nil {
		return
	}
	user, ip := m[1], m[2]
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
	reason := "auto-whitelist: ssh " + truncateReason(user, 120)
	addCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := addToJabaliAllowlist(addCtx, ip, reason, conf.TTL); err != nil {
		log.Warn("login-allowlist: ssh allowlist add failed", "ip", ip, "user", user, "err", err)
		return
	}
	log.Info("login-allowlist: ssh login allowlisted", "ip", ip, "user", user, "ttl", conf.TTL)
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
	return map[string]any{"enabled": p.Enabled, "ttl_hours": ttlHours}, nil
}

func init() {
	Default.Register("security.crowdsec.login_allowlist.apply", csLoginAllowlistApplyHandler)
}
