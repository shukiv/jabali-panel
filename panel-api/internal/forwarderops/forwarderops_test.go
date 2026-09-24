package forwarderops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeAgent struct {
	calls        int
	lastCmd      string
	lastParams   map[string]any // params of the last mailbox.sieve.apply
	err          error
	vacationResp json.RawMessage // returned for mailbox.vacation.get
	vacationHits int
}

func (a *fakeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.calls++
	if cmd == "mailbox.vacation.get" {
		a.vacationHits++
		return a.vacationResp, nil
	}
	a.lastCmd = cmd
	if m, ok := params.(map[string]any); ok {
		a.lastParams = m
	}
	return nil, a.err
}

// fakeFwdRepo overrides only ListByMailboxID; the embedded nil interface
// satisfies the rest of repository.EmailForwarderRepository (none are called).
type fakeFwdRepo struct {
	repository.EmailForwarderRepository
	rows []models.EmailForwarder
}

func (r *fakeFwdRepo) ListByMailboxID(_ context.Context, _ string, _ repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}

// fakeARRepo overrides FindByMailboxID + Update; a nil row means "no
// autoresponder". Update stores the row (the adoption path upserts here) and
// FindByMailboxID then returns it, so a Converge that adopts is observable.
type fakeARRepo struct {
	repository.EmailAutoresponderRepository
	row      *models.EmailAutoresponder
	upserted *models.EmailAutoresponder
}

func (r *fakeARRepo) FindByMailboxID(_ context.Context, _ string) (*models.EmailAutoresponder, error) {
	return r.row, nil
}

func (r *fakeARRepo) Update(_ context.Context, ar *models.EmailAutoresponder) error {
	r.upserted = ar
	r.row = ar
	return nil
}

func strp(s string) *string { return &s }

// derefStr reads a *string carried through the wire map (autoresponderPayload
// stores pointers), tolerating nil / wrong type.
func derefStr(v any) string {
	if p, ok := v.(*string); ok && p != nil {
		return *p
	}
	return ""
}

// A nil agent (or nil forwarders repo) must be a no-op so callers without an
// agent handle — e.g. the backup scheduler — can pass nil safely.
func TestConverge_NilAgentIsNoop(t *testing.T) {
	if err := Converge(context.Background(), nil, &fakeFwdRepo{}, &fakeARRepo{}, "mb-1", "m@d.com"); err != nil {
		t.Fatalf("nil agent must be a no-op, got %v", err)
	}
}

// GH #1795: Converge sends the mailbox's full server-side rule state in ONE
// mailbox.sieve.apply — enabled type=external forwarders as {target, keep_copy},
// and the autoresponder row as the composite's vacation payload. Disabled rows
// and type=alias rows (served by the SQL directory, not Sieve) are NOT sent.
func TestConverge_SendsCompositeAndSkipsAliasesAndDisabled(t *testing.T) {
	repo := &fakeFwdRepo{rows: []models.EmailForwarder{
		{Type: "alias", LocalPart: strp("sales"), Enabled: true}, // alias → not in sieve
		{Type: "external", Target: "keep@out.org", KeepCopy: true, Enabled: true},
		{Type: "external", Target: "off@out.org", Enabled: false}, // disabled → skipped
	}}
	from := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ar := &fakeARRepo{row: &models.EmailAutoresponder{
		Enabled: true, Subject: strp("Away"), TextBody: strp("Back Monday"), FromDate: &from,
	}}
	ag := &fakeAgent{}

	if err := Converge(context.Background(), ag, repo, ar, "mb-1", "notifications@example.com"); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if ag.calls != 1 || ag.lastCmd != "mailbox.sieve.apply" {
		t.Fatalf("expected one mailbox.sieve.apply call, got calls=%d cmd=%q", ag.calls, ag.lastCmd)
	}
	if got := ag.lastParams["mailbox_email"]; got != "notifications@example.com" {
		t.Errorf("mailbox_email = %v", got)
	}
	if _, hasAliases := ag.lastParams["aliases"]; hasAliases {
		t.Error("mailbox.sieve.apply must not carry aliases — they are served by the SQL directory")
	}
	externals, _ := ag.lastParams["externals"].([]map[string]any)
	if len(externals) != 1 {
		t.Fatalf("externals = %#v (want exactly one — alias + disabled dropped)", ag.lastParams["externals"])
	}
	if externals[0]["target"] != "keep@out.org" || externals[0]["keep_copy"] != true {
		t.Errorf("external = %#v (want target=keep@out.org keep_copy=true)", externals[0])
	}
	arPayload, ok := ag.lastParams["autoresponder"].(map[string]any)
	if !ok {
		t.Fatalf("autoresponder payload = %#v (want a map)", ag.lastParams["autoresponder"])
	}
	if arPayload["enabled"] != true || derefStr(arPayload["subject"]) != "Away" {
		t.Errorf("autoresponder = %#v", arPayload)
	}
	if derefStr(arPayload["from_date"]) != "2026-01-02T03:04:05Z" {
		t.Errorf("from_date = %v (want RFC3339 2026-01-02T03:04:05Z)", arPayload["from_date"])
	}
}

