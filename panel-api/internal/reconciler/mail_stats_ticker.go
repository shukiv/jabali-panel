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
	Log      bwTickerLogger
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
			if time.Since(lastPrune) > 24*time.Hour {
				if n, err := deps.Stats.Prune(ctx, time.Now().UTC().Add(-mailStatsRetention)); err != nil {
					deps.Log.Warn("mail stats: prune failed", "err", err)
				} else if n > 0 {
					deps.Log.Info("mail stats: pruned", "rows", n)
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
