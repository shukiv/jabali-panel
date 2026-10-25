// Package eventsources — snuffleupagus.go (M41 Wave D, ADR-0088)
//
// Polls agent.snuffleupagus.fetch_incidents on a 60s tick, persists each
// row via SnuffleupagusRepository.InsertIncident, and emits one M14
// envelope per batch (summary line; per-incident is too noisy for a
// rule-violation pipeline). Severity scales with action:
//   * block        → warning
//   * simulated_block → info
//   * log          → info
package eventsources

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const snuffleupagusTick = 60 * time.Second

// snuffleupagusCooldown debounces the M14 dispatch — every minute we
// poll, but if the rule fires 1000x in a minute we still only send one
// envelope. Cooldown gives the operator a single notification per 10 min
// burst window.
const snuffleupagusCooldown = 10 * time.Minute

// runSnuffleupagusIngest is the M41 ingest loop. Disabled when Snuffleupagus
// repo or Agent is nil.
func runSnuffleupagusIngest(ctx context.Context, d Deps) {
	if d.Agent == nil || d.Snuffleupagus == nil {
		return
	}
	d.Log.Info("eventsources: snuffleupagus.ingest started", "tick", snuffleupagusTick.String())

	since := snuffleupagusIngestStart(ctx, d, time.Now().UTC())
	tick := time.NewTicker(snuffleupagusTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		next, n := snuffleupagusIngestPass(ctx, d, since)
		if !next.IsZero() {
			since = next
		}
		_ = n
	}
}

// snuffleupagusIngestStart is where the first pass reads from: the last
// minute, or just after the newest stored incident when that is more
// recent, so a panel restart does not store the last minute twice.
func snuffleupagusIngestStart(ctx context.Context, d Deps, now time.Time) time.Time {
	since := now.Add(-snuffleupagusTick)
	latest, err := d.Snuffleupagus.LatestIncidentTs(ctx)
	if err != nil {
		d.Log.Warn("eventsources: snuffleupagus latest incident lookup failed", "err", err)
		return since
	}
	if latest != nil && latest.After(since) {
		return latest.Add(time.Microsecond)
	}
	return since
}

