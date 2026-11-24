package commands

// login_ban_exempt.go — the login IPs CrowdSec never bans (ADR-0153).
//
// A panel admin login or a root SSH login keeps its source IP here for the
// configured window. The IPs are rendered into a CrowdSec postoverflow
// whitelist: an alert a scenario raises for one of them is dropped before it
// becomes a ban, while the AppSec WAF still inspects every request the IP
// sends. They used to go on the jabali allowlist, which also skips the WAF.
// Bans that do not come from this server's scenarios (the community
// blocklist, a ban added by hand) are not stopped by a postoverflow
// whitelist; the extra blocklists leave these IPs out (listProtected).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// Vars so tests can point them at temp paths.
var (
	loginBanExemptPath      = "/etc/crowdsec/postoverflows/s01-whitelist/zz-jabali-login-ips.yaml"
	loginBanExemptStatePath = "/var/lib/jabali/login-ban-exempt.json"
	crowdsecConfigDir       = "/etc/crowdsec"
)

const (
	// loginAutoReasonPrefix marks the allowlist entries older panels and
	// agents added for logins. allowlists.add sends them here instead, and
	// dropAutoAllowlistEntries removes the ones already on the allowlist.
	loginAutoReasonPrefix  = "auto-whitelist: "
	loginBanExemptMaxTTL   = 8760 * time.Hour
	loginBanExemptMaxEntry = 1000
)

// loginBanExemptMu serialises every read-modify-write of the state file and
// the whitelist it renders.
var loginBanExemptMu sync.Mutex

type loginBanExemptEntry struct {
	Reason  string    `json:"reason"`
	Expires time.Time `json:"expires"`
}

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// loginBanExemptIP returns the canonical form of s when it may be exempted:
// a single address that is not loopback, unspecified, link-local, multicast,
// zoned or carrier-grade NAT (100.64/10, shared by everyone behind the same
// NAT or tailnet exit).
func loginBanExemptIP(s string) (string, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return "", false
	}
	a = a.Unmap()
	if a.Zone() != "" || a.IsLoopback() || a.IsUnspecified() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || cgnatPrefix.Contains(a) {
		return "", false
	}
	return a.String(), true
}

// readLoginBanExemptState returns the stored entries, dropping any key that
// is not a canonical exemptable IP. A missing or unreadable file is an empty
// set: losing it removes exemptions, it never adds one.
func readLoginBanExemptState() map[string]loginBanExemptEntry {
	m := map[string]loginBanExemptEntry{}
	b, err := os.ReadFile(loginBanExemptStatePath)
	if err != nil {
		return m
	}
	if json.Unmarshal(b, &m) != nil {
		return map[string]loginBanExemptEntry{}
	}
	for k := range m {
		if c, ok := loginBanExemptIP(k); !ok || c != k {
			delete(m, k)
		}
	}
	return m
}

func writeLoginBanExemptState(m map[string]loginBanExemptEntry) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(loginBanExemptStatePath), 0o700); err != nil {
		return err
	}
	tmp := loginBanExemptStatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, loginBanExemptStatePath)
}

func pruneLoginBanExempt(m map[string]loginBanExemptEntry, now time.Time) {
	for k, e := range m {
		if !now.Before(e.Expires) {
			delete(m, k)
		}
	}
}

// renderLoginBanExempt is the postoverflow whitelist for ips. Each ip is the
// canonical text of a parsed address, so quoting it is all it needs.
func renderLoginBanExempt(ips []string) string {
	var b strings.Builder
	b.WriteString("# Managed by jabali: login IPs CrowdSec never bans (ADR-0153).\n")
	b.WriteString("# The AppSec WAF still inspects them. Hand edits are overwritten;\n")
	b.WriteString("# with no login IP left this file is removed.\n")
	b.WriteString("name: jabali/login-ips\n")
	b.WriteString("description: \"Never ban the source IP of a recent jabali admin or root SSH login\"\n")
	b.WriteString("whitelist:\n  reason: \"jabali login IP\"\n  ip:\n")
	for _, ip := range ips {
		fmt.Fprintf(&b, "    - %q\n", ip)
	}
	return b.String()
}

