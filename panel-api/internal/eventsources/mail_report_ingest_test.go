package eventsources

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// testdata/stalwart_reports.ndjson holds one DMARC, one TLS and one ARF
// report exactly as Stalwart returned them on the .60 test box (2026-09-29),
// after they were delivered to its report address by SMTP.
func loadReportFixtures(t *testing.T) map[string]map[string]any {
	t.Helper()
	f, err := os.Open("testdata/stalwart_reports.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	byKind := map[string]map[string]any{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var obj map[string]any
		if err := json.Unmarshal(sc.Bytes(), &obj); err != nil {
			t.Fatal(err)
		}
		report := obj["report"].(map[string]any)
		switch {
		case report["records"] != nil:
			byKind["DmarcExternalReport"] = obj
		case report["policies"] != nil:
			byKind["TlsExternalReport"] = obj
		case report["feedbackType"] != nil:
			byKind["ArfExternalReport"] = obj
		}
	}
	if len(byKind) != 3 {
		t.Fatalf("fixtures: found %d report kinds, want 3", len(byKind))
	}
	return byKind
}

// fixture returns a copy of one fixture with a new id, for a test to change.
func fixture(t *testing.T, kind, id string) map[string]any {
	t.Helper()
	b, _ := json.Marshal(loadReportFixtures(t)[kind])
	var obj map[string]any
	_ = json.Unmarshal(b, &obj)
	obj["id"] = id
	return obj
}

// fakeReports is Stalwart's report store as the ingest sees it.
type fakeReports struct {
	objs    map[string]map[string]json.RawMessage // type -> id -> object
	listErr error
	fetched []string
}

func newFakeReports() *fakeReports {
	return &fakeReports{objs: map[string]map[string]json.RawMessage{}}
}

func (f *fakeReports) add(t *testing.T, typeName string, obj map[string]any) {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	if f.objs[typeName] == nil {
		f.objs[typeName] = map[string]json.RawMessage{}
	}
	f.objs[typeName][obj["id"].(string)] = b
}

func (f *fakeReports) QueryIDs(_ context.Context, typeName string, _ map[string]any) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	ids := []string{}
	for id := range f.objs[typeName] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (f *fakeReports) GetMany(_ context.Context, typeName string, ids, _ []string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, id := range ids {
		if raw, ok := f.objs[typeName][id]; ok {
			out = append(out, raw)
			f.fetched = append(f.fetched, id)
		}
	}
	return out, nil
}

type fakeDmarcRepo struct {
	repository.DMARCAggregateRepository
	rows      []models.DMARCAggregate
	insertErr error
}

func (r *fakeDmarcRepo) InsertMany(_ context.Context, rows []models.DMARCAggregate) (int, error) {
	if r.insertErr != nil {
		return 0, r.insertErr
	}
	r.rows = append(r.rows, rows...)
	return len(rows), nil
}

func (r *fakeDmarcRepo) ExistsForReport(_ context.Context, reporter, domain string, start, end time.Time) (bool, error) {
	for _, row := range r.rows {
		if row.Reporter == reporter && row.Domain == domain && row.WindowStart.Equal(start) && row.WindowEnd.Equal(end) {
			return true, nil
		}
	}
	return false, nil
}

type fakeTlsRptRepo struct {
	repository.TLSRPTAggregateRepository
	rows []models.TLSRPTAggregate
}

func (r *fakeTlsRptRepo) InsertMany(_ context.Context, rows []models.TLSRPTAggregate) (int, error) {
	r.rows = append(r.rows, rows...)
	return len(rows), nil
}

func (r *fakeTlsRptRepo) ExistsForReport(_ context.Context, reporter, domain string, start, end time.Time) (bool, error) {
	for _, row := range r.rows {
		if row.Reporter == reporter && row.Domain == domain && row.WindowStart.Equal(start) && row.WindowEnd.Equal(end) {
			return true, nil
		}
	}
	return false, nil
}

type fakeArfRepo struct {
	repository.ARFReportRepository
	rows []models.ARFReport
}

func (r *fakeArfRepo) InsertMany(_ context.Context, rows []models.ARFReport) (int, error) {
	r.rows = append(r.rows, rows...)
	return len(rows), nil
}

func (r *fakeArfRepo) ExistsForStalwartID(_ context.Context, id string) (bool, error) {
	for _, row := range r.rows {
		if row.StalwartID == id {
			return true, nil
		}
	}
	return false, nil
}

