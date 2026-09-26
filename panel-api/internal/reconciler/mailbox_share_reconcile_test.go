package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type msStore struct {
	mailboxes map[string]models.Mailbox
	domains   map[string]models.Domain
	// listed is what ListAll returns (the tick's batch read); owned is what
	// FindByOwnerID returns (the apply-time re-read). They differ only when a
	// test simulates a delete landing between the two.
	listed []models.MailboxShare
	owned  map[string][]models.MailboxShare
}

type msShares struct {
	repository.MailboxShareRepository
	s *msStore
}

func (f msShares) ListAll(_ context.Context, opts repository.ListOptions) ([]models.MailboxShare, int64, error) {
	if opts.Offset >= len(f.s.listed) {
		return nil, int64(len(f.s.listed)), nil
	}
	return f.s.listed[opts.Offset:], int64(len(f.s.listed)), nil
}

func (f msShares) FindByOwnerID(_ context.Context, owner string, _ repository.ListOptions) ([]models.MailboxShare, int64, error) {
	rows := f.s.owned[owner]
	return rows, int64(len(rows)), nil
}

type msMailboxes struct {
	repository.MailboxRepository
	s *msStore
}

func (f msMailboxes) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	mb, ok := f.s.mailboxes[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &mb, nil
}

func (f msMailboxes) FindByIDs(_ context.Context, ids []string) ([]models.Mailbox, error) {
	var out []models.Mailbox
	seen := map[string]bool{}
	for _, id := range ids {
		if mb, ok := f.s.mailboxes[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, mb)
		}
	}
	return out, nil
}

type msDomains struct {
	repository.DomainRepository
	s *msStore
}