// syncLoginBanExempt makes the whitelist file list exactly the IPs in m and
// reloads CrowdSec when the file changed. CrowdSec checks the whole config
// first; a file it rejects is put back as it was, so the reload (or the
// restart reloadCrowdsec falls back to) never meets a config CrowdSec cannot
// start with. Caller holds loginBanExemptMu.
func syncLoginBanExempt(m map[string]loginBanExemptEntry) (changed bool, err error) {
	if _, err := os.Stat(crowdsecConfigDir); err != nil {
		return false, fmt.Errorf("crowdsec config dir: %w", exec.ErrNotFound)
	}
	ips := make([]string, 0, len(m))
	for ip := range m {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	old, rerr := os.ReadFile(loginBanExemptPath)
	had := rerr == nil
	var want []byte
	if len(ips) > 0 {
		want = []byte(renderLoginBanExempt(ips))
	}
	if (!had && want == nil) || (had && bytes.Equal(old, want)) {
		return false, nil
	}
	put := func(body []byte) error {
		if body == nil {
			if err := os.Remove(loginBanExemptPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(loginBanExemptPath), 0o755); err != nil {
			return err
		}
		tmp := loginBanExemptPath + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, loginBanExemptPath)
	}
	if err := put(want); err != nil {
		return false, fmt.Errorf("write login IP whitelist: %v", err)
	}
	// A fresh context: callers hold short request deadlines, and a reload cut
	// off half-way makes reloadCrowdsec try a restart on a dead context.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if out, err := execCommandContext(ctx, "crowdsec", "-c", filepath.Join(crowdsecConfigDir, "config.yaml"), "-t", "-error").CombinedOutput(); err != nil {
		var prev []byte
		if had {
			prev = old
		}
		if rerr := put(prev); rerr != nil {
			return false, fmt.Errorf("crowdsec rejected the login IP whitelist (%v: %s) and putting the old one back failed: %v", err, strings.TrimSpace(string(out)), rerr)
		}
		return false, fmt.Errorf("crowdsec rejected the login IP whitelist, old one kept: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := reloadCrowdsec(ctx); err != nil {
		return true, err
	}
	return true, nil
}

// addLoginBanExempt keeps ip off CrowdSec's bans until now+ttl, sliding the
// window when ip is already there. CrowdSec reloads only when ip is new (or an
// expired one was pruned), not on a refresh.
func addLoginBanExempt(ip, reason string, ttl time.Duration, now time.Time) error {
	c, ok := loginBanExemptIP(ip)
	if !ok {
		return fmt.Errorf("%q cannot be exempted from bans", ip)
	}
	if ttl <= 0 {
		ttl = 168 * time.Hour
	}
	if ttl > loginBanExemptMaxTTL {
		ttl = loginBanExemptMaxTTL
	}
	if _, err := os.Stat(crowdsecConfigDir); err != nil {
		return fmt.Errorf("crowdsec config dir: %w", exec.ErrNotFound)
	}
	loginBanExemptMu.Lock()
	defer loginBanExemptMu.Unlock()
	m := readLoginBanExemptState()
	pruneLoginBanExempt(m, now)
	if _, there := m[c]; !there && len(m) >= loginBanExemptMaxEntry {
		return fmt.Errorf("login IP list is full (%d)", loginBanExemptMaxEntry)
	}
	m[c] = loginBanExemptEntry{Reason: truncateReason(reason, 200), Expires: now.Add(ttl)}
	if err := writeLoginBanExemptState(m); err != nil {
		return fmt.Errorf("save login IPs: %v", err)
	}
	changed, err := syncLoginBanExempt(m)
	if changed {
		requestListsRerender()
	}
	return err
}

// removeLoginBanExempt drops ip; found=false when it was not on the list.
func removeLoginBanExempt(ip string, now time.Time) (found bool, err error) {
	a, perr := netip.ParseAddr(strings.TrimSpace(ip))
	if perr != nil {
		return false, nil
	}
	loginBanExemptMu.Lock()
	defer loginBanExemptMu.Unlock()
	m := readLoginBanExemptState()
	pruneLoginBanExempt(m, now)
	k := a.Unmap().String()
	if _, found = m[k]; !found {
		return false, nil
	}
	delete(m, k)
	if err := writeLoginBanExemptState(m); err != nil {
		return true, fmt.Errorf("save login IPs: %v", err)
	}
	changed, err := syncLoginBanExempt(m)
	if changed {
		requestListsRerender()
	}
	return true, err
}

// maintainLoginBanExempt drops expired IPs, empties the list while the
// login setting is off (its push is best-effort), and brings the whitelist
// file back in line with the list after a failed write or reload.
func maintainLoginBanExempt(now time.Time) error {
	loginBanExemptMu.Lock()
	defer loginBanExemptMu.Unlock()
	m := readLoginBanExemptState()
	n := len(m)
	pruneLoginBanExempt(m, now)
	if !readLoginAllowlistConf().Enabled {
		m = map[string]loginBanExemptEntry{}
	}
	if len(m) != n {
		if err := writeLoginBanExemptState(m); err != nil {
			return fmt.Errorf("save login IPs: %v", err)
		}
	}
	changed, err := syncLoginBanExempt(m)
	if changed {
		requestListsRerender()
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil // no CrowdSec on this box
	}
	return err
}

// clearLoginBanExempt empties the list (the login setting was switched off).
func clearLoginBanExempt() error {
	loginBanExemptMu.Lock()
	defer loginBanExemptMu.Unlock()
	empty := map[string]loginBanExemptEntry{}
	if err := writeLoginBanExemptState(empty); err != nil {
		return fmt.Errorf("save login IPs: %v", err)
	}
	changed, err := syncLoginBanExempt(empty)
	if changed {
		requestListsRerender()
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	return err
}

// loginBanExemptPrefixes is the unexpired list as prefixes, for the extra
// blocklists' protected set.
func loginBanExemptPrefixes(now time.Time) []netip.Prefix {
	m := readLoginBanExemptState()
	pruneLoginBanExempt(m, now)
	out := make([]netip.Prefix, 0, len(m))
	for k := range m {
		if a, err := netip.ParseAddr(k); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

// runLoginBanExemptMaintenance runs maintainLoginBanExempt at start and then
// hourly (every five minutes while it fails, e.g. CrowdSec not up yet).
func runLoginBanExemptMaintenance(ctx context.Context, log *slog.Logger) {
	for {
		wait := time.Hour
		if err := maintainLoginBanExempt(time.Now()); err != nil {
			log.Warn("login-allowlist: login IP whitelist upkeep failed", "err", err)
			wait = 5 * time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// ---- security.crowdsec.login_ips.{add,list,remove} -------------------------

type csLoginIPAddParams struct {
	Value      string `json:"value"`
	Reason     string `json:"reason"`
	Expiration string `json:"expiration,omitempty"`
}

func loginBanExemptError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: agentwire.MsgCrowdSecNotInstalled}
	}
	return csInternal("login IP whitelist", err)
}

// loginBanExemptAdd validates and adds; shared by login_ips.add and the
// allowlists.add route for older panels.
func loginBanExemptAdd(value, reason, expiration string) (any, error) {
	if _, ok := loginBanExemptIP(value); !ok {
		return nil, csInvalidArg("value must be a single public or private IP (not loopback, link-local, multicast or 100.64.0.0/10)")
	}
	if l := len(reason); l < 3 || l > 200 {
		return nil, csInvalidArg("reason length must be 3..200")
	}
	ttl := 168 * time.Hour
	if exp := strings.TrimSpace(expiration); exp != "" {
		d, _, err := parseCrowdsecDuration(exp)
		if err != nil || d <= 0 || d > loginBanExemptMaxTTL {
			return nil, csInvalidArg("expiration must be a duration up to 8760h (e.g. \"168h\", \"7d\")")
		}
		ttl = d
	}
	if err := addLoginBanExempt(value, reason, ttl, time.Now()); err != nil {
		return nil, loginBanExemptError(err)
	}
	return map[string]any{"value": value, "ban_only": true}, nil
}

func csLoginIPsAddHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p csLoginIPAddParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	return loginBanExemptAdd(p.Value, p.Reason, p.Expiration)
}

type csLoginIPWire struct {
	Value     string `json:"value"`
	Reason    string `json:"reason"`
	ExpiresAt string `json:"expires_at"`
}

func csLoginIPsListHandler(_ context.Context, _ json.RawMessage) (any, error) {
	m := readLoginBanExemptState()
	pruneLoginBanExempt(m, time.Now())
	items := make([]csLoginIPWire, 0, len(m))
	for ip, e := range m {
		items = append(items, csLoginIPWire{Value: ip, Reason: e.Reason, ExpiresAt: e.Expires.UTC().Format(time.RFC3339)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Value < items[j].Value })
	return map[string]any{"items": items}, nil
}

func csLoginIPsRemoveHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	found, err := removeLoginBanExempt(p.Value, time.Now())
	if err != nil {
		return nil, loginBanExemptError(err)
	}
	if !found {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "login IP not found"}
	}
	return map[string]any{}, nil
}

func init() {
	Default.Register("security.crowdsec.login_ips.add", csLoginIPsAddHandler)
	Default.Register("security.crowdsec.login_ips.list", csLoginIPsListHandler)
	Default.Register("security.crowdsec.login_ips.remove", csLoginIPsRemoveHandler)
}
