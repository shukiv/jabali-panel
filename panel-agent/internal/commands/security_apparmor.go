// security_apparmor — M40 (ADR-0086) thin wrapper around aa-status
// + aa-{enforce,complain} for the admin Security tab. Two commands:
//
//   security.apparmor.status    — list profiles + recent denials
//   security.apparmor.set_mode  — flip a single jabali-* profile
//                                 between complain and enforce
//
// We hard-code an allowlist of profile labels we own; arbitrary
// profile-name input from the panel is refused. Operator who needs
// to flip a non-allowlisted profile uses SSH.

package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"regexp"
	"strings"
	"time"
)

const apparmorCallTimeout = 10 * time.Second

// allowedProfiles enumerates the jabali-shipped profiles the panel
// can toggle. Adding a new profile here MUST be paired with adding
// the profile file under install/apparmor/.
// jabali-agent + jabali-kratos were DROPPED in M40.2/M40.3 (AA 4.x complain-mode
// unix-socket mediation bug on Debian 13 / Ubuntu 24.04 returns EACCES on
// connect() to mysqld.sock/pdns even in complain, breaking DNS + DB). They run
// intentionally unconfined, so they are NOT listed here — otherwise the status
// verb reports them as permanently "missing" (GH #679) which is a false alarm.
var allowedProfiles = map[string]bool{
	"jabali-panel":   true,
	"jabali-bulwark": true,
	"stalwart-mail":  true,
	"jabali-fpm-app": true,
}

// apparmorProfileFile maps a profile name to its on-disk file path.
// aa-enforce/aa-complain need a path that resolves either via PATH or
// directly to a profile file; profile names like "jabali-bulwark"
// don't resolve via PATH on Debian, so we always pass the explicit
// file path.
func apparmorProfileFile(name string) string {
	switch name {
	case "jabali-panel":
		return "/etc/apparmor.d/usr.local.bin.jabali-panel-api"
	case "jabali-bulwark":
		return "/etc/apparmor.d/usr.local.bin.jabali-bulwark"
	case "stalwart-mail":
		return "/etc/apparmor.d/usr.local.bin.stalwart-mail"
	case "jabali-fpm-app":
		return "/etc/apparmor.d/usr.local.libexec.jabali.fpm-exec"
	}
	return ""
}

type apparmorProfile struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// apparmorDenial is one parsed audit-trail row. complain-mode profiles
// emit "ALLOWED" but with a violation noted — we only return DENIED
// rows here (genuine enforce-mode blocks). Path is the requested
// resource; profile is which jabali-* daemon was confined.
type apparmorDenial struct {
	Timestamp     string `json:"timestamp"`
	Profile       string `json:"profile"`
	Operation     string `json:"operation"`
	Path          string `json:"path,omitempty"`
	RequestedMask string `json:"requested_mask,omitempty"`
	DeniedMask    string `json:"denied_mask,omitempty"`
	Comm          string `json:"comm,omitempty"`
	// GH #715: richer denial fields for the profile drawer.
	Exe   string `json:"exe,omitempty"`
	PID   string `json:"pid,omitempty"`
	FSUID string `json:"fsuid,omitempty"`
}

type apparmorStatusResponse struct {
	Enabled  bool              `json:"enabled"`
	Profiles []apparmorProfile `json:"profiles"`
	// Denials is the most recent N apparmor="DENIED" audit lines from
	// journalctl across the last 24h. Empty list when nothing's been
	// blocked (the desirable state for confined-and-correct daemons).
	Denials []apparmorDenial `json:"denials"`
	// Violations are complain-mode apparmor="ALLOWED" would-deny events (GH
	// #688) — a complain profile that logs these is NOT soak-ready to enforce.
	Violations []apparmorDenial `json:"violations"`
	// Reason: human-readable when Enabled=false (e.g. "kernel LSM
	// missing", "GRUB pending reboot").
	Reason string `json:"reason,omitempty"`
}

func mwApparmorStatusHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, apparmorCallTimeout)
	defer cancel()

	resp := apparmorStatusResponse{
		Profiles: []apparmorProfile{},
		Denials:  []apparmorDenial{},
	}

	out, err := osexec.CommandContext(ctx, "aa-status", "--json").Output()
	if err != nil {
		// aa-status returns non-zero on disabled / not-installed —
		// surface as Enabled=false, not as an internal error.
		resp.Enabled = false
		resp.Reason = fmt.Sprintf("aa-status: %v", err)
		return resp, nil
	}

	resp.Enabled = true

	// aa-status --json shape (Debian 13 / apparmor 3.x):
	// { "version": "...", "profiles": { "<name>": "enforce|complain", ... } }
	var raw struct {
		Profiles map[string]string `json:"profiles"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return resp, nil
	}

	for name, mode := range raw.Profiles {
		// Filter to jabali profiles + the system-service profiles we ship.
		if !strings.HasPrefix(name, "jabali-") &&
			name != "stalwart-mail" {
			continue
		}
		// Skip complain-mode child shadow profiles (e.g. "jabali-panel//null-/usr/sbin/...").
		if strings.Contains(name, "//") {
			continue
		}
		// jabali-ssh-shell ships flags=(unconfined) BY DESIGN — it's a userns
		// permission shim for bwrap (Ubuntu noble), not a confinement profile.
		// Reporting it as an "unconfined" security row is a false alarm.
		if name == "jabali-ssh-shell" {
			continue
		}
		resp.Profiles = append(resp.Profiles, apparmorProfile{
			Name: name,
			Mode: mode,
		})
	}

	// GH #679: report EXPECTED jabali profiles that are missing/unloaded instead
	// of silently omitting them. BUT distinguish two cases: a genuine failure
	// ("missing", red) vs a DELIBERATE kernel-gate skip. On kernels lacking
	// /sys/kernel/security/apparmor/features/unix (Debian 13 / Ubuntu 24.04
	// broken unix-socket mediation), install_apparmor intentionally does NOT load
	// the daemon profiles (#687) — attaching them would EACCES DNS/DB. That is
	// working-as-designed, not a failure, so report it as "kernel-gated" (neutral)
	// and explain it, rather than an alarming red "missing".
	// Profiles are now abi/3.0-pinned and LOADED in complain even on kernels
	// without unix mediation (GH #705-followup), so a profile absent from
	// aa-status on such a kernel is "kernel-gated" (couldn't load / was skipped
	// by an older install) rather than a genuine "missing" failure. Only surface
	// the explanatory reason if we actually have such a row.
	unixOK := apparmorUnixMediationAvailable()
	sawKernelGated := false
	for name := range allowedProfiles {
		if _, ok := raw.Profiles[name]; !ok {
			mode := "missing"
			if !unixOK {
				mode = "kernel-gated"
				sawKernelGated = true
			}
			resp.Profiles = append(resp.Profiles, apparmorProfile{Name: name, Mode: mode})
		}
	}
	if sawKernelGated && resp.Reason == "" {
		resp.Reason = "AppArmor unix-socket mediation is unavailable on this kernel " +
			"(no /sys/kernel/security/apparmor/features/unix — Debian 13 / Ubuntu 24.04). " +
			"Jabali profiles are abi/3.0-pinned so they normally load in complain here; a " +
			"profile still showing kernel-gated failed to load or predates this fix — run " +
			"jabali update. This is not a security failure."
	}

	// Best-effort denial scrape. Failures (journalctl missing, no
	// matches) leave Denials as the empty slice — never error here.
	resp.Denials = readApparmorEvents(ctx, "DENIED")
	resp.Violations = readApparmorEvents(ctx, "ALLOWED")
	return resp, nil
}

// AppArmor AVC audit-line format the kernel emits. Sample:
//
//	type=AVC msg=audit(1746371982.123:567): apparmor="DENIED"
//	operation="open" profile="jabali-panel" name="/etc/shadow"
//	pid=1234 comm="jabali-panel" requested_mask="r" denied_mask="r"
//	fsuid=0 ouid=0
//
// Fields are extracted per-key so extra/missing trailing fields are fine.
// Quoted values may contain spaces (tenant paths do) — the field regex has a
// quoted alternative so such values aren't truncated at the first space.
var (
	apparmorFieldRe   = regexp.MustCompile(`(\w+)="([^"]*)"|(\w+)=([^\s"]+)`)
	apparmorAuditTSRe = regexp.MustCompile(`audit\((\d+)\.\d+:\d+\)`)
)

const (
	apparmorDenialsWindow = 24 * time.Hour
	maxApparmorDenials    = 50
	// apparmorAuditLogPath is auditd's store. With auditd running (jabali
	// installs it — ADR-0085), the kernel routes AppArmor AVC records to the
	// audit subsystem: they land HERE and never reach the kernel journal, so
	// `journalctl -k --grep apparmor=` is structurally empty on every jabali
	// box while enforce-mode denials are actively happening. Read this first;
	// journalctl stays as the fallback for hosts without auditd.
	apparmorAuditLogPath   = "/var/log/audit/audit.log"
	apparmorAuditTailBytes = 8 << 20 // bounded tail per scrape
)

// readApparmorEvents returns the most recent AppArmor events with the given
// status ("DENIED" enforce blocks | "ALLOWED" complain would-deny), newest
// first, from the last 24h.
func readApparmorEvents(ctx context.Context, status string) []apparmorDenial {
	since := time.Now().Add(-apparmorDenialsWindow)
	if f, err := os.Open(apparmorAuditLogPath); err == nil {
		defer f.Close()
		if fi, serr := f.Stat(); serr == nil && fi.Size() > apparmorAuditTailBytes {
			_, _ = f.Seek(fi.Size()-apparmorAuditTailBytes, io.SeekStart)
		}
		if events := parseApparmorEventLines(f, status, since, maxApparmorDenials); len(events) > 0 {
			return events
		}
		// audit.log present but nothing in-window: fall through to the journal —
		// auditd may be installed but stopped, with fresh lines in the journal.
	}
	return readApparmorEventsJournal(ctx, status, since)
}

func readApparmorEventsJournal(ctx context.Context, status string, since time.Time) []apparmorDenial {
	if _, err := osexec.LookPath("journalctl"); err != nil {
		return []apparmorDenial{}
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := osexec.CommandContext(cctx,
		"journalctl",
		"-k",
		"--since", "24 hours ago",
		"--no-pager",
		"-q",
		"--grep", `apparmor="`+status+`"`,
	)
	stdout, err := cmd.Output()
	if err != nil {
		return []apparmorDenial{}
	}
	return parseApparmorEventLines(strings.NewReader(string(stdout)), status, since, maxApparmorDenials)
}

// parseApparmorEventLines scans chronological audit/journal lines and returns
// up to max in-window rows with the requested status, NEWEST FIRST. (The old
// implementation kept the first 50 chronological rows — on a noisy day the
// panel showed the OLDEST denials and the fresh ones were invisible.)
func parseApparmorEventLines(r io.Reader, status string, since time.Time, max int) []apparmorDenial {
	needle := `apparmor="` + status + `"`
	events := []apparmorDenial{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, needle) {
			continue
		}
		row := apparmorDenial{}
		if m := apparmorAuditTSRe.FindStringSubmatch(line); len(m) == 2 {
			var sec int64
			_, _ = fmt.Sscanf(m[1], "%d", &sec)
			if sec > 0 {
				ts := time.Unix(sec, 0).UTC()
				if ts.Before(since) {
					continue
				}
				row.Timestamp = ts.Format(time.RFC3339)
			}
		}
		for _, fm := range apparmorFieldRe.FindAllStringSubmatch(line, -1) {
			key, val := fm[1], fm[2]
			if key == "" {
				key, val = fm[3], fm[4]
			}
			switch key {
			case "profile":
				row.Profile = val
			case "operation":
				row.Operation = val
			case "name":
				row.Path = val
			case "requested_mask":
				row.RequestedMask = val
			case "denied_mask":
				row.DeniedMask = val
			case "comm":
				row.Comm = val
			case "exe":
				row.Exe = val
			case "pid":
				row.PID = val
			case "fsuid":
				row.FSUID = val
			}
		}
		// Rows without a profile are unrelated audit lines the grep surfaced.
		if row.Profile == "" {
			continue
		}
		events = append(events, row)
		// Bound memory on a huge tail: only the newest max matter.
		if len(events) > 4*max {
			events = events[len(events)-max:]
		}
	}
	if len(events) > max {
		events = events[len(events)-max:]
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events
}

type apparmorSetModeRequest struct {
	Profile string `json:"profile"`
	Mode    string `json:"mode"` // complain|enforce
}

func mwApparmorSetModeHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req apparmorSetModeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, mwInvalidArg("malformed JSON body")
	}
	if !allowedProfiles[req.Profile] {
		return nil, mwInvalidArg("profile not in allowlist")
	}
	var tool string
	switch req.Mode {
	case "enforce":
		tool = "aa-enforce"
	case "complain":
		tool = "aa-complain"
	default:
		return nil, mwInvalidArg("mode must be enforce|complain")
	}

	ctx, cancel := context.WithTimeout(ctx, apparmorCallTimeout)
	defer cancel()

	// aa-{enforce,complain} accepts either the profile-file path OR
	// the profile label. We pass the label — works on Debian 13 AA 3.x.
	profilePath := apparmorProfileFile(req.Profile)
	if profilePath == "" {
		return nil, mwInvalidArg("profile has no file path mapping")
	}
	out, err := osexec.CommandContext(ctx, tool, profilePath).CombinedOutput()
	if err != nil {
		return nil, mwInternal(fmt.Sprintf("%s %s: %s", tool, req.Profile, string(out)), err)
	}
	return map[string]any{
		"profile": req.Profile,
		"mode":    req.Mode,
	}, nil
}

func init() {
	Default.Register("security.apparmor.status", mwApparmorStatusHandler)
	Default.Register("security.apparmor.set_mode", mwApparmorSetModeHandler)
}

// apparmorUnixMediationAvailable reports whether the kernel supports AppArmor
// unix-socket mediation (features/unix). On kernels without it (Debian 13 /
// Ubuntu 24.04, AA 4.x bug), install_apparmor deliberately skips loading the
// jabali daemon profiles — so a profile absent there is a kernel-gate skip, not
// a failure (GH #687 / #679).
func apparmorUnixMediationAvailable() bool {
	_, err := os.Stat("/sys/kernel/security/apparmor/features/unix")
	return err == nil
}
