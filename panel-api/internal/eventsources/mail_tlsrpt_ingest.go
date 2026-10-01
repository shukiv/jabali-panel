// M47 Wave 8 ingest source — SMTP TLS reports (RFC 8460). Receivers report
// back when STARTTLS failed or certificate validation broke: the signal that
// MTA-STS or DANE stopped working for one of the panel's domains.
//
// Each policy block becomes a success row plus one row per failure result
// type in tlsrpt_aggregate; the deliverability score sums the failures. A
// policy is identified by reporter + policy domain + date range. See
// mail_report_ingest.go for the listing and why a report's contents are
// treated as untrusted.
package eventsources

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/stalwartadmin"
)

func runMailTlsRptIngest(ctx context.Context, d Deps) {
	if d.StalwartAdmin == nil || d.TLSRPTAggregate == nil {
		d.Log.Debug("eventsources: mail_tlsrpt_ingest disabled (missing stalwart client or repo)")
		return
	}
	ri := newReportIngest("TlsExternalReport")
	ri.prune = d.TLSRPTAggregate.PruneOlderThan
	runReportIngest(ctx, func(ctx context.Context) { mailTlsRptIngestPass(ctx, d, ri) })
}

// tlsImported is one stored policy block, for the pass's notification.
type tlsImported struct {
	domain            string
	failed, succeeded uint64
}

func mailTlsRptIngestPass(ctx context.Context, d Deps, ri *reportIngest) {
	var imported []tlsImported
	ri.pass(ctx, d, func(ctx context.Context, raw json.RawMessage) error {
		got, err := mailTlsRptImportOne(ctx, d, raw)
		imported = append(imported, got...)
		return err
	})
	notifyTlsRptImported(ctx, d, imported)
}

// mailTlsRptImportOne stores one report's policy blocks that are not stored
// yet. It returns what it stored, and an error to retry the report later.
func mailTlsRptImportOne(ctx context.Context, d Deps, raw json.RawMessage) ([]tlsImported, error) {
	var rep stalwartadmin.TlsExternalReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		d.Log.Warn("tlsrpt-ingest: unreadable report, skipped", "err", err)
		return nil, nil
	}
	reporter := clip(defaultStr(rep.Report.OrganizationName, rep.From), 253)
	start, end := rep.Report.DateRangeStart.UTC(), rep.Report.DateRangeEnd.UTC()
	if reporter == "" || start.IsZero() || end.IsZero() {
		d.Log.Warn("tlsrpt-ingest: report without a reporter or date range, skipped", "id", rep.ID)
		return nil, nil
	}
	if reportTooOld(d, end) {
		d.Log.Info("tlsrpt-ingest: report older than retention, skipped", "id", rep.ID, "end", end)
		return nil, nil
	}
	var rows []models.TLSRPTAggregate
	var got []tlsImported
	for _, pol := range rep.Report.Policies {
		domain := reportDomain(pol.PolicyDomain)
		if domain == "" {
			continue
		}
		exists, err := d.TLSRPTAggregate.ExistsForReport(ctx, reporter, domain, start, end)
		if err != nil {
			return nil, err
		}
		if exists {
			continue
		}
		row := func(resultType string, succeeded, failed uint64) models.TLSRPTAggregate {
			return models.TLSRPTAggregate{
				Domain:       domain,
				Reporter:     reporter,
				WindowStart:  start,
				WindowEnd:    end,
				ResultType:   resultType,
				SuccessCount: clampCount(succeeded),
				FailureCount: clampCount(failed),
			}
		}
		if pol.TotalSuccessfulSessions > 0 {
			rows = append(rows, row("successful-tls", pol.TotalSuccessfulSessions, 0))
		}
		var detailed uint64
		for _, f := range pol.FailureDetails {
			rows = append(rows, row(tlsResultType(f.ResultType), 0, f.FailedSessionCount))
			detailed += f.FailedSessionCount
		}
		// failure-details is optional in RFC 8460: failures the report
		// counts but does not detail still count against the domain.
		if pol.TotalFailedSessions > detailed {
			rows = append(rows, row("unspecified", 0, pol.TotalFailedSessions-detailed))
		}
		got = append(got, tlsImported{domain: domain, failed: pol.TotalFailedSessions, succeeded: pol.TotalSuccessfulSessions})
	}
	if len(rows) == 0 {
		return nil, nil
	}
	n, err := d.TLSRPTAggregate.InsertMany(ctx, rows)
	if err != nil {
		return nil, err
	}
	d.Log.Info("tlsrpt-ingest: imported", "reporter", reporter, "policies", len(got), "rows", n)
	return got, nil
}

// tlsResultTypes maps Stalwart's TlsResultType to RFC 8460's result types.
var tlsResultTypes = map[string]string{
	"startTlsNotSupported":    "starttls-not-supported",
	"certificateHostMismatch": "certificate-host-mismatch",
	"certificateExpired":      "certificate-expired",
	"certificateNotTrusted":   "certificate-not-trusted",
	"validationFailure":       "validation-failure",
	"tlsaInvalid":             "tlsa-invalid",
	"dnssecInvalid":           "dnssec-invalid",
	"daneRequired":            "dane-required",
	"stsPolicyFetchError":     "sts-policy-fetch-error",
	"stsPolicyInvalid":        "sts-policy-invalid",
	"stsWebpkiInvalid":        "sts-webpki-invalid",
	"other":                   "other",
}

// tlsResultType returns the RFC 8460 name for a Stalwart result type. A
// value Stalwart adds later is spelled in the same kebab case, limited to
// lower-case letters, digits and hyphens and to the column's 48 characters.
func tlsResultType(s string) string {
	if rfc, ok := tlsResultTypes[s]; ok {
		return rfc
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			if b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
		}
	}
	out := clip(b.String(), 48)
	if out == "" {
		return "other"
	}
	return out
}

// notifyTlsRptImported sends one notification when the policies a pass
// stored report failed sessions.
func notifyTlsRptImported(ctx context.Context, d Deps, imported []tlsImported) {
	if d.Queue == nil {
		return
	}
	domains := map[string]bool{}
	var failed, succeeded uint64
	for _, p := range imported {
		if p.failed == 0 {
			continue
		}
		domains[p.domain] = true
		failed += p.failed
		succeeded += p.succeeded
	}
	if len(domains) == 0 {
		return
	}
	list, n := listDomains(domains)
	title := "TLS-RPT received: " + list
	if n > 1 {
		title = fmt.Sprintf("TLS-RPT failures reported for %d domains", n)
	}
	_, _ = d.Queue.Publish(ctx, notifications.Envelope{
		EventKind: "mail.tls.report_received",
		Severity:  "warning",
		Title:     title,
		Body:      fmt.Sprintf("TLS reports for %s: %d sessions failed, %d succeeded.", list, failed, succeeded),
		Deeplink:  deliverabilityLink,
	})
}