// With no autoresponder row, the composite carries a nil autoresponder (the
// forwards still apply, the vacation block is dropped).
func TestConverge_NoAutoresponderRow(t *testing.T) {
	repo := &fakeFwdRepo{rows: []models.EmailForwarder{
		{Type: "external", Target: "a@out.org", Enabled: true},
	}}
	ag := &fakeAgent{}
	if err := Converge(context.Background(), ag, repo, &fakeARRepo{row: nil}, "mb-1", "m@d.com"); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	// With no row the payload is a nil map, which marshals to JSON null — the
	// agent then decodes a nil *autoresponder and drops the vacation block.
	wire, err := json.Marshal(ag.lastParams)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if !strings.Contains(string(wire), `"autoresponder":null`) {
		t.Errorf("autoresponder must serialise to null when no row; wire = %s", wire)
	}
}

// GH #1795 follow-up: with no jabali autoresponder row, Converge adopts an
// externally-set (Bulwark webmail) native VacationResponse into
// email_autoresponders BEFORE applying, so it survives as part of the composite.
func TestConverge_AdoptsExternalVacationWhenNoRow(t *testing.T) {
	repo := &fakeFwdRepo{rows: []models.EmailForwarder{
		{Type: "external", Target: "a@out.org", Enabled: true},
	}}
	ar := &fakeARRepo{row: nil}
	ag := &fakeAgent{vacationResp: json.RawMessage(`{"is_enabled":true,"from_date":null,"to_date":null,"subject":"Away","text_body":"OOO","html_body":null}`)}

	if err := Converge(context.Background(), ag, repo, ar, "mb-1", "user@example.com"); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if ag.vacationHits != 1 {
		t.Fatalf("mailbox.vacation.get calls = %d, want 1", ag.vacationHits)
	}
	if ar.upserted == nil {
		t.Fatal("no autoresponder row was adopted")
	}
	if !ar.upserted.Enabled || ar.upserted.Subject == nil || *ar.upserted.Subject != "Away" {
		t.Errorf("adopted row = %#v (want enabled, subject=Away)", ar.upserted)
	}
	if ar.upserted.ManagedBy != "adopted" {
		t.Errorf("adopted row ManagedBy = %q, want adopted", ar.upserted.ManagedBy)
	}
	// The adopted vacation must be in the composite that gets applied.
	arPayload, ok := ag.lastParams["autoresponder"].(map[string]any)
	if !ok || arPayload["enabled"] != true || derefStr(arPayload["subject"]) != "Away" {
		t.Errorf("applied autoresponder = %#v (want enabled Away from adoption)", ag.lastParams["autoresponder"])
	}
}

// When jabali already owns an autoresponder row, the panel is the source of
// truth: Converge must NOT read or overwrite it from the native value.
func TestConverge_DoesNotAdoptWhenJabaliRowExists(t *testing.T) {
	repo := &fakeFwdRepo{rows: []models.EmailForwarder{{Type: "external", Target: "a@out.org", Enabled: true}}}
	existing := &models.EmailAutoresponder{MailboxID: "mb-1", Enabled: true, Subject: strp("Panel-set"), ManagedBy: "m6.5"}
	ar := &fakeARRepo{row: existing}
	ag := &fakeAgent{vacationResp: json.RawMessage(`{"is_enabled":true,"subject":"Webmail-set"}`)}

	if err := Converge(context.Background(), ag, repo, ar, "mb-1", "user@example.com"); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if ag.vacationHits != 0 {
		t.Errorf("mailbox.vacation.get was called %d times; must not read the native value when a jabali row exists", ag.vacationHits)
	}
	if ar.upserted != nil {
		t.Errorf("existing row was overwritten: %#v", ar.upserted)
	}
}

// A disabled native VacationResponse is nothing to adopt.
func TestConverge_DoesNotAdoptWhenNativeDisabled(t *testing.T) {
	repo := &fakeFwdRepo{rows: []models.EmailForwarder{{Type: "external", Target: "a@out.org", Enabled: true}}}
	ar := &fakeARRepo{row: nil}
	ag := &fakeAgent{vacationResp: json.RawMessage(`{"is_enabled":false}`)}

	if err := Converge(context.Background(), ag, repo, ar, "mb-1", "user@example.com"); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if ag.vacationHits != 1 {
		t.Fatalf("mailbox.vacation.get calls = %d, want 1", ag.vacationHits)
	}
	if ar.upserted != nil {
		t.Errorf("adopted a disabled vacation: %#v", ar.upserted)
	}
	// autoresponderPayload returns a nil map (marshals to JSON null) when there
	// is no row; a typed-nil map is a non-nil interface, so check its length.
	if m, _ := ag.lastParams["autoresponder"].(map[string]any); len(m) != 0 {
		t.Errorf("autoresponder = %#v (want empty — nothing adopted)", ag.lastParams["autoresponder"])
	}
}
