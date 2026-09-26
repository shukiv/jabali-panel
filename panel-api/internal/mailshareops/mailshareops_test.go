package mailshareops

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// store is an in-memory mailbox/domain/share store. Each repo wrapper embeds
// its nil interface for the methods these tests never call.
type store struct {
	mailboxes map[string]models.Mailbox
	domains   map[string]models.Domain
	shares    map[string]models.MailboxShare
}

func newStore() *store {
	s := &store{
		mailboxes: map[string]models.Mailbox{},
		domains:   map[string]models.Domain{},
		shares:    map[string]models.MailboxShare{},
	}
	// Tenant u1 owns two domains; tenant u2 owns one.
	s.domains["d1"] = models.Domain{ID: "d1", UserID: "u1", Name: "one.test"}
	s.domains["d1b"] = models.Domain{ID: "d1b", UserID: "u1", Name: "one-b.test"}
	s.domains["d2"] = models.Domain{ID: "d2", UserID: "u2", Name: "two.test"}
	for _, mb := range []models.Mailbox{
		{ID: "alice", DomainID: "d1", LocalPart: "alice", EmailCached: "alice@one.test"},
		{ID: "bob", DomainID: "d1", LocalPart: "bob", EmailCached: "bob@one.test"},
		{ID: "carol", DomainID: "d1b", LocalPart: "carol", EmailCached: "carol@one-b.test"},
		{ID: "mallory", DomainID: "d2", LocalPart: "mallory", EmailCached: "mallory@two.test"},
	} {
		s.mailboxes[mb.ID] = mb
	}
	return s
}

func (s *store) deps(ag *fakeAgent) Deps {
	d := Deps{Mailboxes: mbRepo{store: s}, Domains: domRepo{store: s}, Shares: shareRepo{store: s}}
	if ag != nil {
		d.Agent = ag
	}
	return d
}

type mbRepo struct {
	repository.MailboxRepository
	*store
}

func (r mbRepo) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	mb, ok := r.mailboxes[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &mb, nil
}

func (r mbRepo) FindByIDs(_ context.Context, ids []string) ([]models.Mailbox, error) {
	var out []models.Mailbox
	for _, id := range ids {
		if mb, ok := r.mailboxes[id]; ok {
			out = append(out, mb)
		}
	}
	return out, nil
}

type domRepo struct {
	repository.DomainRepository
	*store
}

