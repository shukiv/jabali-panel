package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// repair_fpm — GH #889/#953 self-diagnosis. A per-user PHP-FPM master
// (jabali-fpm@<user>.service) that crash-loops never binds its socket, so nginx
// answers /run/php/jabali-<user>/fpm.sock "No such file or directory" and the
// site 500/502s. The exit reason lives in `journalctl -u jabali-fpm@<user>` —
// which every such report currently costs a round-trip to ask the reporter to
// paste. This detector enumerates every down master and names the likely cause
// in one `jabali repair --diagnose`, no username needed.
//
// The unit is Type=simple + Restart=on-failure/RestartSec=5s, so `systemctl
// restart` returns 0 even for a master that forks then immediately exits, and a
// crash-looping unit reads as "activating (auto-restart)", not "failed". So we
// judge health by the observed ActiveState/SubState (+ NRestarts as evidence),
// never by a restart exit code.
//
// Detect-only (fix: nil): the cause is a bad extension / php.ini directive /
// AppArmor deny / Snuffleupagus rule baked into the pool config — a blind
// restart won't clear it, and guessing a fix would mask the real problem. The
// reason string points the operator straight at it.

// Injectable exec seams (GH #994): production runs the real host commands; unit
// tests swap these for canned output so `go test` never touches systemd/journald.
var fpmDiagListMasters = func() (string, error) {
	out, err := exec.Command("systemctl", "list-units", "jabali-fpm@*.service",
		"--all", "--no-legend", "--plain", "--no-pager").Output()
	return string(out), err
}

var fpmDiagShow = func(unit string) (string, error) {
	out, err := exec.Command("systemctl", "show", unit,
		"-p", "ActiveState", "-p", "SubState", "-p", "Result",
		"-p", "ExecMainStatus", "-p", "NRestarts").Output()
	return string(out), err
}

// fpmDiagJournal returns the unit's recent log lines (message text only, -o cat).
//
// Deliberately NOT scoped to the current InvocationID: a Restart=on-failure
// master crash-loops every RestartSec, so each read lands on a DIFFERENT (often
// fresh, not-yet-errored) invocation and the current one's fatal isn't logged
// yet — the InvocationID filter then returns nothing. The unit-scoped window
// instead captures the last several crash cycles, which repeat the SAME config
// fatal; extractFPMFailureReason scans newest-first so the current cause wins.
// We only ever journal a master we've already found DOWN, so recent lines ARE
// the current failure, not a stale one. journalctl exits non-zero on an empty
// match; that is not an error here.
var fpmDiagJournal = func(unit string) string {
	out, _ := exec.Command("journalctl", "-u", unit, "-n", "80", "--no-pager", "-o", "cat").Output()
	return string(out)
}

// fpmCausePatterns are SPECIFIC root-cause fatals (the thing an operator needs:
// which extension/directive/rule broke). Preferred over consequence patterns.
var fpmCausePatterns = []string{
	"unable to load dynamic library",
	"failed loading",
	"cannot load",
	"unknown entry",
	"is not a valid",
	"must be specified",
	"cannot bind",
	"address already in use",
	"snuffleupagus",
	"php fatal",
	"fatal error",
}

// fpmConsequencePatterns are GENERIC downstream errors — useful only when no
// specific cause line is present (e.g. "failed to load configuration file").
var fpmConsequencePatterns = []string{
	"failed to load configuration",
	"fpm initialization failed",
	"error:",
}

// extractFPMFailureReason pulls the decisive php-fpm error line(s) out of a
// unit's journal text. Pure (no exec) so it is unit-tested with canned input.
//
// Two-tier: a crash on a bad directive logs the specific cause ("unknown entry
// 'x'") THEN generic consequences ("failed to load configuration file", "FPM
// initialization failed"). A naive newest-first scan returns the consequences
// and buries the cause, so we prefer cause lines and fall back to consequence
// lines only when none is present. Within a tier: newest-first (most recent
// crash cycle wins), deduped, at most maxLines, presented chronologically.
func extractFPMFailureReason(journalText string, maxLines int) string {
	if maxLines < 1 {
		maxLines = 1
	}
	lines := strings.Split(journalText, "\n")
	pick := func(patterns []string) []string {
		var hits []string
		seen := map[string]bool{}
		for i := len(lines) - 1; i >= 0 && len(hits) < maxLines; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			low := strings.ToLower(line)
			for _, p := range patterns {
				if strings.Contains(low, p) {
					if !seen[line] {
						seen[line] = true
						hits = append(hits, line)
					}
					break
				}
			}
		}
		// newest-first → chronological.
		for i, j := 0, len(hits)-1; i < j; i, j = i+1, j-1 {
			hits[i], hits[j] = hits[j], hits[i]
		}
		return hits
	}
	hits := pick(fpmCausePatterns)
	if len(hits) == 0 {
		hits = pick(fpmConsequencePatterns)
	}
	return strings.Join(hits, "; ")
}

func parseSystemctlShow(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if i := strings.IndexByte(l, '='); i > 0 {
			m[l[:i]] = strings.TrimSpace(l[i+1:])
		}
	}
	return m
}

// fpmMasterDiagnostic reports whether a user's FPM master is unhealthy and, if
// so, a one-line reason (state + evidence + the journal's fatal line).
func fpmMasterDiagnostic(username string) (unhealthy bool, reason string) {
	unit := "jabali-fpm@" + username + ".service"
	props := parseSystemctlShow(func() string { s, _ := fpmDiagShow(unit); return s }())
	active := props["ActiveState"]
	sub := props["SubState"]
	// Healthy is the only all-clear; everything else (activating/auto-restart,
	// failed, inactive with no socket) is worth a line in a diagnostic run.
	if active == "active" && sub == "running" {
		return false, ""
	}

	cause := extractFPMFailureReason(fpmDiagJournal(unit), 2)
	if cause == "" {
		cause = "no php-fpm fatal matched in the journal — inspect: journalctl -u " + unit + " -n 80"
	}

	state := active
	if sub != "" {
		state = active + "/" + sub
	}
	if n := props["NRestarts"]; n != "" && n != "0" {
		state += ", " + n + " restarts"
	}
	return true, fmt.Sprintf("%s [%s] — %s", unit, state, cause)
}

// detectFPMMastersDown enumerates every jabali-fpm@<user> master and reports the
// unhealthy ones with their cause. Read-only.
func detectFPMMastersDown(_ repairCtx) (bool, string, error) {
	list, err := fpmDiagListMasters()
	if err != nil {
		// No masters registered, or systemctl unavailable (e.g. a container):
		// nothing to diagnose, not a failure of the detector itself.
		return false, "no per-user PHP-FPM masters found", nil
	}
	var down []string
	for _, line := range strings.Split(list, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		if !strings.HasPrefix(unit, "jabali-fpm@") || !strings.HasSuffix(unit, ".service") {
			continue
		}
		user := strings.TrimSuffix(strings.TrimPrefix(unit, "jabali-fpm@"), ".service")
		if user == "" {
			continue
		}
		if bad, reason := fpmMasterDiagnostic(user); bad {
			down = append(down, reason)
		}
	}
	if len(down) == 0 {
		return false, "all per-user PHP-FPM masters healthy", nil
	}
	sort.Strings(down)
	return true, fmt.Sprintf("%d PHP-FPM master(s) unhealthy:\n    - %s",
		len(down), strings.Join(down, "\n    - ")), nil
}
