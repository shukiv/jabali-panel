// M47 Waves 4/6/8 — shared plumbing for the three Stalwart report ingest
// sources (DMARC, TLS-RPT, ARF).
//
// Stalwart parses the reports it receives at its report addresses and keeps
// them as DmarcExternalReport, TlsExternalReport and ArfExternalReport
// objects for about 30 days. Stalwart can neither filter nor sort these by
// receivedAt, so each pass lists every report id Stalwart holds and fetches
// only the ones this process has not handled yet. After a restart every
// report is fetched once more, and the repos' duplicate checks keep it from
// being stored twice.
//
// Anyone can email a report to a report address, so everything in one is
// untrusted: strings are clipped to their column, IPs parsed, enums mapped
// to a fixed set, and notifications summarised per pass so a flood of
// reports cannot become a flood of notifications.
package eventsources

import (
	"context"
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// StalwartReportClient is the slice of stalwartadmin.Client the report
// ingest sources use.
type StalwartReportClient interface {
	QueryIDs(ctx context.Context, typeName string, filter map[string]any) ([]string, error)
	GetMany(ctx context.Context, typeName string, ids, properties []string) ([]json.RawMessage, error)
}

const (
	mailReportIngestTick    = 5 * time.Minute
	mailReportIngestTimeout = 60 * time.Second
	// reportGetBatch keeps one response small: a DMARC report from a big
	// receiver can hold hundreds of records.
	reportGetBatch = 32
	// maxReportsPerPass bounds one pass; the rest wait for the next.
	maxReportsPerPass = 500
)

// reportIngest remembers which Stalwart report ids one source has handled
// in this process.
type reportIngest struct {
	typeName string
	seen     map[string]bool
}

func newReportIngest(typeName string) *reportIngest {
	return &reportIngest{typeName: typeName, seen: map[string]bool{}}
}

// runReportIngest runs pass now and then on every tick until ctx ends.
func runReportIngest(ctx context.Context, pass func(ctx context.Context)) {
	run := func() {
		cctx, cancel := context.WithTimeout(ctx, mailReportIngestTimeout)
		defer cancel()
		pass(cctx)
	}
	run()
	tick := time.NewTicker(mailReportIngestTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		run()
	}
}

// pass hands every report this process has not handled yet to importOne.
// importOne returns nil once a report is done with (stored, a duplicate, or
// unusable) and an error to try it again next pass.
func (ri *reportIngest) pass(ctx context.Context, d Deps, importOne func(ctx context.Context, raw json.RawMessage) error) {
	ids, err := d.StalwartAdmin.QueryIDs(ctx, ri.typeName, nil)
	if err != nil {
		d.Log.Warn("report-ingest: list failed", "type", ri.typeName, "err", err)
		return
	}
	listed := make(map[string]bool, len(ids))
	var fresh []string
	for _, id := range ids {
		listed[id] = true
		if !ri.seen[id] {
			fresh = append(fresh, id)
		}
	}
	// Stalwart expires reports; forget the ids it no longer has.
	for id := range ri.seen {
		if !listed[id] {
			delete(ri.seen, id)
		}
	}
	if len(fresh) > maxReportsPerPass {
		fresh = fresh[:maxReportsPerPass]
	}
	for start := 0; start < len(fresh); start += reportGetBatch {
		objs, err := d.StalwartAdmin.GetMany(ctx, ri.typeName, fresh[start:min(start+reportGetBatch, len(fresh))], nil)
		if err != nil {
			d.Log.Warn("report-ingest: fetch failed", "type", ri.typeName, "err", err)
			return
		}
		for _, raw := range objs {
			var head struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &head) != nil || head.ID == "" {
				continue
			}
			if err := importOne(ctx, raw); err != nil {
				d.Log.Warn("report-ingest: import failed, retrying next pass", "type", ri.typeName, "id", head.ID, "err", err)
				continue
			}
			ri.seen[head.ID] = true
		}
	}
}

// clip trims s, drops control characters and cuts it to max characters
// (the column's VARCHAR length).
func clip(s string, max int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s))
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// reportDomain normalises a domain a report names: lower case, no trailing
// dot, at most 253 characters.
func reportDomain(s string) string {
	return clip(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "."), 253)
}

// reportIP returns s as a canonical IP address, or "" when it is not one.
func reportIP(s string) string {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return ""
	}
	return ip.String()
}

// reportAddress drops the angle brackets ARF keeps around an address
// ("<a@example.com>"), so the abuse count's "%@domain" match works.
func reportAddress(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">")
	return clip(s, 320)
}

// clampCount fits a report's count into an INT UNSIGNED column.
func clampCount(n uint64) uint {
	const maxU32 = 1<<32 - 1
	if n > maxU32 {
		return maxU32
	}
	return uint(n)
}

// defaultStr returns dflt when s is blank.
func defaultStr(s, dflt string) string {
	if strings.TrimSpace(s) == "" {
		return dflt
	}
	return s
}

// listDomains names up to five domains for a notification body, sorted,
// and returns how many there are.
func listDomains(domains map[string]bool) (list string, n int) {
	names := make([]string, 0, len(domains))
	for d := range domains {
		names = append(names, d)
	}
	sort.Strings(names)
	shown := names
	if len(shown) > 5 {
		shown = shown[:5]
	}
	list = strings.Join(shown, ", ")
	if extra := len(names) - len(shown); extra > 0 {
		list += " and " + strconv.Itoa(extra) + " more"
	}
	return list, len(names)
}

// deliverabilityLink is where every report notification points: the
// deliverability page is the one admin view of these reports.
const deliverabilityLink = "/jabali-admin/mail/deliverability"