func (r domRepo) FindByID(_ context.Context, id string) (*models.Domain, error) {
	d, ok := r.domains[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &d, nil
}

type shareRepo struct {
	repository.MailboxShareRepository
	*store
}

func (r shareRepo) FindByID(_ context.Context, id string) (*models.MailboxShare, error) {
	s, ok := r.shares[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &s, nil
}

func (r shareRepo) FindByOwnerID(_ context.Context, owner string, _ repository.ListOptions) ([]models.MailboxShare, int64, error) {
	var out []models.MailboxShare
	for _, s := range r.shares {
		if s.OwnerMailboxID == owner {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, int64(len(out)), nil
}

func (r shareRepo) Create(_ context.Context, s *models.MailboxShare) error {
	r.shares[s.ID] = *s
	return nil
}

func (r shareRepo) DeleteByOwner(_ context.Context, id, owner string) error {
	s, ok := r.shares[id]
	if !ok || s.OwnerMailboxID != owner {
		return repository.ErrNotFound
	}
	delete(r.shares, id)
	return nil
}

// fakeAgent records every mailbox.share_set payload.
type fakeAgent struct {
	pushes []Payload
	err    error
}

func (a *fakeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd != "mailbox.share_set" {
		return nil, errors.New("unexpected command " + cmd)
	}
	a.pushes = append(a.pushes, params.(Payload))
	return nil, a.err
}

func (a *fakeAgent) last(t *testing.T) Payload {
	t.Helper()
	if len(a.pushes) == 0 {
		t.Fatal("no mailbox.share_set was sent")
	}
	return a.pushes[len(a.pushes)-1]
}

func (s *store) mb(id string) *models.Mailbox {
	mb := s.mailboxes[id]
	return &mb
}

var read = models.Rights{MayRead: true}

func TestCreate_AppliesTheOwnersFullList(t *testing.T) {
	s := newStore()
	s.shares["s0"] = models.MailboxShare{ID: "s0", OwnerMailboxID: "alice", SharedWithMailboxID: "carol", Rights: read}
	ag := &fakeAgent{}

	res, err := Create(context.Background(), s.deps(ag), s.mb("alice"), "bob", models.Rights{MayRead: true, MayAddItems: true}, "m6.5")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ApplyErr != nil {
		t.Fatalf("ApplyErr = %v, want nil", res.ApplyErr)
	}
	if _, ok := s.shares[res.Share.ID]; !ok {
		t.Fatal("share row was not saved")
	}
	p := ag.last(t)
	if p.OwnerEmail != "alice@one.test" {
		t.Errorf("owner_email = %q", p.OwnerEmail)
	}
	// The whole list: the new share AND the one that already existed.
	if len(p.Shares) != 2 || !p.Shares["bob@one.test"].MayAddItems || !p.Shares["carol@one-b.test"].MayRead {
		t.Errorf("shares = %+v, want bob (read+add) and carol (read)", p.Shares)
	}
}

func TestCreate_RefusesATargetInAnotherAccount(t *testing.T) {
	s := newStore()
	ag := &fakeAgent{}
	_, err := Create(context.Background(), s.deps(ag), s.mb("alice"), "mallory", read, "m6.5")
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("err = %v, want ErrTargetNotFound", err)
	}
	if len(s.shares) != 0 || len(ag.pushes) != 0 {
		t.Errorf("rows=%d pushes=%d, want nothing saved or sent", len(s.shares), len(ag.pushes))
	}
}

func TestCreate_AllowsAnotherDomainOfTheSameAccount(t *testing.T) {
	s := newStore()
	if _, err := Create(context.Background(), s.deps(&fakeAgent{}), s.mb("alice"), "carol", read, "m6.5"); err != nil {
		t.Fatalf("Create alice→carol (same user, other domain): %v", err)
	}
}

func TestCreate_Rejections(t *testing.T) {
	cases := []struct {
		name   string
		target string
		rights models.Rights
		seed   bool
		want   error
	}{
		{"self", "alice", read, false, ErrSelfShare},
		{"no rights", "bob", models.Rights{}, false, ErrNoRights},
		{"missing target", "nobody", read, false, ErrTargetNotFound},
		{"duplicate", "bob", read, true, ErrAlreadyShared},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore()
			if tc.seed {
				s.shares["s0"] = models.MailboxShare{ID: "s0", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: read}
			}
			ag := &fakeAgent{}
			_, err := Create(context.Background(), s.deps(ag), s.mb("alice"), tc.target, tc.rights, "m6.5")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(ag.pushes) != 0 {
				t.Errorf("a rejected create sent %d pushes", len(ag.pushes))
			}
		})
	}
}

func TestCreate_ApplyFailureKeepsTheRowAndReportsIt(t *testing.T) {
	s := newStore()
	ag := &fakeAgent{err: errors.New("stalwart down")}
	res, err := Create(context.Background(), s.deps(ag), s.mb("alice"), "bob", read, "m6.5")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !errors.Is(res.ApplyErr, ErrApply) {
		t.Fatalf("ApplyErr = %v, want ErrApply", res.ApplyErr)
	}
	if _, ok := s.shares[res.Share.ID]; !ok {
		t.Fatal("row must stay for the reconcile sweep to retry")
	}
}

func TestDelete_PushesTheListWithoutTheShareThenDeletesTheRow(t *testing.T) {
	s := newStore()
	s.shares["s1"] = models.MailboxShare{ID: "s1", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: read}
	s.shares["s2"] = models.MailboxShare{ID: "s2", OwnerMailboxID: "alice", SharedWithMailboxID: "carol", Rights: read}
	ag := &fakeAgent{}

	if err := Delete(context.Background(), s.deps(ag), "alice", "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	p := ag.last(t)
	if _, still := p.Shares["bob@one.test"]; still || len(p.Shares) != 1 {
		t.Errorf("pushed shares = %+v, want only carol", p.Shares)
	}
	if _, ok := s.shares["s1"]; ok {
		t.Error("row s1 still present after a successful revoke")
	}
}

func TestDelete_LastShareSendsAnEmptyList(t *testing.T) {
	s := newStore()
	s.shares["s1"] = models.MailboxShare{ID: "s1", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: read}
	ag := &fakeAgent{}
	if err := Delete(context.Background(), s.deps(ag), "alice", "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if p := ag.last(t); p.Shares == nil || len(p.Shares) != 0 {
		t.Errorf("shares = %#v, want an empty (non-nil) map so Stalwart clears shareWith", p.Shares)
	}
}

func TestDelete_PushFailureKeepsTheRow(t *testing.T) {
	s := newStore()
	s.shares["s1"] = models.MailboxShare{ID: "s1", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: read}
	ag := &fakeAgent{err: errors.New("stalwart down")}
	err := Delete(context.Background(), s.deps(ag), "alice", "s1")
	if !errors.Is(err, ErrApply) {
		t.Fatalf("err = %v, want ErrApply", err)
	}
	if _, ok := s.shares["s1"]; !ok {
		t.Fatal("row deleted although Stalwart still has the share")
	}
}

func TestDelete_WithoutAnAgentFailsClosed(t *testing.T) {
	s := newStore()
	s.shares["s1"] = models.MailboxShare{ID: "s1", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: read}
	err := Delete(context.Background(), s.deps(nil), "alice", "s1")
	if !errors.Is(err, ErrApply) {
		t.Fatalf("err = %v, want ErrApply", err)
	}
	if _, ok := s.shares["s1"]; !ok {
		t.Fatal("row deleted with no agent to revoke the share")
	}
}

func TestDelete_AnotherOwnersShareIsNotFound(t *testing.T) {
	s := newStore()
	s.shares["s1"] = models.MailboxShare{ID: "s1", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: read}
	ag := &fakeAgent{}
	if err := Delete(context.Background(), s.deps(ag), "bob", "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(ag.pushes) != 0 {
		t.Error("a refused delete must not push anything")
	}
}