func reportDeps(client *fakeReports, pub *capturingPublisher) Deps {
	return Deps{
		Queue:         pub,
		StalwartAdmin: client,
		Log:           slog.New(slog.DiscardHandler),
		Now:           func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) },
	}
}

// dmarcDeps sets the clock two days after the DMARC fixture's window
// (2025-09-28), which is past retention at reportDeps' clock.
func dmarcDeps(client *fakeReports, pub *capturingPublisher) Deps {
	d := reportDeps(client, pub)
	d.Now = func() time.Time { return time.Date(2025, 9, 30, 12, 0, 0, 0, time.UTC) }
	return d
}

func TestDmarcIngest_StoresTheRecordsOfARealStalwartReport(t *testing.T) {
	client, pub := newFakeReports(), &capturingPublisher{}
	client.add(t, "DmarcExternalReport", fixture(t, "DmarcExternalReport", "jg1tf79uacaa"))
	repo := &fakeDmarcRepo{}
	d := dmarcDeps(client, pub)
	d.DMARCAggregate = repo

	mailDmarcIngestPass(context.Background(), d, newReportIngest("DmarcExternalReport"))

	start := time.Date(2025, 9, 28, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 9, 28, 23, 59, 59, 0, time.UTC)
	want := []models.DMARCAggregate{
		{Domain: "mx.jabali-panel.com", Reporter: "reporter.example", WindowStart: start, WindowEnd: end, SourceIP: "203.0.113.9", Disposition: "none", DKIM: "pass", SPF: "fail", Cnt: 12},
		{Domain: "mx.jabali-panel.com", Reporter: "reporter.example", WindowStart: start, WindowEnd: end, SourceIP: "198.51.100.7", Disposition: "quarantine", DKIM: "fail", SPF: "fail", Cnt: 3},
	}
	if fmt.Sprint(repo.rows) != fmt.Sprint(want) {
		t.Fatalf("rows =\n%+v\nwant\n%+v", repo.rows, want)
	}
	if pub.Count() != 1 {
		t.Fatalf("%d notifications, want 1", pub.Count())
	}
	// 3 of 15 messages failed DKIM: over 10%, so the operator should look.
	if env := pub.Last(); env.Severity != "warning" || env.EventKind != "mail.dmarc.report_received" || env.Deeplink != "/jabali-admin/mail/deliverability" {
		t.Fatalf("notification = %+v", env)
	}
}

// A receiver sends one report per domain for the same day, and sends a
// report again after a delivery retry. Each domain's report is stored once.
func TestDmarcIngest_OneReportPerDomainForTheSameDay(t *testing.T) {
	client := newFakeReports()
	a := fixture(t, "DmarcExternalReport", "r1")
	b := fixture(t, "DmarcExternalReport", "r2")
	b["report"].(map[string]any)["policyDomain"] = "b.example"
	resent := fixture(t, "DmarcExternalReport", "r3") // r1 again, under a new Stalwart id
	for _, r := range []map[string]any{a, b, resent} {
		client.add(t, "DmarcExternalReport", r)
	}
	repo := &fakeDmarcRepo{}
	d := dmarcDeps(client, &capturingPublisher{})
	d.DMARCAggregate = repo

	mailDmarcIngestPass(context.Background(), d, newReportIngest("DmarcExternalReport"))

	perDomain := map[string]int{}
	for _, r := range repo.rows {
		perDomain[r.Domain]++
	}
	if len(repo.rows) != 4 || perDomain["mx.jabali-panel.com"] != 2 || perDomain["b.example"] != 2 {
		t.Fatalf("rows per domain = %v (%d rows), want 2 each", perDomain, len(repo.rows))
	}
}

