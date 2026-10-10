package backupmetadata

import (
	"context"
	"slices"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/trustedsenders"
)

// GH #2017: a backup carries each mailbox's trusted senders, and a restore
// brings them back. The reconciler then writes them into Stalwart.

type tsBackupRows struct {
	repository.MailboxTrustedSenderRepository
	rows    []models.MailboxTrustedSender
	extra   int64 // added to CountByMailbox
	batched int
}

func (f *tsBackupRows) ListByMailboxIDs(_ context.Context, ids []string) ([]models.MailboxTrustedSender, error) {
	f.batched++
	var out []models.MailboxTrustedSender
	for _, r := range f.rows {
		if slices.Contains(ids, r.MailboxID) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *tsBackupRows) ListByMailbox(context.Context, string) ([]models.MailboxTrustedSender, error) {
	panic("Build must batch trusted senders via ListByMailboxIDs")
}

func (f *tsBackupRows) CountByMailbox(_ context.Context, mbID string) (int64, error) {
	n := f.extra
	for _, r := range f.rows {
		if r.MailboxID == mbID {
			n++
		}
	}
	return n, nil
}

func (f *tsBackupRows) Create(_ context.Context, row *models.MailboxTrustedSender) error {
	for _, r := range f.rows {
		if r.MailboxID == row.MailboxID && r.Address == row.Address {
			return repository.ErrConflict
		}
	}
	f.rows = append(f.rows, *row)
	return nil
}

func (f *tsBackupRows) addresses(mbID string) []string {
	var out []string
	for _, r := range f.rows {
		if r.MailboxID == mbID {
			out = append(out, r.Address)
		}
	}
	slices.Sort(out)
	return out
}

func TestBuild_CarriesTrustedSenders(t *testing.T) {
	c := &counts{}
	rows := &tsBackupRows{rows: []models.MailboxTrustedSender{
		{MailboxID: "m1", Address: "zed@x.com"},
		{MailboxID: "m1", Address: "amy@y.com"},
		{MailboxID: "other", Address: "nope@x.com"},
	}}
	m := Build(context.Background(), &models.User{ID: "u1"}, Deps{
		Domains: &fDomains{rows: []models.Domain{{ID: "d1", Name: "shop.org"}}},
		Mailboxes: &fMailboxes{t: t, c: c, rows: []models.Mailbox{
			{ID: "m1", DomainID: "d1", LocalPart: "info", EmailCached: "info@shop.org"},
			{ID: "m2", DomainID: "d1", LocalPart: "sales", EmailCached: "sales@shop.org"},
		}},
		TrustedSenders: rows,
	})
	if len(m.Domains) != 1 || len(m.Domains[0].Mailboxes) != 2 {
		t.Fatalf("domains = %+v", m.Domains)
	}
	mbs := m.Domains[0].Mailboxes
	if got := mbs[0].TrustedSenders; !slices.Equal(got, []string{"amy@y.com", "zed@x.com"}) {
		t.Fatalf("info@ trusted senders = %v", got)
	}
	if mbs[1].TrustedSenders != nil {
		t.Fatalf("sales@ trusted senders = %v, want none", mbs[1].TrustedSenders)
	}
	if rows.batched != 1 {
		t.Fatalf("ListByMailboxIDs ran %d times, want one batch", rows.batched)
	}

	// No repository wired: no section.
	m = Build(context.Background(), &models.User{ID: "u1"}, Deps{
		Domains:   &fDomains{rows: []models.Domain{{ID: "d1", Name: "shop.org"}}},
		Mailboxes: &fMailboxes{t: t, c: c, rows: []models.Mailbox{{ID: "m1", DomainID: "d1", LocalPart: "info"}}},
	})
	if m.Domains[0].Mailboxes[0].TrustedSenders != nil {
		t.Fatal("trusted senders without a repository")
	}
}

func tsMeta(senders ...string) *internalbackup.AccountMetadata {
	meta := dcMeta()
	meta.Domains = meta.Domains[:1]
	meta.Domains[0].Mailboxes[0].TrustedSenders = senders
	return meta
}

// The rows come back, each address checked as the API checks it: the
// backup may be an uploaded file.
func TestApply_RestoresTrustedSenders(t *testing.T) {
	_, _, _, deps := dcDeps()
	deps.CheckDomain = allowDomains
	rows := &tsBackupRows{}
	deps.TrustedSenders = rows

	r := Apply(context.Background(), tsMeta("bob@x.com", "amy@y.com", "not an address", "o'brien@x.com"), deps)

	if got := rows.addresses("mb-good"); !slices.Equal(got, []string{"amy@y.com", "bob@x.com"}) {
		t.Fatalf("restored %v: %v", got, r.Errors)
	}
	if !hasError(r.Errors, `trusted sender "not an address"`) || !hasError(r.Errors, `trusted sender "o'brien@x.com"`) {
		t.Fatalf("invalid senders not reported: %v", r.Errors)
	}
}

// An address stored by an older panel in another form is stored in the
// canonical one; one the mailbox already trusts is skipped, not an error.
func TestApply_TrustedSendersCanonicalAndSkipsDuplicates(t *testing.T) {
	_, _, _, deps := dcDeps()
	deps.CheckDomain = allowDomains
	rows := &tsBackupRows{rows: []models.MailboxTrustedSender{{MailboxID: "mb-good", Address: "bob@x.com"}}}
	deps.TrustedSenders = rows

	r := Apply(context.Background(), tsMeta("BOB@X.com", "Amy@Y.com"), deps)

	if got := rows.addresses("mb-good"); !slices.Equal(got, []string{"amy@y.com", "bob@x.com"}) {
		t.Fatalf("rows %v", got)
	}
	if hasError(r.Errors, "trusted sender") {
		t.Fatalf("a duplicate is an error: %v", r.Errors)
	}
}

// The restore keeps the mailbox's cap.
func TestApply_TrustedSendersKeepTheCap(t *testing.T) {
	_, _, _, deps := dcDeps()
	deps.CheckDomain = allowDomains
	rows := &tsBackupRows{extra: trustedsenders.MaxPerMailbox - 1}
	deps.TrustedSenders = rows

	r := Apply(context.Background(), tsMeta("a@x.com", "b@x.com", "c@x.com"), deps)

	if got := rows.addresses("mb-good"); len(got) != 1 {
		t.Fatalf("restored %v past the cap", got)
	}
	if !hasError(r.Errors, "at most 500") {
		t.Fatalf("cap not reported: %v", r.Errors)
	}
}

func TestApply_TrustedSendersWithoutARepository(t *testing.T) {
	_, mb, _, deps := dcDeps()
	deps.CheckDomain = allowDomains
	r := Apply(context.Background(), tsMeta("bob@x.com"), deps) // must not panic
	if mb.created != 1 {
		t.Fatalf("mailbox not restored: %v", r.Errors)
	}
}