func (f msDomains) FindByIDs(_ context.Context, ids []string) ([]models.Domain, error) {
	var out []models.Domain
	for _, id := range ids {
		if d, ok := f.s.domains[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func newMSStore() *msStore {
	s := &msStore{
		mailboxes: map[string]models.Mailbox{},
		domains: map[string]models.Domain{
			"d1":   {ID: "d1", Name: "one.test", UserID: "u1", EmailEnabled: true},
			"doff": {ID: "doff", Name: "off.test", UserID: "u1", EmailEnabled: false},
		},
		owned: map[string][]models.MailboxShare{},
	}
	for _, mb := range []models.Mailbox{
		{ID: "alice", DomainID: "d1", EmailCached: "alice@one.test"},
		{ID: "bob", DomainID: "d1", EmailCached: "bob@one.test"},
		{ID: "carol", DomainID: "d1", EmailCached: "carol@one.test"},
		{ID: "dave", DomainID: "doff", EmailCached: "dave@off.test"},
	} {
		s.mailboxes[mb.ID] = mb
	}
	return s
}

func (s *msStore) share(id, owner, target string) {
	row := models.MailboxShare{ID: id, OwnerMailboxID: owner, SharedWithMailboxID: target, Rights: models.Rights{MayRead: true}}
	s.listed = append(s.listed, row)
	s.owned[owner] = append(s.owned[owner], row)
}

func newShareReconciler(s *msStore, ag *fakeAgent, mailEnabled bool) *Reconciler {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	r := New(msDomains{s: s}, nil, ag, log, Config{Interval: time.Second}).
		WithMailboxShares(msShares{s: s}, msMailboxes{s: s})
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{MailEnabled: mailEnabled}}
	return r
}

// sharePushes returns every mailbox.share_set payload, sorted by owner.
func sharePushes(ag *fakeAgent) []map[string]any {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var out []map[string]any
	for _, c := range ag.calls {
		if c.method != "mailbox.share_set" {
			continue
		}
		b, _ := json.Marshal(c.params)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["owner_email"].(string) < out[j]["owner_email"].(string) })
	return out
}

func pushedTargets(p map[string]any) []string {
	shares, _ := p["shares"].(map[string]any)
	var out []string
	for k := range shares {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Shares saved before this change were never pushed: nothing called
// mailbox.share_set. The sweep must push every owner's list.
func TestReconcileMailboxShares_PushesExistingShares(t *testing.T) {
	s := newMSStore()
	s.share("s1", "alice", "bob")
	s.share("s2", "alice", "carol")
	s.share("s3", "bob", "carol")
	ag := &fakeAgent{}

	newShareReconciler(s, ag, true).reconcileMailboxShares(context.Background())

	got := sharePushes(ag)
	if len(got) != 2 {
		t.Fatalf("pushes = %v, want one per owner (alice, bob)", got)
	}
	if got[0]["owner_email"] != "alice@one.test" || len(pushedTargets(got[0])) != 2 {
		t.Errorf("alice push = %v, want bob and carol", got[0])
	}
	if got[1]["owner_email"] != "bob@one.test" || pushedTargets(got[1])[0] != "carol@one.test" {
		t.Errorf("bob push = %v, want carol", got[1])
	}
}

func TestReconcileMailboxShares_SteadyStateSendsNothing(t *testing.T) {
	s := newMSStore()
	s.share("s1", "alice", "bob")
	ag := &fakeAgent{}
	r := newShareReconciler(s, ag, true)

	r.reconcileMailboxShares(context.Background())
	r.reconcileMailboxShares(context.Background())

	if n := len(sharePushes(ag)); n != 1 {
		t.Fatalf("pushes = %d, want 1 (the unchanged second tick is skipped)", n)
	}
}

// The tick read alice→bob, but the share was deleted before the apply ran.
// The apply re-reads, so the revoked share is not pushed back.
func TestReconcileMailboxShares_ApplyReReadsTheRows(t *testing.T) {
	s := newMSStore()
	s.share("s1", "alice", "bob")
	s.owned["alice"] = nil // deleted after the batch read
	ag := &fakeAgent{}

	newShareReconciler(s, ag, true).reconcileMailboxShares(context.Background())

	got := sharePushes(ag)
	if len(got) != 1 {
		t.Fatalf("pushes = %v, want 1", got)
	}
	if targets := pushedTargets(got[0]); len(targets) != 0 {
		t.Errorf("pushed %v, want an empty list (the share is gone)", targets)
	}
}

func TestReconcileMailboxShares_SkipsMailOff(t *testing.T) {
	s := newMSStore()
	s.share("s1", "dave", "alice") // dave's domain has mail disabled
	ag := &fakeAgent{}
	newShareReconciler(s, ag, true).reconcileMailboxShares(context.Background())
	if n := len(sharePushes(ag)); n != 0 {
		t.Errorf("pushes = %d for a mail-disabled domain, want 0", n)
	}

	s2 := newMSStore()
	s2.share("s1", "alice", "bob")
	ag2 := &fakeAgent{}
	newShareReconciler(s2, ag2, false).reconcileMailboxShares(context.Background())
	if n := len(sharePushes(ag2)); n != 0 {
		t.Errorf("pushes = %d with server mail disabled, want 0", n)
	}
}

// A failed apply is not stamped, and it backs off instead of retrying on
// every tick.
func TestReconcileMailboxShares_FailedApplyBacksOff(t *testing.T) {
	s := newMSStore()
	s.share("s1", "alice", "bob")
	ag := &fakeAgent{failMethod: "mailbox.share_set"}
	r := newShareReconciler(s, ag, true)

	r.reconcileMailboxShares(context.Background())
	r.reconcileMailboxShares(context.Background())
	if n := len(sharePushes(ag)); n != 1 {
		t.Fatalf("pushes = %d, want 1 (the retry waits out the back-off)", n)
	}

	// Once the back-off has passed, the owner is retried (never stamped).
	r.mailboxShareMu.Lock()
	r.mailboxShareRetryAt["alice"] = time.Now().Add(-time.Second)
	r.mailboxShareMu.Unlock()
	r.reconcileMailboxShares(context.Background())
	if n := len(sharePushes(ag)); n != 2 {
		t.Fatalf("pushes = %d, want 2 after the back-off", n)
	}
}

// ReconcileAll must run the sweep: before this change no pass pushed
// mailbox shares at all.
func TestReconcileAll_RunsTheMailboxShareSweep(t *testing.T) {
	r, ag, dom := plannerFixture(t)
	dom.EmailEnabled = true
	selector := "jabali"
	dom.DkimSelector = &selector // provisioned already: no email_enable retry
	r.serverSettings.(*fakeServerSettingsRepo).settings.MailEnabled = true

	s := newMSStore()
	for _, id := range []string{"alice", "bob"} {
		mb := s.mailboxes[id]
		mb.DomainID = dom.ID
		s.mailboxes[id] = mb
	}
	s.share("s1", "alice", "bob")
	r.WithMailboxShares(msShares{s: s}, msMailboxes{s: s})

	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("ReconcileAll: %v", err)
	}
	if n := len(sharePushes(ag)); n != 1 {
		t.Fatalf("mailbox.share_set pushes = %d, want 1", n)
	}
}
