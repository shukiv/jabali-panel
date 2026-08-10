// Package pdnsrecursor manages /etc/powerdns/recursor.forwards on the
// panel-agent side. Every edit goes through atomic write + validator +
// rec_control reload + SOA-proxy probe + rollback-on-fail.
//
// The package is agent-only (panel-api never touches rec_control or the
// forwards file directly). See ADR-0047 for the design decision record.
package pdnsrecursor

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry is one line in recursor.forwards: "<zone>=<ip>(:port)?".
//
// Callers (handler + CLI) default Port explicitly before calling AddZone;
// the Manager is strict (rejects Port==0) so two independent callers can't
// pick subtly different defaults.
type Entry struct {
	Zone string // apex name, no trailing dot, lowercase.
	Addr string // IPv4 literal or bracketed IPv6 stripped — no brackets here.
	Port int    // REQUIRED (>0). Typical value: 5300 (pdns-server loopback bind).
}

// String renders the Entry as a recursor.forwards line (no trailing newline).
// For IPv6 addresses (containing ":"), the Addr is wrapped in [...] per
// pdns-recursor config syntax.
func (e Entry) String() string {
	addr := e.Addr
	if strings.Contains(addr, ":") {
		addr = "[" + addr + "]"
	}
	return fmt.Sprintf("%s=%s:%d", e.Zone, addr, e.Port)
}

// Options configures a Manager. All fields are optional except where
// documented; zero values get safe defaults.
type Options struct {
	// ForwardsPath defaults to /etc/powerdns/recursor.forwards.
	ForwardsPath string
	// BackupPath defaults to ForwardsPath + ".bak".
	BackupPath string
	// TempPath defaults to ForwardsPath + ".tmp".
	TempPath string
	// Exec runs rec_control. Default: osExecRunner with 5s per-call timeout.
	Exec ExecRunner
	// Prober probes "zone NS" against the recursor's loopback bind. Default:
	// netResolverProbe querying 127.0.0.1:53 with 2s timeout.
	Prober Prober
	// ProbeAttempts is how many times a post-add probe is retried before the
	// forwarder is rolled back (GH #896). A freshly-created authoritative zone
	// can take a couple of seconds after `rec_control reload-zones` to answer
	// the recursor's NS query; a single attempt used to roll the forwarder
	// back on that race, so the zone stayed unresolvable until the next
	// reconcile pass re-added it. Default 5.
	ProbeAttempts int
	// ProbeGap is the wait between probe attempts. Default 2s. Tests set it to
	// 0 for speed.
	ProbeGap time.Duration
	// Clock is injectable for deterministic tests; default time.Now.
	Clock func() time.Time
	// FileMode for the forwards file; default 0640.
	FileMode os.FileMode
	// Owner as "user:group" for chown. If empty (the production case),
	// New() auto-detects the recursor's group by probing `pdns-recursor`
	// then `pdns`; whichever exists wins, Owner resolves to `root:<group>`.
	//
	// Why this has to happen on EVERY write, not just install:
	// the agent runs as `root:jabali`, so os.WriteFile() on the tmp file
	// creates it with uid:gid root:jabali. Atomic rename preserves that,
	// clobbering install.sh's `root:pdns` seed. Without the chown here,
	// the pdns-group recursor daemon cannot read its own forwards file
	// and every `rec_control reload-zones` fails silently — which is the
	// bug that shipped to production on 2026-04-22.
	//
	// Set SkipChown:true to bypass both auto-detection and chown (tests).
	Owner string
	// SkipChown disables ownership management entirely — no detection,
	// no chown. Use in tests where the target directory is a tmpdir and
	// the `pdns`/`pdns-recursor` groups may not exist.
	SkipChown bool
}

// ExecRunner wraps exec.Cmd so tests can stub rec_control invocations.
type ExecRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Prober issues a lightweight DNS query to verify a forwarder is answering
// for the given zone. Returns nil on success; error on timeout/NXDOMAIN/etc.
type Prober interface {
	ProbeZone(ctx context.Context, zone string) error
}

// Manager serializes writes + reloads against recursor.forwards.
type Manager struct {
	mu   sync.Mutex
	opts Options
}

