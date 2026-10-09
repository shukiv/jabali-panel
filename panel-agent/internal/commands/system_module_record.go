package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// GH #2056: the result of the last system.module.install per module. Every
// install path ends in this agent (the Server Settings toggle, the Modules
// card's Retry and the reconciler's convergence pass), so the agent records
// the outcome and system.module.status reports it. The Modules card can then
// say why an install failed instead of timing out with no reason. The record
// is a file, so it survives an agent restart, and a first mail install that
// restarts jabali-panel mid-run doesn't lose it either.

// moduleInstallScript is install.sh for the module installs. A var (not
// installShPath directly) so tests can point it at a stub.
var moduleInstallScript = installShPath

// moduleInstallStateDir holds one <key>.json record per module.
var moduleInstallStateDir = "/var/lib/jabali-panel/module-install"

// moduleInstallSummaryMax caps the reason shown in the panel.
const moduleInstallSummaryMax = 400

type moduleInstallRecord struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Log        string `json:"log,omitempty"`
	FinishedAt string `json:"finished_at"`
}

func moduleInstallRecordPath(key string) string {
	return filepath.Join(moduleInstallStateDir, key+".json")
}

// writeModuleInstallRecord saves the outcome of one install. Best-effort: a
// record that can't be written must not change the install's own result.
func writeModuleInstallRecord(key string, rec moduleInstallRecord) {
	if err := os.MkdirAll(moduleInstallStateDir, 0o700); err != nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(moduleInstallStateDir, key+".json.*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), moduleInstallRecordPath(key))
}

func readModuleInstallRecord(key string) *moduleInstallRecord {
	data, err := os.ReadFile(moduleInstallRecordPath(key))
	if err != nil {
		return nil
	}
	var rec moduleInstallRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	return &rec
}

// recordModuleInstallFailure saves a failed install with a short reason taken
// from install.sh's output.
func recordModuleInstallFailure(key, output string) {
	summary, logPath := summarizeModuleInstallFailure(output)
	writeModuleInstallRecord(key, moduleInstallRecord{
		Error:      summary,
		Log:        logPath,
		FinishedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// mergeModuleInstallRecord adds a recorded failure to a status probe. A module
// that is installed and running reports no error, whatever the record says:
// it was fixed since (by hand, or by an install that didn't go through here).
func mergeModuleInstallRecord(resp *moduleStatusResponse, rec *moduleInstallRecord) {
	if rec == nil || rec.OK || (resp.Installed && resp.Active) {
		return
	}
	resp.LastError = rec.Error
	resp.LastErrorAt = rec.FinishedAt
	resp.InstallLog = rec.Log
}

var (
	ansiEscapeRE          = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	installLogLineRE      = regexp.MustCompile(`(?:^\[i\] install log: |^Full install log at: )(\S+)`)
	installDiedFieldRE    = regexp.MustCompile(`^\s+(function|line|command)\s+:\s?(.*)$`)
	installDieLineRE      = regexp.MustCompile(`^\[✗\]\s*(.*)$`)
	installModulePrefixRE = regexp.MustCompile(`^install\.sh --install-module [a-z]+: `)
)

// summarizeModuleInstallFailure turns install.sh's combined output into one
// line for the panel, plus the install log path when install.sh printed it.
//
//  1. install.sh's crash handler ("install.sh died:") names the function, line
//     and command that failed. It wins: its log tail can repeat earlier,
//     non-fatal [✗] lines.
//  2. Otherwise the last [✗] line at the start of a line: install.sh's _die.
//  3. Otherwise the last non-empty line, for example systemd-run refusing to
//     start the unit.
func summarizeModuleInstallFailure(output string) (summary, logPath string) {
	lines := strings.Split(ansiEscapeRE.ReplaceAllString(output, ""), "\n")

	var died bool
	var fn, line, command, lastDie, lastLine string
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		if m := installLogLineRE.FindStringSubmatch(l); m != nil && logPath == "" {
			logPath = m[1]
		}
		if strings.Contains(l, "install.sh died:") {
			died = true
			continue
		}
		if died && (fn == "" || line == "" || command == "") {
			if m := installDiedFieldRE.FindStringSubmatch(l); m != nil {
				switch m[1] {
				case "function":
					fn = strings.TrimSpace(m[2])
				case "line":
					line = strings.TrimSpace(m[2])
				case "command":
					command = strings.TrimSpace(m[2])
				}
				continue
			}
		}
		if m := installDieLineRE.FindStringSubmatch(l); m != nil {
			lastDie = strings.TrimSpace(m[1])
		}
		if strings.TrimSpace(l) != "" {
			lastLine = strings.TrimSpace(l)
		}
	}

	switch {
	case died && fn != "":
		summary = fmt.Sprintf("install.sh stopped in %s (line %s) at: %s", fn, line, command)
	case lastDie != "":
		summary = installModulePrefixRE.ReplaceAllString(lastDie, "")
	case lastLine != "":
		summary = lastLine
	default:
		summary = "install failed without output"
	}
	if len(summary) > moduleInstallSummaryMax {
		summary = strings.ToValidUTF8(summary[:moduleInstallSummaryMax], "") + "…"
	}
	return summary, logPath
}

// In-flight installs, including ones waiting on aptMu behind another install.
var (
	moduleInstallInFlightMu sync.Mutex
	moduleInstallInFlight   = map[string]int{}
)

func moduleInstallBegin(key string) {
	moduleInstallInFlightMu.Lock()
	moduleInstallInFlight[key]++
	moduleInstallInFlightMu.Unlock()
}

func moduleInstallEnd(key string) {
	moduleInstallInFlightMu.Lock()
	if moduleInstallInFlight[key] > 1 {
		moduleInstallInFlight[key]--
	} else {
		delete(moduleInstallInFlight, key)
	}
	moduleInstallInFlightMu.Unlock()
}

// moduleInstalling reports an install queued or running in this agent, or a
// jabali-module-install-<key> unit still running (an install that outlived an
// agent restart, or one stuck in "activating").
func moduleInstalling(ctx context.Context, key string) bool {
	moduleInstallInFlightMu.Lock()
	n := moduleInstallInFlight[key]
	moduleInstallInFlightMu.Unlock()
	if n > 0 {
		return true
	}
	out, _ := execCommandContext(ctx, "systemctl", "is-active", "jabali-module-install-"+key).Output()
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "reloading":
		return true
	}
	return false
}
