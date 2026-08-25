package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// M53 Updates Center — auto-update host apply (ADR-0118).
//
// Converges the panel's desired auto-update state onto the host. apt security
// patches run via the stock unattended-upgrades path (toggled by the
// 20auto-upgrades periodic flags, scheduled by an apt-daily-upgrade.timer
// drop-in). jabali self-update is opt-in: a dedicated jabali-autoupdate.timer
// runs `jabali update -f` (default OFF — a bad self-update can brick the
// panel).
//
// The handler is self-gating: it renders the desired file contents, writes
// only the files whose bytes actually changed, daemon-reloads only when
// something changed, and flips a timer's enable state only when it differs.
// That makes it safe for the reconciler to call every tick (no per-tick
// daemon-reload churn — see feedback_per_tick_idempotent_loops).

type autoupdateApplyParams struct {
	AptEnabled    bool   `json:"apt_enabled"`
	AptTime       string `json:"apt_time"`
	JabaliEnabled bool   `json:"jabali_enabled"`
	JabaliTime    string `json:"jabali_time"`
}

type autoupdateApplyResponse struct {
	Changed       bool `json:"changed"`
	AptEnabled    bool `json:"apt_enabled"`
	JabaliEnabled bool `json:"jabali_enabled"`
}

const (
	aptAutoUpgradesFile    = "/etc/apt/apt.conf.d/20auto-upgrades"
	aptUpgradeOverrideDir  = "/etc/systemd/system/apt-daily-upgrade.timer.d"
	aptUpgradeOverrideFile = "/etc/systemd/system/apt-daily-upgrade.timer.d/jabali-schedule.conf"
	aptUpgradeTimer        = "apt-daily-upgrade.timer"
	// GH #1224: report the apply to the Updates Center immediately. The background
	// poll only runs every 6h, so without this the page shows a stale "Last
	// Applied" for hours after the scheduled run — which reads as "never ran".
	aptUpgradeSvcDropinDir  = "/etc/systemd/system/apt-daily-upgrade.service.d"
	aptUpgradeSvcDropinFile = "/etc/systemd/system/apt-daily-upgrade.service.d/jabali-report.conf"
	jabaliAutoSvcFile       = "/etc/systemd/system/jabali-autoupdate.service"
	jabaliAutoTimerFile     = "/etc/systemd/system/jabali-autoupdate.timer"
	jabaliAutoTimer         = "jabali-autoupdate.timer"
)

var autoupdateHHMM = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func systemAutoupdateApplyHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p autoupdateApplyParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
		}
	}
	// Defence in depth: panel-api already validates, but the agent never
	// renders an unvalidated value into a systemd OnCalendar line.
	if !autoupdateHHMM.MatchString(p.AptTime) || !autoupdateHHMM.MatchString(p.JabaliTime) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "apt_time/jabali_time must be HH:MM (24h)"}
	}

	changed := false

	// --- apt periodic flags -------------------------------------------------
	flag := "0"
	if p.AptEnabled {
		flag = "1"
	}
	aptConf := fmt.Sprintf("APT::Periodic::Update-Package-Lists \"%s\";\nAPT::Periodic::Unattended-Upgrade \"%s\";\n", flag, flag)
	if wrote, err := writeIfChanged(aptAutoUpgradesFile, aptConf); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	} else if wrote {
		changed = true
	}

	// --- apt-daily-upgrade.timer schedule override --------------------------
	if err := os.MkdirAll(aptUpgradeOverrideDir, 0o755); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir override dir: %v", err)}
	}
	// Reset OnCalendar (blank line clears the stock value) then pin ours.
	aptOverride := fmt.Sprintf("[Timer]\nOnCalendar=\nOnCalendar=*-*-* %s:00\nRandomizedDelaySec=0\nPersistent=true\n", p.AptTime)
	if wrote, err := writeIfChanged(aptUpgradeOverrideFile, aptOverride); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	} else if wrote {
		changed = true
	}

	// --- apt-daily-upgrade.service ExecStartPost: push state to the panel now ---
	// (GH #1224) So the Updates Center reflects a scheduled apply within seconds
	// instead of waiting up to 6h for the background poll. Best-effort ('-'): a
	// refresh failure must never fail the apt upgrade itself.
	if err := os.MkdirAll(aptUpgradeSvcDropinDir, 0o755); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir service dropin dir: %v", err)}
	}
	aptReport := "[Service]\nExecStartPost=-/usr/local/bin/jabali update apt-refresh\n"
	if wrote, err := writeIfChanged(aptUpgradeSvcDropinFile, aptReport); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	} else if wrote {
		changed = true
	}

	// --- jabali self-update unit + timer ------------------------------------
	jabaliSvc := "[Unit]\nDescription=Jabali panel self-update (M53)\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=oneshot\nExecStart=/usr/local/bin/jabali update -f\n"
	if wrote, err := writeIfChanged(jabaliAutoSvcFile, jabaliSvc); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	} else if wrote {
		changed = true
	}
	jabaliTimer := fmt.Sprintf("[Unit]\nDescription=Schedule Jabali panel self-update (M53)\n\n[Timer]\nOnCalendar=*-*-* %s:00\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n", p.JabaliTime)
	if wrote, err := writeIfChanged(jabaliAutoTimerFile, jabaliTimer); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	} else if wrote {
		changed = true
	}

	if changed {
		if out, err := execCommandContext(ctx, "systemctl", "daemon-reload").CombinedOutput(); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("daemon-reload: %v: %s", err, out)}
		}
	}

	// --- timer enable state (only flip on drift) ----------------------------
	if setTimerEnabled(ctx, aptUpgradeTimer, p.AptEnabled) {
		changed = true
	}
	if setTimerEnabled(ctx, jabaliAutoTimer, p.JabaliEnabled) {
		changed = true
	}

	return autoupdateApplyResponse{
		Changed:       changed,
		AptEnabled:    p.AptEnabled,
		JabaliEnabled: p.JabaliEnabled,
	}, nil
}

// writeIfChanged writes content to path only when the current bytes differ.
// Returns whether a write happened.
func writeIfChanged(path, content string) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		return false, nil
	}
	if err := atomicWrite(path, content); err != nil {
		return false, err
	}
	return true, nil
}

// setTimerEnabled enables --now or disables --now a timer, but only when its
// current is-enabled state differs from desired. Returns whether it flipped.
func setTimerEnabled(ctx context.Context, timer string, want bool) bool {
	out, _ := execCommandContext(ctx, "systemctl", "is-enabled", timer).Output()
	enabled := strings.TrimSpace(string(out)) == "enabled"
	if enabled == want {
		return false
	}
	if want {
		_ = execCommandContext(ctx, "systemctl", "enable", "--now", timer).Run()
	} else {
		_ = execCommandContext(ctx, "systemctl", "disable", "--now", timer).Run()
	}
	return true
}

func init() {
	Default.Register("system.autoupdate_apply", systemAutoupdateApplyHandler)
}
