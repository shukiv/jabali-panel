package commands

// mail.stats_domain_sample — per-domain mail TRAFFIC counts for the round-3
// statistics drilldown (GH #873).
//
// Stalwart's Prometheus exporter only exposes server-WIDE counters (that is
// what mail.stats_sample scrapes). Per-domain traffic isn't in the metrics at
// all, so this verb derives it from the delivery log the mail-logs feature
// already parses. It is deliberately stateless: the panel drives the window by
// passing `since` (the timestamp of its last domain sample); this handler
// counts the events in (since, now] and returns them as deltas, plus the `now`
// it used so the panel can advance its watermark with no gap or overlap.
//
// Only LOCAL domains are counted — the panel passes its domain set in
// `local_domains`; a message to/from a remote domain contributes nothing, so
// the store never bloats with third-party domains.
//
// Admin-only for now (per GH #873 round-3 scope): the panel keeps this behind
// RequireAdmin. Per-user and tenant-scoped views are a later round.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type mailStatsDomainArgs struct {
	// Since is the RFC3339 lower bound (exclusive). Empty → last 15 minutes.
	Since string `json:"since"`
	// LocalDomains is the set of panel-managed domains to attribute traffic to.
	LocalDomains []string `json:"local_domains"`
}

// mailStatsDomainCount is one (domain, metric) delta for the window.
type mailStatsDomainCount struct {
	Domain string `json:"domain"`
	Metric string `json:"metric"` // sent | received | delivered | failed
	Count  int64  `json:"count"`
}

type mailStatsDomainResponse struct {
	// SampledAt is the upper bound of the counted window (RFC3339, UTC). The
	// panel stores the counts at this time and uses it as the next `since`.
	SampledAt string                 `json:"sampled_at"`
	Counts    []mailStatsDomainCount `json:"counts"`
}

// domainStatMaxLookback caps how far back a single sample will parse, so a
// long panel downtime (stale watermark) can't make one tick walk months of
// rotated logs. Traffic older than this is simply not backfilled.
const domainStatMaxLookback = 48 * time.Hour

func mailStatsDomainSampleHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var args mailStatsDomainArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "bad mail.stats_domain_sample args: " + err.Error()}
		}
	}

	now := time.Now().UTC()
	since := now.Add(-15 * time.Minute)
	if args.Since != "" {
		if t, err := time.Parse(time.RFC3339, args.Since); err == nil {
			since = t.UTC()
		}
	}
	if since.Before(now.Add(-domainStatMaxLookback)) {
		since = now.Add(-domainStatMaxLookback)
	}

	local := make(map[string]bool, len(args.LocalDomains))
	for _, d := range args.LocalDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			local[d] = true
		}
	}

	// counts[domain][metric] = n; seen dedupes an event line's contribution so
	// a message with two recipients in one domain counts that domain once.
	counts := map[string]map[string]int64{}
	seen := map[string]bool{}
	bump := func(domain, metric, qid string) {
		if domain == "" || !local[domain] {
			return
		}
		key := metric + "|" + domain + "|" + qid
		if qid != "" {
			if seen[key] {
				return
			}
			seen[key] = true
		}
		m := counts[domain]
		if m == nil {
			m = map[string]int64{}
			counts[domain] = m
		}
		m[metric]++
	}

	for _, path := range mailLogFiles() {
		// Skip rotated files whose newest line predates the window — bounds the
		// per-tick work to the current (and any in-window) log instead of
		// re-parsing every archived file forever. mtime is the last append, so
		// mtime < since means no line in this file is in (since, now].
		if fi, serr := os.Stat(path); serr == nil && fi.ModTime().Before(since) {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			p, ok := parseMailLogLine(sc.Text())
			if !ok || !p.ts.After(since) || p.ts.After(now) {
				continue
			}
			switch {
			case p.queued:
				bump(domainOfAddr(p.entry.From), "sent", p.queueID)
				for _, r := range p.recipients {
					bump(domainOfAddr(r), "received", p.queueID)
				}
			case p.rank == 2: // delivery.completed
				for _, r := range p.recipients {
					bump(domainOfAddr(r), "delivered", p.queueID)
				}
			case p.rank == 1: // delivery.failed
				for _, r := range p.recipients {
					bump(domainOfAddr(r), "failed", p.queueID)
				}
			}
		}
		f.Close()
	}

	out := make([]mailStatsDomainCount, 0, len(counts))
	for domain, metrics := range counts {
		for metric, n := range metrics {
			out = append(out, mailStatsDomainCount{Domain: domain, Metric: metric, Count: n})
		}
	}
	return mailStatsDomainResponse{SampledAt: now.Format(time.RFC3339), Counts: out}, nil
}

// domainOfAddr returns the lowercased domain part of an email address, or ""
// when the address has no "@".
func domainOfAddr(addr string) string {
	addr = strings.TrimSpace(strings.Trim(addr, `"`))
	at := strings.LastIndexByte(addr, '@')
	if at < 0 || at == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[at+1:])
}

func init() {
	Default.Register("mail.stats_domain_sample", mailStatsDomainSampleHandler)
}
