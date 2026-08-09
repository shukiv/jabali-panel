package reconciler

// GH #873: mail statistics collector. Every 15 minutes: one agent
// mail.stats_sample call (Stalwart Prometheus counters + queue size),
// stored verbatim into mail_stats_samples. Once a day it prunes samples
// older than 90 days. Self-gating on the mail module: hosts without a
// mail stack skip sampling entirely (the agent call would just fail
// against a stopped Stalwart and warn-spam the log).

import (
	"context"
	"encoding/json"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	mailStatsInterval  = 15 * time.Minute
	mailStatsRetention = 90 * 24 * time.Hour
)

type MailStatsTickerDeps struct {
	Agent    agent.AgentInterface
	Stats    repository.MailStatsRepository
	Settings repository.ServerSettingsRepository
	// Domains supplies the local-domain set for the per-domain traffic
	// sample (GH #873 round 3). Nil disables that half; the server-wide
	// sample still runs.
	Domains repository.DomainRepository
	Log     bwTickerLogger
}

// StartMailStatsTicker runs until ctx is cancelled. Call in a goroutine.
func StartMailStatsTicker(ctx context.Context, deps MailStatsTickerDeps) {
	if deps.Agent == nil || deps.Stats == nil || deps.Log == nil {
		return
	}
	deps.Log.Info("mail_stats_ticker starting", "interval", mailStatsInterval.String())
	t := time.NewTicker(mailStatsInterval)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			mailStatsTick(ctx, deps)
			domainStatsTick(ctx, deps)
			if time.Since(lastPrune) > 24*time.Hour {
				cutoff := time.Now().UTC().Add(-mailStatsRetention)
				if n, err := deps.Stats.Prune(ctx, cutoff); err != nil {
					deps.Log.Warn("mail stats: prune failed", "err", err)
				} else if n > 0 {
					deps.Log.Info("mail stats: pruned", "rows", n)
				}
				if n, err := deps.Stats.PruneDomain(ctx, cutoff); err != nil {
					deps.Log.Warn("mail stats: domain prune failed", "err", err)
				} else if n > 0 {
					deps.Log.Info("mail stats: pruned domain rows", "rows", n)
				}
				lastPrune = time.Now()
			}
		}
	}
}

func mailStatsTick(ctx context.Context, deps MailStatsTickerDeps) {
	if deps.Settings != nil {
		sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
		srv, err := deps.Settings.Get(sctx)
		scancel()
		if err == nil && srv != nil && !srv.MailEnabled {
			return
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	raw, err := deps.Agent.Call(cctx, "mail.stats_sample", map[string]any{})
	cancel()
	if err != nil {
		deps.Log.Warn("mail stats: sample failed", "err", err)
		return
	}
	var sample struct {
		Metrics    map[string]float64 `json:"metrics"`
		QueueSize  int64              `json:"queue_size"`
		EnabledNow bool               `json:"enabled_now"`
	}
	if err := json.Unmarshal(raw, &sample); err != nil {
		deps.Log.Warn("mail stats: decode sample failed", "err", err)
		return
	}
	if sample.Metrics == nil {
		sample.Metrics = map[string]float64{}
	}
	// Queue size rides along as a pseudo-gauge so history and charts get
	// it with zero special-casing.
	sample.Metrics["queue_size"] = float64(sample.QueueSize)

	if err := deps.Stats.InsertSamples(ctx, time.Now().UTC(), sample.Metrics); err != nil {
		deps.Log.Warn("mail stats: insert failed", "err", err)
		return
	}
	if sample.EnabledNow {
		deps.Log.Info("mail stats: stalwart prometheus exporter enabled (one-time)")
	}
}

// domainStatsTick derives per-domain traffic deltas (GH #873 round 3). It reads
// the current local-domain set, asks the agent to count delivery-log events
// since the last domain sample, and stores the deltas. The panel drives the
// window (LastDomainSampleAt -> agent's returned sampled_at) so there is no gap
// or overlap between ticks. Mail-module gating is inherited from mailStatsTick,
// which runs first in the same tick and returns early on a mail-less host.
func domainStatsTick(ctx context.Context, deps MailStatsTickerDeps) {
	if deps.Domains == nil {
		return
	}
	if deps.Settings != nil {
		sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
		srv, err := deps.Settings.Get(sctx)
		scancel()
		if err == nil && srv != nil && !srv.MailEnabled {
			return
		}
	}

	domains, _, err := deps.Domains.List(ctx, repository.ListOptions{Limit: 100000, OmitHeavyColumns: true})
	if err != nil {
		deps.Log.Warn("mail stats: list domains failed", "err", err)
		return
	}
	if len(domains) == 0 {
		return
	}
	names := make([]string, 0, len(domains))
	for _, d := range domains {
		if d.Name != "" {
			names = append(names, d.Name)
		}
	}

	since, err := deps.Stats.LastDomainSampleAt(ctx)
	if err != nil {
		deps.Log.Warn("mail stats: domain watermark failed", "err", err)
		return
	}
	args := map[string]any{"local_domains": names}
	if !since.IsZero() {
		args["since"] = since.UTC().Format(time.RFC3339)
	}

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	raw, err := deps.Agent.Call(cctx, "mail.stats_domain_sample", args)
	cancel()
	if err != nil {
		deps.Log.Warn("mail stats: domain sample failed", "err", err)
		return
	}
	var sample struct {
		SampledAt string `json:"sampled_at"`
		Counts    []struct {
			Domain string `json:"domain"`
			Metric string `json:"metric"`
			Count  int64  `json:"count"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(raw, &sample); err != nil {
		deps.Log.Warn("mail stats: decode domain sample failed", "err", err)
		return
	}
	if len(sample.Counts) == 0 {
		return
	}
	at, err := time.Parse(time.RFC3339, sample.SampledAt)
	if err != nil {
		at = time.Now().UTC()
	}
	counts := make([]repository.DomainCount, 0, len(sample.Counts))
	for _, c := range sample.Counts {
		counts = append(counts, repository.DomainCount{Domain: c.Domain, Metric: c.Metric, Count: c.Count})
	}
	if err := deps.Stats.InsertDomainSamples(ctx, at.UTC(), counts); err != nil {
		deps.Log.Warn("mail stats: insert domain samples failed", "err", err)
	}
}
