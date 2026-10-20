// Package reconciler — daily bandwidth scan ticker (M13.1).
//
// Calls the agent's bandwidth.scan_day at 00:30 UTC daily, parses the
// per-domain {bytes, requests, day} stats, looks each domain name up in
// the panel DB, and upserts a row into bw_daily keyed by (domain_id,
// day). Non-existent domains in the agent response (e.g. logs left
// behind by a recently-deleted domain whose nginx site hasn't been
// pruned yet) are skipped silently. When the agent also reports the
// day's traffic per country (hosts with the GeoIP database), those
// rows replace the day's rows in bw_daily_country.
package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// bwTickerLogger is the slog-shaped subset the ticker uses; matches
// SSL ticker's pattern so we don't fight a `*slog.Logger` direct
// dep import in tests.
type bwTickerLogger interface {
	Info(string, ...interface{})
	Warn(string, ...interface{})
	Error(string, ...interface{})
}

// StartBandwidthTicker runs the daily goaccess-driven bandwidth scan.
// Tick interval is 24h; we don't try to align to wall-clock 00:30 here
// because the ticker fires N hours after panel-api's last boot — close
// enough for a per-day metric whose precision is "yesterday".
//
// The ticker runs in its own goroutine and stops when ctx is cancelled.
func StartBandwidthTicker(ctx context.Context, ag agent.AgentInterface, domainRepo repository.DomainRepository, bwRepo repository.BWDailyRepository, log bwTickerLogger) {
	if ag == nil || domainRepo == nil || bwRepo == nil {
		return
	}
	const interval = 24 * time.Hour
	t := time.NewTicker(interval)
	defer t.Stop()
	log.Info("bw_ticker starting", "interval", interval.String())

	// Run once 60s after boot so an admin watching a fresh install sees
	// data populate without waiting a day. The agent's scan handler is
	// idempotent.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(60 * time.Second):
			scanBandwidth(ctx, ag, domainRepo, bwRepo, log)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Info("bw_ticker stopping")
			return
		case <-t.C:
			scanBandwidth(ctx, ag, domainRepo, bwRepo, log)
		}
	}
}

// bwScanResponse is the bandwidth.scan_day reply. The agent's handler
// (panel-agent bandwidth_scan_day.go) decodes the same fixture,
// internal/agent/testdata/bandwidth_scan_day_response.json, so a field
// renamed on either side fails a test.
type bwScanResponse struct {
	Stats []struct {
		Domain        string `json:"domain"`
		BytesTotal    uint64 `json:"bytes_total"`
		RequestsTotal uint64 `json:"requests_total"`
		Day           string `json:"day"` // YYYY-MM-DD
		// Countries is absent from an older agent and on hosts without
		// the GeoIP database.
		Countries []struct {
			Code          string `json:"code"`
			RequestsTotal uint64 `json:"requests_total"`
			BytesTotal    uint64 `json:"bytes_total"`
		} `json:"countries,omitempty"`
	} `json:"stats"`
	Skipped []string `json:"skipped"`
}

func scanBandwidth(ctx context.Context, ag agent.AgentInterface, domainRepo repository.DomainRepository, bwRepo repository.BWDailyRepository, log bwTickerLogger) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	raw, err := ag.Call(callCtx, "bandwidth.scan_day", map[string]any{})
	if err != nil {
		log.Warn("bw_ticker: agent bandwidth.scan_day failed", "err", err)
		return
	}
	var resp bwScanResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		log.Error("bw_ticker: parse agent response failed", "err", err)
		return
	}
	if len(resp.Skipped) > 0 {
		log.Info("bw_ticker: agent skipped some logs", "count", len(resp.Skipped), "first", resp.Skipped[0])
	}

	written := 0
	for _, s := range resp.Stats {
		day, err := time.Parse("2006-01-02", s.Day)
		if err != nil {
			log.Warn("bw_ticker: invalid day from agent", "domain", s.Domain, "day", s.Day, "err", err)
			continue
		}
		// Look up domain by name; skip if no DB row (orphan log).
		dom, err := domainRepo.FindByName(callCtx, s.Domain)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				continue
			}
			log.Warn("bw_ticker: domain lookup failed", "domain", s.Domain, "err", err)
			continue
		}
		row := &models.BWDaily{
			DomainID:      dom.ID,
			Day:           day,
			BytesTotal:    s.BytesTotal,
			RequestsTotal: s.RequestsTotal,
		}
		if err := bwRepo.Upsert(callCtx, row); err != nil {
			log.Warn("bw_ticker: upsert failed", "domain", s.Domain, "err", err)
			continue
		}
		written++

		// No countries means the scan had no GeoIP data, not that the day
		// had no visitors, so the day's rows from an earlier scan stay.
		countries := make([]models.BWDailyCountry, 0, len(s.Countries))
		for _, c := range s.Countries {
			if !isCountryCode(c.Code) {
				continue
			}
			countries = append(countries, models.BWDailyCountry{
				CountryCode:   c.Code,
				BytesTotal:    c.BytesTotal,
				RequestsTotal: c.RequestsTotal,
			})
		}
		if len(countries) > 0 {
			if err := bwRepo.ReplaceCountriesForDay(callCtx, dom.ID, day, countries); err != nil {
				log.Warn("bw_ticker: country rows failed", "domain", s.Domain, "err", err)
			}
		}
	}
	log.Info("bw_ticker: scan complete", "domains", len(resp.Stats), "written", written, "skipped", len(resp.Skipped))
}

// isCountryCode accepts the agent's two-letter upper-case country codes
// (ISO 3166-1 alpha-2, or ZZ). Anything else is dropped rather than stored.
func isCountryCode(c string) bool {
	return len(c) == 2 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z'
}
