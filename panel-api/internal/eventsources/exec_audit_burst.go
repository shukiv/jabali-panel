package eventsources

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// M39 Step 7 — exec.audit.burst event source.
//
// Polls panel-agent's `security.audit.recent` (which tails ausearch
// against tag jabali_susp_exec from M39's narrow auditd rules) every
// 5 minutes. Fires exec.audit.burst per-user when their event count
// in the last 5-minute window crosses execAuditBurstThreshold.
//
// Per-user dedupe with a 30-min cool-off so a script running shell
// commands in a tight loop doesn't drown the operator's M14 channel.
// Threshold is hard-coded at 20 events per 5-min window — close to
// the cliff between "noisy WordPress install running curl + php
// repeatedly" (legitimate, ~10/window) and "compromised user grinding
// reverse-shell attempts" (50+/window).
//
// Disabled when AgentCaller is nil (no way to fetch events).

const (
	execAuditTick           = 5 * time.Minute
	execAuditCoolOff        = 30 * time.Minute
	execAuditBurstThreshold = 20
	// execAuditFetchLimit caps the per-tick agent call. 500 is enough
	// to count any plausible burst across all users in the window;
	// a real flood will be > 500 and we fire on the first user that
	// crosses anyway.
	execAuditFetchLimit = 500
	execAuditCallTimeout = 10 * time.Second
)

func runExecAuditBurst(ctx context.Context, d Deps) {
	if d.Agent == nil {
		d.Log.Info("eventsources: exec.audit.burst disabled (no agent)")
		return
	}
	d.Log.Info("eventsources: exec.audit.burst started", "tick", execAuditTick.String())
	execAuditPass(ctx, d)
	tick := time.NewTicker(execAuditTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		execAuditPass(ctx, d)
	}
}

// auditEventDTO matches panel-agent/internal/commands/security_audit.go
// `auditEvent`. Kept as a private type here so the eventsource can
// decode without importing the panel-agent package.
type auditEventDTO struct {
	Timestamp string `json:"ts"`
	Auid      int    `json:"auid"`
	Username  string `json:"username,omitempty"`
	Comm      string `json:"comm,omitempty"`
	Exe       string `json:"exe,omitempty"`
	PPID      int    `json:"ppid,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

type auditEventsDTO struct {
	Events []auditEventDTO `json:"events"`
}

// systemctlExePaths are the real systemctl binary paths. An exec of
// systemctl under a TENANT login-uid is always one of jabali's own
// management polls (the #280 cron last_run harvester shells
// `sudo -u <tenant> systemctl --user show jabali-cron-<id>`, once per
// cron per tick) — tenants are SFTP-only with no shell or sudo, so they
// cannot invoke systemctl themselves. Counting these jabali-initiated
// execs in the suspicious-exec burst fires continuous false positives
// on any tenant with several crons (GH #403). A genuine exploit spawns
// a shell/interpreter (sh, bash, python, perl, …) at a different exe
// path, which is still counted — so excluding the real systemctl binary
// removes jabali's own noise without blunting the detector.
var systemctlExePaths = map[string]bool{
	"/usr/bin/systemctl": true,
	"/bin/systemctl":     true,
}

// isJabaliManagementExec reports whether an audit event is one of
// jabali's own management execs that must not count toward a tenant's
// suspicious-exec burst (GH #403). Matched on the resolved exe path
// (not comm) so an attacker's binary merely named "systemctl" elsewhere
// is still counted.
func isJabaliManagementExec(e auditEventDTO) bool {
	if systemctlExePaths[e.Exe] {
		return true
	}
	// Exe can be empty on some auditd rows (e.g. truncated); fall back
	// to comm, which auditd derives from the binary's basename.
	if e.Exe == "" && e.Comm == "systemctl" {
		return true
	}
	return false
}

func execAuditPass(ctx context.Context, d Deps) {
	callCtx, cancel := context.WithTimeout(ctx, execAuditCallTimeout)
	defer cancel()
	raw, err := d.Agent.Call(callCtx, "security.audit.recent", map[string]any{
		"limit": execAuditFetchLimit,
	})
	if err != nil {
		d.Log.Debug("exec.audit.burst: agent call failed", "err", err)
		return
	}
	var resp auditEventsDTO
	if err := json.Unmarshal(raw, &resp); err != nil {
		d.Log.Debug("exec.audit.burst: decode failed", "err", err)
		return
	}
	if len(resp.Events) == 0 {
		return
	}

	// Bucket by username + filter to the current 5-min window.
	// security.audit.recent doesn't take a `since`; we filter
	// here so a slow tick (operator restarted panel) doesn't
	// double-fire on stale events that already crossed the
	// threshold last cycle.
	cutoff := d.Now().Add(-execAuditTick)
	counts := map[string]int{}
	for _, e := range resp.Events {
		if e.Username == "" {
			continue
		}
		// GH #403: don't count jabali's own management polls.
		if isJabaliManagementExec(e) {
			continue
		}
		ts, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			// Auditd entries with unparseable timestamps still
			// count — better than silently dropping the row.
			counts[e.Username]++
			continue
		}
		if ts.Before(cutoff) {
			continue
		}
		counts[e.Username]++
	}

	// Stable order so the log line for a multi-user fire is
	// deterministic across ticks. Doesn't affect what fires —
	// every over-threshold user gets its own envelope.
	users := make([]string, 0, len(counts))
	for u := range counts {
		users = append(users, u)
	}
	sort.Strings(users)

	for _, u := range users {
		n := counts[u]
		if n < execAuditBurstThreshold {
			continue
		}
		dedupe := fmt.Sprintf("user=%s exec_burst>=%d", u, execAuditBurstThreshold)
		if !shouldFire(ctx, d, "exec.audit.burst", dedupe, execAuditCoolOff) {
			continue
		}
		_, err := d.Queue.Publish(ctx, notifications.Envelope{
			EventKind: "exec.audit.burst",
			Severity:  models.NotificationSeverityWarning,
			Title:     fmt.Sprintf("Suspicious exec burst: %d events from %s", n, u),
			Body: fmt.Sprintf(
				"User %s ran %d events tagged jabali_susp_exec in the last %s "+
					"(threshold %d). Likely indicates a compromised account "+
					"running shells/curl/wget repeatedly. Inspect with "+
					"`jabali audit by-user --user %s` or via the Exec audit tab. (%s)",
				u, n, execAuditTick.String(), execAuditBurstThreshold, u, dedupe,
			),
			Deeplink: "/admin/security?tab=malware&sub=exec_audit",
		})
		if err != nil {
			d.Log.Warn("exec.audit.burst: publish failed", "err", err, "user", u)
		}
	}
}
