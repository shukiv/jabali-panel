// M47 Wave 4 ingest source — ARF (RFC 5965) feedback reports: a receiver
// (Gmail, Microsoft, Yahoo postmaster) telling the sender that a recipient
// marked one of its messages as spam. One arf_report row per report, keyed
// by the Stalwart id; the deliverability score counts them per sender
// domain.
//
// See mail_report_ingest.go for the listing and why a report's contents are
// treated as untrusted.
package eventsources

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/stalwartadmin"
)

func runMailAbuseIngest(ctx context.Context, d Deps) {
	if d.StalwartAdmin == nil || d.ARFReports == nil {
		d.Log.Debug("eventsources: mail_abuse_ingest disabled (missing stalwart client or repo)")
		return
	}
	ri := newReportIngest("ArfExternalReport")
	ri.prune = d.ARFReports.PruneOlderThan
	runReportIngest(ctx, func(ctx context.Context) { mailAbuseIngestPass(ctx, d, ri) })
}

func mailAbuseIngestPass(ctx context.Context, d Deps, ri *reportIngest) {
	imported := 0
	ri.pass(ctx, d, func(ctx context.Context, raw json.RawMessage) error {
		stored, err := mailAbuseImportOne(ctx, d, raw)
		if stored {
			imported++
		}
		return err
	})
	if d.Queue == nil || imported == 0 {
		return
	}
	_, _ = d.Queue.Publish(ctx, notifications.Envelope{
		EventKind: "mail.feedback.received",
		Severity:  "warning",
		Title:     "ARF feedback reports received",
		Body:      fmt.Sprintf("%d new abuse-feedback report(s) imported from upstream postmasters", imported),
		Deeplink:  deliverabilityLink,
	})
}

// mailAbuseImportOne stores one report. It reports whether it stored it,
// and returns an error to retry the report later.
func mailAbuseImportOne(ctx context.Context, d Deps, raw json.RawMessage) (bool, error) {
	var rep stalwartadmin.ArfExternalReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		d.Log.Warn("abuse-ingest: unreadable report, skipped", "err", err)
		return false, nil
	}
	received := rep.ReceivedAt.UTC()
	if received.IsZero() {
		received = d.Now().UTC()
	}
	if reportTooOld(d, received) {
		d.Log.Info("abuse-ingest: report older than retention, skipped", "id", rep.ID, "received", received)
		return false, nil
	}
	id := clip(rep.ID, 128)
	exists, err := d.ARFReports.ExistsForStalwartID(ctx, id)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	var arrival *time.Time
	if rep.Report.ArrivalDate != nil && !rep.Report.ArrivalDate.IsZero() {
		a := rep.Report.ArrivalDate.UTC()
		arrival = &a
	}
	row := models.ARFReport{
		StalwartID:       id,
		ReceivedAt:       received,
		FeedbackType:     arfFeedbackType(rep.Report.FeedbackType),
		Reporter:         clip(rep.From, 253),
		OriginalRcpt:     reportAddress(rep.Report.OriginalRcptTo),
		OriginalMailFrom: reportAddress(rep.Report.OriginalMailFrom),
		SourceIP:         reportIP(rep.Report.SourceIP),
		Incidents:        clampCount(max(rep.Report.Incidents, 1)),
		UserAgent:        clip(rep.Report.UserAgent, 255),
		ReportingMTA:     clip(rep.Report.ReportingMta, 253),
		ArrivalDate:      arrival,
	}
	n, err := d.ARFReports.InsertMany(ctx, []models.ARFReport{row})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// arfFeedbackTypes maps Stalwart's ArfFeedbackType to RFC 5965's
// Feedback-Type values.
var arfFeedbackTypes = map[string]string{
	"abuse":       "abuse",
	"authFailure": "auth-failure",
	"fraud":       "fraud",
	"notSpam":     "not-spam",
	"virus":       "virus",
	"other":       "other",
}

func arfFeedbackType(s string) string {
	if t, ok := arfFeedbackTypes[s]; ok {
		return t
	}
	return "other"
}