// New constructs a Manager with defaults applied.
func New(opts Options) (*Manager, error) {
	if opts.ForwardsPath == "" {
		opts.ForwardsPath = "/etc/powerdns/recursor.forwards"
	}
	if opts.BackupPath == "" {
		opts.BackupPath = opts.ForwardsPath + ".bak"
	}
	if opts.TempPath == "" {
		opts.TempPath = opts.ForwardsPath + ".tmp"
	}
	if opts.Exec == nil {
		opts.Exec = &osExecRunner{Timeout: 5 * time.Second}
	}
	if opts.Prober == nil {
		opts.Prober = &netResolverProbe{
			Addr:     "127.0.0.1:53",
			AuthAddr: "127.0.0.1:5300",
			Timeout:  2 * time.Second,
		}
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.ProbeAttempts < 1 {
		opts.ProbeAttempts = 5
	}
	if opts.ProbeGap == 0 {
		opts.ProbeGap = 2 * time.Second
	}
	if opts.FileMode == 0 {
		opts.FileMode = 0o640
	}
	if !opts.SkipChown && opts.Owner == "" {
		group, err := detectRecursorGroup()
		if err != nil {
			return nil, fmt.Errorf("pdnsrecursor: auto-detect recursor group: %w (pass SkipChown:true for tests, or set Owner explicitly)", err)
		}
		opts.Owner = "root:" + group
	}
	return &Manager{opts: opts}, nil
}

// detectRecursorGroup probes for the group the pdns-recursor daemon runs
// as. Debian trixie's pdns-recursor 5.2.x ships user/group `pdns` (shared
// with pdns-server). Earlier distros or a future package split may create
// a dedicated `pdns-recursor`. We prefer `pdns-recursor` when present,
// then fall back to `pdns`. Both absent → fatal: something is wrong with
// the package install.
func detectRecursorGroup() (string, error) {
	for _, g := range []string{"pdns-recursor", "pdns"} {
		if _, err := lookupGID(g); err == nil {
			return g, nil
		}
	}
	return "", fmt.Errorf("neither `pdns-recursor` nor `pdns` group exists — pdns-recursor package not installed?")
}

// AddZone is idempotent: no-op if the entry already matches exactly
// (same zone, addr, port).
//
// Flow: read current forwards → compute desired → if identical, return
// Changed=false. Else: write .tmp → validate → rename .bak → atomic
// rename → chown/chmod → rec_control reload-zones → probe → rollback
// on probe fail.
func (m *Manager) AddZone(ctx context.Context, e Entry) (Changed bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateEntry(e); err != nil {
		return false, fmt.Errorf("add_zone: %w", err)
	}

	current, err := m.readForwards()
	if err != nil {
		return false, fmt.Errorf("add_zone: read forwards: %w", err)
	}

	existing, ok := current[e.Zone]
	if ok && existing == e {
		return false, nil // idempotent no-op
	}

	desired := cloneMap(current)
	desired[e.Zone] = e

	if err := m.applyChange(ctx, desired, &e.Zone); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveZone is idempotent: no-op if zone absent.
func (m *Manager) RemoveZone(ctx context.Context, zone string) (Changed bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateZone(zone); err != nil {
		return false, fmt.Errorf("remove_zone: %w", err)
	}

	current, err := m.readForwards()
	if err != nil {
		return false, fmt.Errorf("remove_zone: read forwards: %w", err)
	}
	if _, ok := current[zone]; !ok {
		return false, nil // already gone
	}

	desired := cloneMap(current)
	delete(desired, zone)

	if err := m.applyChange(ctx, desired, nil); err != nil {
		return false, err
	}
	return true, nil
}

// List returns a deterministic snapshot of the current forwards file.
func (m *Manager) List(ctx context.Context) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.readForwards()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(current))
	for _, e := range current {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Zone < out[j].Zone })
	return out, nil
}

// readForwards parses ForwardsPath into a map[zone]Entry. Missing file is
// treated as empty — first-install case.
func (m *Manager) readForwards() (map[string]Entry, error) {
	data, err := os.ReadFile(m.opts.ForwardsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Entry{}, nil
		}
		return nil, err
	}
	out := map[string]Entry{}
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out[e.Zone] = e
	}
	return out, nil
}