func snuffleupagusIngestPass(ctx context.Context, d Deps, since time.Time) (time.Time, int) {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := d.Agent.Call(callCtx, "snuffleupagus.fetch_incidents", map[string]any{
		// journalctl reads whole seconds; since_us lets the agent skip what
		// the previous pass already returned from that second (GH #2262).
		"since":    since.Format(time.RFC3339),
		"since_us": since.UnixMicro(),
		"limit":    500,
	})
	if err != nil {
		d.Log.Debug("eventsources: snuffleupagus.fetch_incidents call failed", "err", err)
		return time.Time{}, 0
	}
	var fetched struct {
		Incidents []struct {
			Ts         time.Time `json:"ts"`
			RuleName   string    `json:"rule_name"`
			Action     string    `json:"action"`
			SourceIP   string    `json:"source_ip"`
			RequestURI string    `json:"request_uri"`
			PhpVersion string    `json:"php_version"`
			Domain     string    `json:"domain"`
			Owner      string    `json:"owner"`
			Raw        string    `json:"raw"`
		} `json:"incidents"`
		NextSince time.Time `json:"next_since"`
	}
	if err := json.Unmarshal(raw, &fetched); err != nil {
		d.Log.Warn("eventsources: snuffleupagus.fetch_incidents parse failed", "err", err)
		return time.Time{}, 0
	}
	if len(fetched.Incidents) == 0 {
		next := fetched.NextSince
		if next.IsZero() {
			next = time.Now().UTC()
		}
		return next, 0
	}

	// Persist + bucket by action for the dispatch envelope.
	var blockCount, simCount, logCount int
	sites := map[string][2]*string{}
	for _, in := range fetched.Incidents {
		ph := in.PhpVersion
		// GH #2262: a field longer than its column failed the insert, and
		// the incident went unrecorded.
		uri := snufFit(in.RequestURI, 2048)
		raw := snufFit(in.Raw, 16000)
		row := &models.SnuffleupagusIncident{
			Ts:         in.Ts,
			RuleName:   snufFit(in.RuleName, 128),
			Action:     models.SnuffleupagusAction(in.Action),
			RequestURI: stringPtr(uri),
			PhpVersion: stringPtr(ph),
			Raw:        stringPtr(raw),
		}
		key := in.Owner + "/" + in.Domain
		site, ok := sites[key]
		if !ok {
			site[0], site[1] = snuffleupagusSite(ctx, d, in.Owner, in.Domain)
			sites[key] = site
		}
		row.DomainID, row.Domain = site[0], site[1]
		row.SourceIP = models.SnuffleupagusSourceIP(in.SourceIP)
		if err := d.Snuffleupagus.InsertIncident(ctx, row); err != nil {
			d.Log.Warn("eventsources: snuffleupagus.InsertIncident failed", "err", err)
			continue
		}
		switch row.Action {
		case models.SnuffleupagusActionBlock:
			blockCount++
		case models.SnuffleupagusActionSimulatedBlock:
			simCount++
		default:
			logCount++
		}
	}

	// Simulated-block + log-only incidents are sp's dry-run mode --
	// useful for tuning rules, useless as an operator notification.
	// Only fire when an actual block landed; persisted rows stay in
	// the DB so the Security UI can still surface them.
	if blockCount == 0 {
		return fetched.NextSince, len(fetched.Incidents)
	}

	dedupe := fmt.Sprintf("snuf-batch=%s", fetched.Incidents[len(fetched.Incidents)-1].Ts.Format(time.RFC3339Nano))
	if !shouldFire(ctx, d, "snuffleupagus.incident.detected", dedupe, snuffleupagusCooldown) {
		return fetched.NextSince, len(fetched.Incidents)
	}

	severity := "warning"
	title := fmt.Sprintf("Snuffleupagus %d incident(s): %d blocked / %d simulated / %d logged",
		len(fetched.Incidents), blockCount, simCount, logCount)
	body := snuffleupagusBatchBody(fetched.Incidents)
	if _, err := d.Queue.Publish(ctx, notifications.Envelope{
		EventKind: "snuffleupagus.incident.detected",
		Severity:  severity,
		Title:     title,
		Body:      body,
		Deeplink:  "/jabali-admin/security?tab=snuffleupagus",
	}); err != nil {
		d.Log.Warn("eventsources: snuffleupagus.incident.detected publish failed", "err", err)
	}

	return fetched.NextSince, len(fetched.Incidents)
}

func snuffleupagusBatchBody(items []struct {
	Ts         time.Time `json:"ts"`
	RuleName   string    `json:"rule_name"`
	Action     string    `json:"action"`
	SourceIP   string    `json:"source_ip"`
	RequestURI string    `json:"request_uri"`
	PhpVersion string    `json:"php_version"`
	Domain     string    `json:"domain"`
	Owner      string    `json:"owner"`
	Raw        string    `json:"raw"`
}) string {
	const max = 10
	out := ""
	for i, it := range items {
		if i >= max {
			out += fmt.Sprintf("...+%d more\n", len(items)-max)
			break
		}
		out += fmt.Sprintf("%s  %s  %s  %s\n",
			it.Ts.Format("15:04:05"), it.Action, it.RuleName, it.SourceIP)
	}
	return out
}

// snuffleupagusSite is the site an incident ran on, as its domain ID and
// name (GH #2262). The agent names the domain only when the account that
// logged the line owns /home/<owner>, but a directory under its domains/
// is still that account's choice, so the name counts only when the
// panel's domain of that name belongs to <owner>. Nil otherwise.
func snuffleupagusSite(ctx context.Context, d Deps, owner, name string) (domainID, domain *string) {
	if owner == "" || name == "" || d.Domains == nil || d.Users == nil {
		return nil, nil
	}
	dom, err := d.Domains.FindByName(ctx, name)
	if err != nil || dom == nil {
		return nil, nil
	}
	u, err := d.Users.FindByID(ctx, dom.UserID)
	if err != nil || u == nil || u.Username == nil || *u.Username != owner {
		return nil, nil
	}
	id, n := dom.ID, dom.Name
	return &id, &n
}

// snufFit cuts s to at most n characters.
func snufFit(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// SnuffleupagusRepoLite is the slice runSnuffleupagusIngest uses.
// Defined where it's consumed so deps stay narrow (Go interface idiom).
type SnuffleupagusRepoLite = repository.SnuffleupagusRepository