// Stalwart can neither filter nor sort by receivedAt, so every pass lists
// every report. A report handled once is not fetched again; a report whose
// store failed is tried again; a report Stalwart expired is forgotten.
func TestReportIngest_FetchesEachReportOnceAndRetriesFailures(t *testing.T) {
	client, pub := newFakeReports(), &capturingPublisher{}
	client.add(t, "DmarcExternalReport", fixture(t, "DmarcExternalReport", "r1"))
	repo := &fakeDmarcRepo{insertErr: errors.New("db down")}
	d := dmarcDeps(client, pub)
	d.DMARCAggregate = repo
	ri := newReportIngest("DmarcExternalReport")

	mailDmarcIngestPass(context.Background(), d, ri)
	if len(repo.rows) != 0 || ri.seen["r1"] {
		t.Fatalf("failed store marked done: rows=%d seen=%v", len(repo.rows), ri.seen)
	}
	repo.insertErr = nil
	mailDmarcIngestPass(context.Background(), d, ri)
	if len(repo.rows) != 2 || !ri.seen["r1"] {
		t.Fatalf("retry: rows=%d seen=%v", len(repo.rows), ri.seen)
	}
	client.fetched = nil
	mailDmarcIngestPass(context.Background(), d, ri)
	if len(client.fetched) != 0 {
		t.Fatalf("handled report fetched again: %v", client.fetched)
	}
	delete(client.objs["DmarcExternalReport"], "r1")
	mailDmarcIngestPass(context.Background(), d, ri)
	if len(ri.seen) != 0 {
		t.Fatalf("expired report still remembered: %v", ri.seen)
	}
	if pub.Count() != 1 {
		t.Fatalf("%d notifications, want 1 (the pass that stored the report)", pub.Count())
	}
}

func TestReportIngest_ListFailureStoresNothing(t *testing.T) {
	client := newFakeReports()
	client.add(t, "DmarcExternalReport", fixture(t, "DmarcExternalReport", "r1"))
	client.listErr = errors.New("stalwartadmin: x:DmarcExternalReport/query: HTTP 503")
	repo := &fakeDmarcRepo{}
	d := dmarcDeps(client, &capturingPublisher{})
	d.DMARCAggregate = repo
	mailDmarcIngestPass(context.Background(), d, newReportIngest("DmarcExternalReport"))
	if len(repo.rows) != 0 || len(client.fetched) != 0 {
		t.Fatalf("rows=%d fetched=%v after a failed listing", len(repo.rows), client.fetched)
	}
}

// Anyone can email a report to the report address. A flood of reports is
// one notification per pass, not one per report.
func TestReportIngest_AFloodOfReportsIsOneNotification(t *testing.T) {
	client, pub := newFakeReports(), &capturingPublisher{}
	for i := range 50 {
		r := fixture(t, "DmarcExternalReport", fmt.Sprintf("r%02d", i))
		r["report"].(map[string]any)["policyDomain"] = fmt.Sprintf("d%02d.example", i)
		client.add(t, "DmarcExternalReport", r)
	}
	repo := &fakeDmarcRepo{}
	d := dmarcDeps(client, pub)
	d.DMARCAggregate = repo
	mailDmarcIngestPass(context.Background(), d, newReportIngest("DmarcExternalReport"))
	if len(repo.rows) != 100 {
		t.Fatalf("%d rows, want 100", len(repo.rows))
	}
	if pub.Count() != 1 {
		t.Fatalf("%d notifications for one pass, want 1", pub.Count())
	}
	if env := pub.Last(); !strings.Contains(env.Title, "50 domains") || !strings.Contains(env.Body, "and 45 more") {
		t.Fatalf("notification = %+v", env)
	}
}

// A report's strings come from its sender: they must fit their columns and
// carry no control characters, and a source IP must be an IP.
func TestDmarcIngest_UntrustedFieldsFitTheirColumns(t *testing.T) {
	client := newFakeReports()
	r := fixture(t, "DmarcExternalReport", "r1")
	rep := r["report"].(map[string]any)
	rep["orgName"] = "evil\r\nX-Injected: 1 " + strings.Repeat("o", 400)
	rep["policyDomain"] = "Example.COM."
	rec := rep["records"].(map[string]any)["0"].(map[string]any)
	rec["sourceIp"] = "not-an-ip"
	rec["count"] = float64(1 << 40)
	rec["evaluatedDkim"] = "unspecified"
	client.add(t, "DmarcExternalReport", r)
	repo := &fakeDmarcRepo{}
	d := dmarcDeps(client, &capturingPublisher{})
	d.DMARCAggregate = repo
	mailDmarcIngestPass(context.Background(), d, newReportIngest("DmarcExternalReport"))

	if len(repo.rows) != 2 {
		t.Fatalf("%d rows, want 2", len(repo.rows))
	}
	row := repo.rows[0]
	if n := len([]rune(row.Reporter)); n > 253 || strings.ContainsAny(row.Reporter, "\r\n") {
		t.Errorf("reporter %q (%d chars)", row.Reporter, n)
	}
	if row.Domain != "example.com" || row.SourceIP != "" || row.Cnt != 1<<32-1 || row.DKIM != "fail" {
		t.Errorf("row = %+v", row)
	}
}