// applyChange serializes desired, validates, rename-atomically swaps,
// calls rec_control reload-zones, and probes (if zoneToProbe != nil).
// Rolls back to .bak on probe failure.
func (m *Manager) applyChange(ctx context.Context, desired map[string]Entry, zoneToProbe *string) error {
	body := renderForwards(desired)

	// Write .tmp with final perms so an interrupted install never leaves
	// a 0600 tempfile readable only by root blocking recursor's next reload.
	if err := os.WriteFile(m.opts.TempPath, []byte(body), m.opts.FileMode); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	// Re-validate what we just wrote by parsing it back. Catches any
	// serialization bug at the earliest moment.
	if err := validateFile(m.opts.TempPath); err != nil {
		_ = os.Remove(m.opts.TempPath)
		return fmt.Errorf("validate tmp: %w", err)
	}
	// Chown the tmp BEFORE rename. The agent runs as root:jabali, so
	// WriteFile() above created tmp as root:jabali; if we rename first
	// and chown second, a rollback to empty would inherit root:jabali
	// and the pdns-group recursor still couldn't read. Chowning tmp
	// makes the atomic rename ship correct ownership in one step, so
	// any rollback never leaves an unreadable live file. See
	// ADR-0047 follow-up (2026-04-22 production incident).
	if err := applyOwnership(m.opts.TempPath, m.opts.FileMode, m.opts.Owner); err != nil {
		_ = os.Remove(m.opts.TempPath)
		return fmt.Errorf("chown tmp: %w", err)
	}

	// Rolling backup: single .bak. Rename live → .bak if live exists.
	if _, err := os.Stat(m.opts.ForwardsPath); err == nil {
		if err := os.Rename(m.opts.ForwardsPath, m.opts.BackupPath); err != nil {
			_ = os.Remove(m.opts.TempPath)
			return fmt.Errorf("rename to bak: %w", err)
		}
	}

	// Atomic swap .tmp → live. Ownership from the tmp carries over.
	if err := os.Rename(m.opts.TempPath, m.opts.ForwardsPath); err != nil {
		// Try to restore .bak so we don't leave a missing live file.
		_ = os.Rename(m.opts.BackupPath, m.opts.ForwardsPath)
		return fmt.Errorf("rename tmp to live: %w", err)
	}

	// Ask recursor to re-read forwards. 5s timeout.
	if _, err := m.opts.Exec.Run(ctx, "rec_control", "reload-zones"); err != nil {
		m.rollback()
		return fmt.Errorf("rec_control reload-zones: %w", err)
	}
	// Post-probe only on add (zoneToProbe != nil). Retry with a short backoff:
	// a freshly-created authoritative zone can take a couple of seconds after
	// reload-zones to answer the recursor's NS query, and a single-shot probe
	// used to roll the forwarder back on that race — leaving the zone
	// unresolvable until the NEXT reconcile pass re-added it (GH #896: a fresh
	// (sub)domain dead for up to minutes). probeForwarder wipes the cache
	// before each attempt so a just-cached NXDOMAIN can't pin every retry to
	// failure. Removals need no positive signal — absent zone = success.
	if zoneToProbe != nil && *zoneToProbe != "" {
		if err := m.probeForwarder(ctx, *zoneToProbe); err != nil {
			m.rollback()
			// Re-reload after rollback to re-unload any half-applied state.
			_, _ = m.opts.Exec.Run(ctx, "rec_control", "reload-zones")
			return fmt.Errorf("probe %s: %w", *zoneToProbe, err)
		}
	}
	return nil
}

// probeForwarder verifies the just-added forwarder answers for the zone,
// retrying up to ProbeAttempts times (ProbeGap apart) and wiping the recursor
// cache before each attempt. Returns nil on the first success, or the last
// error after exhausting attempts. Context cancellation aborts early.
func (m *Manager) probeForwarder(ctx context.Context, zone string) error {
	var err error
	for i := 0; i < m.opts.ProbeAttempts; i++ {
		// Clear any cached (possibly negative) answer so this attempt reflects
		// the current auth state, not a stale NXDOMAIN from before the zone
		// finished loading. Also clears the positive PRE-forwarder answer on
		// a re-bind (the original single-wipe rationale, PRs #86/#87/#88).
		_, _ = m.opts.Exec.Run(ctx, "rec_control", "wipe-cache", zone+"$")
		if err = m.opts.Prober.ProbeZone(ctx, zone); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if i < m.opts.ProbeAttempts-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.opts.ProbeGap):
			}
		}
	}
	return err
}

// rollback moves .bak back to live. Best-effort; if there's no .bak (first
// write after empty state), it truncates live to empty.
func (m *Manager) rollback() {
	if _, err := os.Stat(m.opts.BackupPath); err == nil {
		_ = os.Rename(m.opts.BackupPath, m.opts.ForwardsPath)
		return
	}
	// No bak → we were writing the first-ever forwards. Nuke live.
	_ = os.WriteFile(m.opts.ForwardsPath, []byte{}, m.opts.FileMode)
}

// renderForwards emits the file body, sorted by zone for deterministic
// diffs. One trailing newline.
func renderForwards(m map[string]Entry) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Managed by panel-agent (pdnsrecursor package). Do not hand-edit.\n")
	b.WriteString("# Edits land via pdns.recursor_add_zone / pdns.recursor_remove_zone\n")
	b.WriteString("# agent commands (atomic write + validator + rec_control reload +\n")
	b.WriteString("# SOA post-probe + rollback). See ADR-0047.\n")
	for _, k := range keys {
		b.WriteString(m[k].String())
		b.WriteString("\n")
	}
	return b.String()
}

func cloneMap(in map[string]Entry) map[string]Entry {
	out := make(map[string]Entry, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// validateFile parses+validates every non-comment line in a rendered
// forwards file. Used as a sanity pass on what we just wrote before
// renaming it over the live file.
func validateFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, err := parseLine(line); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	return nil
}

// applyOwnership chowns + chmods the target if the Owner field is set.
// If Owner is empty (test mode) only chmod runs. "user:group" format.
func applyOwnership(path string, mode os.FileMode, owner string) error {
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if owner == "" {
		return nil
	}
	// chown via net.LookupUid is stdlib-only. Split "user:group".
	parts := strings.SplitN(owner, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("owner %q: expected user:group", owner)
	}
	uid, err := lookupUID(parts[0])
	if err != nil {
		return fmt.Errorf("user %q: %w", parts[0], err)
	}
	gid, err := lookupGID(parts[1])
	if err != nil {
		return fmt.Errorf("group %q: %w", parts[1], err)
	}
	return os.Chown(path, uid, gid)
}

// ensure the non-test compile covers net import even when probe.go trims
// dependencies later.
var _ = net.LookupHost
var _ = filepath.Dir
