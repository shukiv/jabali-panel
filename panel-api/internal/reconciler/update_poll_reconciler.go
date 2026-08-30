package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// M53.1 Updates Center — periodic background check (ADR-0118).
//
// Without this, update_state (the Updates page stat cards + "Last Checked")
// only refreshes when an admin opens the page or clicks Check now. This tick
// runs system.update_check + system.apt_check on a slow cadence so the page is
// fresh without a visit, and fires an M14 system.update.available notification
// when the security-update count rises (e.g. 0 -> N after a new DSA).

const updatePollInterval = 6 * time.Hour

type updateCheckView struct {
	CurrentSHA  string `json:"current_sha"`
	BehindCount int    `json:"behind_count"`
}

type aptCheckView struct {
	Total          int        `json:"total"`
	SecurityTotal  int        `json:"security_total"`
	RebootRequired bool       `json:"reboot_required"`
	LastAppliedAt  *time.Time `json:"last_applied_at"`
	// Error is the structured diagnostic (JAB-10) set when the check itself
	// failed (apt lock, repo unreachable, …). When present, Total/SecurityTotal
	// are meaningless and must not be persisted (JAB-311).
	Error *struct {
		Reason   string `json:"reason"`
		Command  string `json:"command"`
		ExitCode int    `json:"exit_code"`
	} `json:"error"`
}

func (r *Reconciler) reconcileUpdatePoll(ctx context.Context) {
	if r.updateState == nil || r.agent == nil {
		return
	}
	// Self-gate to the slow cadence (apt-get update is not free). First tick
	// after boot runs because lastUpdatePoll is zero.
	if !r.lastUpdatePoll.IsZero() && time.Since(r.lastUpdatePoll) < updatePollInterval {
		return
	}
	r.lastUpdatePoll = time.Now()

	// Previous security count, to detect a rise (only notify on new exposure).
	prevSecurity := 0
	if st, err := r.updateState.Get(ctx); err == nil {
		prevSecurity = st.AptSecurity
	}

	// Panel (git) check — cheap. Forward the configured release channel
	// (GH #445) so a stable box's "behind" count compares against stable, not
	// development — the agent's default for an empty channel. JAB-311: the
	// background poll was sending nil here, silently checking every stable box
	// against development; mirror the HTTP adapter (admin_updates.jabaliCheck).
	channel := "development"
	if r.serverSettings != nil {
		if s, serr := r.serverSettings.Get(ctx); serr == nil && s != nil && s.ReleaseChannel == "stable" {
			channel = "stable"
		}
	}
	if raw, err := r.agent.Call(ctx, "system.update_check", map[string]any{"channel": channel}); err == nil {
		var v updateCheckView
		if json.Unmarshal(raw, &v) == nil {
			_ = r.updateState.UpsertJabali(ctx, v.BehindCount, v.CurrentSHA, time.Now())
		}
	} else {
		r.log.Debug("update poll: panel check failed", "err", err)
	}

	// apt check — runs apt-get update + parses upgradable list.
	raw, err := r.agent.Call(ctx, "system.apt_check", nil)
	if err != nil {
		r.log.Debug("update poll: apt check failed", "err", err)
		return
	}
	var av aptCheckView
	if json.Unmarshal(raw, &av) != nil {
		return
	}
	// JAB-311: on a structured check failure (JAB-10 — apt lock, repo
	// unreachable, …) the counts are meaningless; persisting them would clobber
	// the last-known-good total with a misleading 0 ("0 updates" as if healthy).
	// Skip all persistence + the security-rise notification, as the HTTP
	// aptCheck adapter does.
	if av.Error != nil {
		r.log.Debug("update poll: apt check structured failure — preserving last-known-good",
			"reason", av.Error.Reason, "exit", av.Error.ExitCode)
		return
	}
	_ = r.updateState.UpsertApt(ctx, av.Total, av.SecurityTotal, time.Now())
	// JAB-353: refresh the OS-patch status the Updates Center reports.
	_ = r.updateState.UpsertAptStatus(ctx, av.LastAppliedAt, av.RebootRequired)

	// Notify only when security updates newly appear or increase, so we don't
	// re-alert every 6h for the same standing count.
	if r.notificationQueue != nil && av.SecurityTotal > prevSecurity {
		body := fmt.Sprintf("%d security update%s waiting (of %d package upgrade%s). Review them on the Updates page.",
			av.SecurityTotal, updPlural(av.SecurityTotal), av.Total, updPlural(av.Total))
		if _, perr := r.notificationQueue.Publish(ctx, notifications.Envelope{
			EventKind: "system.update.available",
			Severity:  "info",
			Title:     fmt.Sprintf("%d security update%s available", av.SecurityTotal, updPlural(av.SecurityTotal)),
			Body:      body,
			Deeplink:  "/jabali-admin/updates",
		}); perr != nil {
			r.log.Warn("update poll: publish system.update.available failed", "err", perr)
		}
	}
}

func updPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