func TestTlsRptIngest_StoresARealStalwartReport(t *testing.T) {
	client, pub := newFakeReports(), &capturingPublisher{}
	client.add(t, "TlsExternalReport", fixture(t, "TlsExternalReport", "jg1tf10sadaa"))
	repo := &fakeTlsRptRepo{}
	d := reportDeps(client, pub)
	d.TLSRPTAggregate = repo

	mailTlsRptIngestPass(context.Background(), d, newReportIngest("TlsExternalReport"))

	got := []string{}
	for _, r := range repo.rows {
		got = append(got, fmt.Sprintf("%s %s %s ok=%d fail=%d", r.Domain, r.Reporter, r.ResultType, r.SuccessCount, r.FailureCount))
	}
	want := []string{
		"mx.jabali-panel.com Reporter TLS successful-tls ok=40 fail=0",
		"mx.jabali-panel.com Reporter TLS certificate-expired ok=0 fail=2",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !repo.rows[0].WindowStart.Equal(time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("window start = %v", repo.rows[0].WindowStart)
	}
	if pub.Count() != 1 || pub.Last().Severity != "warning" || pub.Last().Deeplink != "/jabali-admin/mail/deliverability" {
		t.Fatalf("notifications = %d, last %+v", pub.Count(), pub.Last())
	}
}

// Every policy block of a report is stored, and failures a report counts
// without detailing still count.
func TestTlsRptIngest_EveryPolicyAndUndetailedFailures(t *testing.T) {
	client := newFakeReports()
	r := fixture(t, "TlsExternalReport", "t1")
	policies := r["report"].(map[string]any)["policies"].(map[string]any)
	policies["1"] = map[string]any{
		"policyDomain": "b.example", "policyType": "noPolicyFound",
		"totalSuccessfulSessions": 7, "totalFailedSessions": 5, "failureDetails": map[string]any{},
	}
	client.add(t, "TlsExternalReport", r)
	repo := &fakeTlsRptRepo{}
	d := reportDeps(client, &capturingPublisher{})
	d.TLSRPTAggregate = repo

	mailTlsRptIngestPass(context.Background(), d, newReportIngest("TlsExternalReport"))

	failed := map[string]uint{}
	for _, row := range repo.rows {
		failed[row.Domain] += row.FailureCount
	}
	if failed["mx.jabali-panel.com"] != 2 || failed["b.example"] != 5 {
		t.Fatalf("failures per domain = %v, want mx 2 and b 5", failed)
	}
}

func TestTlsResultType(t *testing.T) {
	cases := map[string]string{
		"startTlsNotSupported": "starttls-not-supported",
		"certificateExpired":   "certificate-expired",
		"stsWebpkiInvalid":     "sts-webpki-invalid",
		"other":                "other",
		"someNewType":          "some-new-type",
		"'; DROP TABLE x; --":  "d-r-o-p-t-a-b-l-ex--",
		"":                     "other",
	}
	for in, want := range cases {
		if got := tlsResultType(in); got != want {
			t.Errorf("tlsResultType(%q) = %q, want %q", in, got, want)
		}
	}
	if got := tlsResultType(strings.Repeat("a", 100)); len(got) != 48 {
		t.Errorf("long type kept %d chars, want 48", len(got))
	}
}

func TestAbuseIngest_StoresARealStalwartReport(t *testing.T) {
	client, pub := newFakeReports(), &capturingPublisher{}
	client.add(t, "ArfExternalReport", fixture(t, "ArfExternalReport", "jg1tf3lmaeaa"))
	repo := &fakeArfRepo{}
	d := reportDeps(client, pub)
	d.ARFReports = repo

	mailAbuseIngestPass(context.Background(), d, newReportIngest("ArfExternalReport"))
	// A restart forgets what was handled; the Stalwart id keeps the report
	// from being stored twice.
	mailAbuseIngestPass(context.Background(), d, newReportIngest("ArfExternalReport"))

	if len(repo.rows) != 1 {
		t.Fatalf("%d rows, want 1", len(repo.rows))
	}
	row := repo.rows[0]
	// No angle brackets: the deliverability score matches
	// original_mail_from LIKE '%@<domain>'.
	if row.OriginalMailFrom != "newsletter@mx.jabali-panel.com" || row.OriginalRcpt != "victim@reporter.example" {
		t.Errorf("addresses = %q, %q", row.OriginalMailFrom, row.OriginalRcpt)
	}
	if row.StalwartID != "jg1tf3lmaeaa" || row.FeedbackType != "abuse" || row.Reporter != "abuse@reporter.example" ||
		row.SourceIP != "203.0.113.9" || row.Incidents != 1 || row.UserAgent != "ProbeFBL/1.0" || row.ReportingMTA != "mx.reporter.example" {
		t.Errorf("row = %+v", row)
	}
	if !row.ReceivedAt.Equal(time.Date(2026, 9, 28, 22, 10, 24, 0, time.UTC)) || row.ArrivalDate == nil || !row.ArrivalDate.Equal(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("dates = %v, %v", row.ReceivedAt, row.ArrivalDate)
	}
	if pub.Count() != 1 {
		t.Fatalf("%d notifications, want 1", pub.Count())
	}
}

func TestArfFeedbackType(t *testing.T) {
	cases := map[string]string{"abuse": "abuse", "authFailure": "auth-failure", "notSpam": "not-spam", "virus": "virus", "bogus": "other", "": "other"}
	for in, want := range cases {
		if got := arfFeedbackType(in); got != want {
			t.Errorf("arfFeedbackType(%q) = %q, want %q", in, got, want)
		}
	}
}

// A report whose window ended before the retention cutoff is not stored: the
// daily prune would delete it and a restart would import and announce it again.
func TestDmarcIngest_SkipsAReportPastRetention(t *testing.T) {
	client, pub := newFakeReports(), &capturingPublisher{}
	client.add(t, "DmarcExternalReport", fixture(t, "DmarcExternalReport", "r1")) // window 2025-09-28
	repo := &fakeDmarcRepo{}
	d := reportDeps(client, pub) // 2026-09-29: a year later
	d.DMARCAggregate = repo
	ri := newReportIngest("DmarcExternalReport")

	mailDmarcIngestPass(context.Background(), d, ri)

	if len(repo.rows) != 0 || pub.Count() != 0 {
		t.Fatalf("stale report stored: rows=%d notifications=%d", len(repo.rows), pub.Count())
	}
	if !ri.seen["r1"] {
		t.Fatal("a skipped report must count as handled, not be fetched every pass")
	}
}

// The stored rows are kept 90 days (ADR-0103): each source prunes its table
// at most once a day.
func TestReportIngest_PrunesOnceADay(t *testing.T) {
	client := newFakeReports()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	d := reportDeps(client, &capturingPublisher{})
	d.Now = func() time.Time { return now }
	d.DMARCAggregate = &fakeDmarcRepo{}
	var cutoffs []time.Time
	ri := newReportIngest("DmarcExternalReport")
	ri.prune = func(_ context.Context, cutoff time.Time) (int64, error) {
		cutoffs = append(cutoffs, cutoff)
		return 3, nil
	}

	mailDmarcIngestPass(context.Background(), d, ri)
	now = now.Add(5 * time.Minute)
	mailDmarcIngestPass(context.Background(), d, ri)
	now = now.Add(24 * time.Hour)
	mailDmarcIngestPass(context.Background(), d, ri)

	want := []time.Time{
		time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 12, 5, 0, 0, time.UTC),
	}
	if len(cutoffs) != len(want) || !cutoffs[0].Equal(want[0]) || !cutoffs[1].Equal(want[1]) {
		t.Fatalf("prune cutoffs = %v, want %v", cutoffs, want)
	}
}

func TestReportIngest_FailedPruneIsTriedAgain(t *testing.T) {
	d := reportDeps(newFakeReports(), &capturingPublisher{})
	d.DMARCAggregate = &fakeDmarcRepo{}
	calls := 0
	ri := newReportIngest("DmarcExternalReport")
	ri.prune = func(context.Context, time.Time) (int64, error) {
		calls++
		if calls == 1 {
			return 0, errors.New("db down")
		}
		return 0, nil
	}
	mailDmarcIngestPass(context.Background(), d, ri)
	mailDmarcIngestPass(context.Background(), d, ri)
	mailDmarcIngestPass(context.Background(), d, ri)
	if calls != 2 {
		t.Fatalf("prune ran %d times, want 2 (the failure, then one success)", calls)
	}
}
