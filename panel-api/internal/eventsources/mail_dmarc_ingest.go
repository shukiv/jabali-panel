// M47 Wave 6 ingest source — DMARC aggregate (RUA) reports Stalwart has
// parsed into DmarcExternalReport objects, stored in dmarc_aggregate (one
// row per record). The deliverability score counts the DKIM-failing rows.
//
// A report is identified by reporter + policy domain + date range: a big
// receiver sends one report per domain for the same day, and re-sends a
// report after a delivery retry. See mail_report_ingest.go for the listing
// and why a report's contents are treated as untrusted.
package eventsources

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/stalwartadmin"
)

func runMailDmarcIngest(ctx context.Context, d Deps) {
	if d.StalwartAdmin == nil || d.DMARCAggregate == nil {
		d.Log.Debug("eventsources: mail_dmarc_ingest disabled (missing stalwart client or repo)")
		return
	}
	ri := newReportIngest("DmarcExternalReport")
	runReportIngest(ctx, func(ctx context.Context) { mailDmarcIngestPass(ctx, d, ri) })
}

// dmarcImported is what one stored report adds to the pass's notification.
type dmarcImported struct {
	domain        string
	failed, total uint64
}

func mailDmarcIngestPass(ctx context.Context, d Deps, ri *reportIngest) {
	var imported []dmarcImported
	ri.pass(ctx, d, func(ctx context.Context, raw json.RawMessage) error {
		got, err := mailDmarcImportOne(ctx, d, raw)
		if got != nil {
			imported = append(imported, *got)
		}
		return err
	})
	notifyDmarcImported(ctx, d, imported)
}

// mailDmarcImportOne stores one report. It returns what it stored, nil for a
// duplicate or unusable report, and an error to retry the report later.
func mailDmarcImportOne(ctx context.Context, d Deps, raw json.RawMessage) (*dmarcImported, error) {
	var rep stalwartadmin.DmarcExternalReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		d.Log.Warn("dmarc-ingest: unreadable report, skipped", "err", err)
		return nil, nil
	}
	domain := reportDomain(rep.Report.PolicyDomain)
	reporter := clip(defaultStr(rep.Report.OrgName, rep.From), 253)
	start, end := rep.Report.DateRangeBegin.UTC(), rep.Report.DateRangeEnd.UTC()
	if domain == "" || reporter == "" || start.IsZero() || end.IsZero() {
		d.Log.Warn("dmarc-ingest: report without a domain, reporter or date range, skipped", "id", rep.ID)
		return nil, nil
	}
	exists, err := d.DMARCAggregate.ExistsForReport(ctx, reporter, domain, start, end)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, nil
	}
	got := dmarcImported{domain: domain}
	rows := make([]models.DMARCAggregate, 0, len(rep.Report.Records))
	for _, rec := range rep.Report.Records {
		dkim := dmarcResult(rec.EvaluatedDkim)
		rows = append(rows, models.DMARCAggregate{
			Domain:      domain,
			Reporter:    reporter,
			WindowStart: start,
			WindowEnd:   end,
			SourceIP:    reportIP(rec.SourceIP),
			Disposition: dmarcDisposition(rec.EvaluatedDisposition),
			DKIM:        dkim,
			SPF:         dmarcResult(rec.EvaluatedSpf),
			Cnt:         clampCount(rec.Count),
		})
		got.total += rec.Count
		if dkim != "pass" {
			got.failed += rec.Count
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	n, err := d.DMARCAggregate.InsertMany(ctx, rows)
	if err != nil {
		return nil, err
	}
	d.Log.Info("dmarc-ingest: imported", "reporter", reporter, "domain", domain, "rows", n)
	return &got, nil
}

// dmarcResult maps Stalwart's DmarcResult (pass|fail|unspecified) to the
// dkim/spf column (VARCHAR(8), pass|fail). Anything but pass counts as a
// failure.
func dmarcResult(s string) string {
	if s == "pass" {
		return "pass"
	}
	return "fail"
}

// dmarcDisposition maps Stalwart's DmarcActionDisposition
// (none|pass|quarantine|reject|unspecified) to RFC 7489's
// none|quarantine|reject.
func dmarcDisposition(s string) string {
	switch s {
	case "quarantine", "reject":
		return s
	}
	return "none"
}

// notifyDmarcImported sends one notification for the reports a pass stored.
func notifyDmarcImported(ctx context.Context, d Deps, imported []dmarcImported) {
	if d.Queue == nil || len(imported) == 0 {
		return
	}
	domains := map[string]bool{}
	var failed, total uint64
	severity := "info"
	for _, r := range imported {
		domains[r.domain] = true
		failed += r.failed
		total += r.total
		// >10% of a report's messages DKIM-failing: the operator should look.
		if r.failed*10 > r.total {
			severity = "warning"
		}
	}
	list, n := listDomains(domains)
	title := "DMARC report received: " + list
	if n > 1 {
		title = fmt.Sprintf("DMARC reports received for %d domains", n)
	}
	_, _ = d.Queue.Publish(ctx, notifications.Envelope{
		EventKind: "mail.dmarc.report_received",
		Severity:  severity,
		Title:     title,
		Body:      fmt.Sprintf("%d DMARC report(s) for %s: %d of %d messages failed DKIM.", len(imported), list, failed, total),
		Deeplink:  deliverabilityLink,
	})
}
