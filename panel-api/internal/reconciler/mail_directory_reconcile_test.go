package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type mdStore struct {
	domains   []models.Domain
	mailboxes []models.Mailbox
	groups    map[string]bool // domainID + "/" + local part
	resources map[string]bool // email
	lookupErr error
}

type mdDomains struct {
	repository.DomainRepository
	s *mdStore
}

func (f mdDomains) List(_ context.Context, _ repository.ListOptions) ([]models.Domain, int64, error) {
	return f.s.domains, int64(len(f.s.domains)), nil
}

type mdMailboxes struct {
	repository.MailboxRepository
	s *mdStore
}

func (f mdMailboxes) ListByDomainIDs(_ context.Context, ids []string) ([]models.Mailbox, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []models.Mailbox
	for _, mb := range f.s.mailboxes {
		if want[mb.DomainID] {
			out = append(out, mb)
		}
	}
	return out, nil
}

type mdGroups struct {
	repository.MailGroupRepository
	s *mdStore
}

func (f mdGroups) ExistsByDomainAndLocalPart(_ context.Context, domainID, local string) (bool, error) {
	if f.s.lookupErr != nil {
		return false, f.s.lookupErr
	}
	return f.s.groups[domainID+"/"+local], nil
}

type mdResources struct {
	repository.SharedResourceRepository
	s *mdStore
}

func (f mdResources) ExistsByEmail(_ context.Context, email string) (bool, error) {
	return f.s.resources[email], nil
}

func newMDStore() *mdStore {
	return &mdStore{
		domains: []models.Domain{
			{ID: "d1", Name: "one.test", EmailEnabled: true},
			{ID: "dext", Name: "ext.test", EmailEnabled: true, MailProvider: models.MailProviderM365},
			{ID: "doff", Name: "off.test", EmailEnabled: false},
			{ID: "dsmtp", Name: "smtp.test", EmailEnabled: true},
		},
		mailboxes: []models.Mailbox{
			{ID: "m-alice", DomainID: "d1", LocalPart: "alice", EmailCached: "alice@one.test", DisplayName: "Alice Adams"},
			{ID: "m-bob", DomainID: "d1", LocalPart: "bob", EmailCached: "bob@one.test", DisplayName: "Bob", IsDisabled: true},
			{ID: "m-carol", DomainID: "d1", LocalPart: "carol", EmailCached: "carol@one.test"},
			{ID: "m-relay", DomainID: "d1", LocalPart: "noreply", EmailCached: "noreply@one.test", System: true},
			{ID: "m-scan", DomainID: "d1", LocalPart: "scanner", EmailCached: "scanner@one.test", SendOnly: true},
			{ID: "m-ext", DomainID: "dext", LocalPart: "eve", EmailCached: "eve@ext.test"},
			{ID: "m-off", DomainID: "doff", LocalPart: "oscar", EmailCached: "oscar@off.test"},
			{ID: "m-smtp", DomainID: "dsmtp", LocalPart: "printer", EmailCached: "printer@smtp.test", SendOnly: true},
		},
		groups:    map[string]bool{},
		resources: map[string]bool{},
	}
}

func newDirectoryReconciler(s *mdStore, ag *fakeAgent, mailEnabled bool) *Reconciler {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	r := New(mdDomains{s: s}, nil, ag, log, Config{Interval: time.Second}).
		WithMailDirectory(mdMailboxes{s: s}, mdGroups{s: s}, mdResources{s: s})
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{MailEnabled: mailEnabled}}
	return r
}

// directoryApplies returns every mail.directory.apply payload in call order.
func directoryApplies(ag *fakeAgent) []mailDirectorySpec {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var out []mailDirectorySpec
	for _, c := range ag.calls {
		if c.method == "mail.directory.apply" {
			out = append(out, c.params.(mailDirectorySpec))
		}
	}
	return out
}

// clearBackoff lets a test run the next tick at once after a failure.
func clearBackoff(r *Reconciler) {
	r.mailDirMu.Lock()
	r.mailDirRetryAt = nil
	r.mailDirMu.Unlock()
}

// Only domains whose mail the panel hosts get a directory, and it lists the
// domain's enabled mailboxes a person uses: no system relay, no send-only
// account, no disabled mailbox.
func TestReconcileMailDirectories_SendsEachMailDomainsDirectory(t *testing.T) {
	s := newMDStore()
	ag := &fakeAgent{}

	newDirectoryReconciler(s, ag, true).reconcileMailDirectories(context.Background())

	got := directoryApplies(ag)
	if len(got) != 1 {
		t.Fatalf("applies = %+v, want only one.test's", got)
	}
	spec := got[0]
	if spec.HostEmail != "jabali-directory@one.test" || spec.DisplayName != "one.test directory" {
		t.Errorf("host = %q name = %q", spec.HostEmail, spec.DisplayName)
	}
	wantEntries := []mailDirectoryEntry{{Email: "alice@one.test", Name: "Alice Adams"}, {Email: "carol@one.test", Name: ""}}
	if fmt.Sprint(spec.Entries) != fmt.Sprint(wantEntries) {
		t.Errorf("entries = %v, want %v", spec.Entries, wantEntries)
	}
	if strings.Join(spec.Readers, ",") != "alice@one.test,carol@one.test" {
		t.Errorf("readers = %v, want alice and carol", spec.Readers)
	}
}

func TestReconcileMailDirectories_NothingWhileMailIsOff(t *testing.T) {
	ag := &fakeAgent{}
	newDirectoryReconciler(newMDStore(), ag, false).reconcileMailDirectories(context.Background())
	if n := len(directoryApplies(ag)); n != 0 {
		t.Fatalf("applies = %d, want 0 with mail off", n)
	}
}

// A domain whose last enabled mailbox was disabled still gets an apply, with
// no readers, so that mailbox's grant is withdrawn.
func TestReconcileMailDirectories_WithdrawsTheLastGrant(t *testing.T) {
	s := newMDStore()
	for i := range s.mailboxes {
		if s.mailboxes[i].DomainID == "d1" {
			s.mailboxes[i].IsDisabled = true
		}
	}
	ag := &fakeAgent{}

	newDirectoryReconciler(s, ag, true).reconcileMailDirectories(context.Background())

	got := directoryApplies(ag)
	if len(got) != 1 || len(got[0].Readers) != 0 || len(got[0].Entries) != 0 {
		t.Fatalf("applies = %+v, want one with no readers and no entries", got)
	}
}

func TestReconcileMailDirectories_SkipsUnchangedAndFollowsChanges(t *testing.T) {
	s := newMDStore()
	ag := &fakeAgent{}
	r := newDirectoryReconciler(s, ag, true)
	ctx := context.Background()

	r.reconcileMailDirectories(ctx)
	r.reconcileMailDirectories(ctx)
	if n := len(directoryApplies(ag)); n != 1 {
		t.Fatalf("applies = %d, want 1 (the unchanged second tick is skipped)", n)
	}

	s.mailboxes = append(s.mailboxes, models.Mailbox{ID: "m-dan", DomainID: "d1", LocalPart: "dan", EmailCached: "dan@one.test"})
	r.reconcileMailDirectories(ctx)
	got := directoryApplies(ag)
	if len(got) != 2 || len(got[1].Readers) != 3 {
		t.Fatalf("applies = %+v, want a second apply with dan", got)
	}

	// carol deleted and created again: the same address is a new Stalwart
	// account that has no grant yet.
	for i := range s.mailboxes {
		if s.mailboxes[i].ID == "m-carol" {
			s.mailboxes[i].ID = "m-carol-2"
		}
	}
	r.reconcileMailDirectories(ctx)
	if n := len(directoryApplies(ag)); n != 3 {
		t.Fatalf("applies = %d, want 3 (a re-created mailbox is re-applied)", n)
	}
}

// Turning mail off purges the domain's accounts, the directory's host with
// them. The same mailboxes on its return must rebuild it at once, not at the
// next audit.
func TestReconcileMailDirectories_RebuildsAfterMailComesBack(t *testing.T) {
	s := newMDStore()
	ag := &fakeAgent{}
	r := newDirectoryReconciler(s, ag, true)
	ctx := context.Background()

	r.reconcileMailDirectories(ctx)
	s.domains[0].EmailEnabled = false
	r.reconcileMailDirectories(ctx)
	s.domains[0].EmailEnabled = true
	r.reconcileMailDirectories(ctx)

	if n := len(directoryApplies(ag)); n != 2 {
		t.Fatalf("applies = %d, want 2 (the return re-applies)", n)
	}
}

// The directory's address held by a mailbox, mail group or shared resource
// (rows older than the reserved-name guard) means its principal is not the
// panel's host: nothing is sent.
func TestReconcileMailDirectories_LeavesATakenAddressAlone(t *testing.T) {
	cases := map[string]func(s *mdStore){
		"mailbox": func(s *mdStore) {
			s.mailboxes = append(s.mailboxes, models.Mailbox{ID: "m-x", DomainID: "d1", LocalPart: "jabali-directory", EmailCached: "jabali-directory@one.test"})
		},
		"mail group":      func(s *mdStore) { s.groups["d1/jabali-directory"] = true },
		"shared resource": func(s *mdStore) { s.resources["jabali-directory@one.test"] = true },
		"lookup error":    func(s *mdStore) { s.lookupErr = errors.New("db down") },
	}
	for name, taint := range cases {
		t.Run(name, func(t *testing.T) {
			s := newMDStore()
			taint(s)
			ag := &fakeAgent{}
			newDirectoryReconciler(s, ag, true).reconcileMailDirectories(context.Background())
			if n := len(directoryApplies(ag)); n != 0 {
				t.Fatalf("applies = %d, want 0", n)
			}
		})
	}
}

func TestReconcileMailDirectories_FailureBacksOffAndIsRetried(t *testing.T) {
	s := newMDStore()
	ag := &fakeAgent{failMethod: "mail.directory.apply"}
	r := newDirectoryReconciler(s, ag, true)
	ctx := context.Background()

	r.reconcileMailDirectories(ctx)
	r.reconcileMailDirectories(ctx)
	if n := len(directoryApplies(ag)); n != 1 {
		t.Fatalf("applies = %d, want 1 (the failed domain backs off)", n)
	}

	ag.failMethod = ""
	clearBackoff(r)
	r.reconcileMailDirectories(ctx)
	if n := len(directoryApplies(ag)); n != 2 {
		t.Fatalf("applies = %d, want 2 (a failure is not stamped)", n)
	}
}

// A reader the agent could not find in Stalwart has no grant: the domain is
// retried, not stamped as done.
func TestReconcileMailDirectories_UnresolvedReaderIsRetried(t *testing.T) {
	s := newMDStore()
	ag := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"mail.directory.apply": json.RawMessage(`{"ok":true,"readers":1,"readers_unresolved":1}`),
	}}
	r := newDirectoryReconciler(s, ag, true)
	ctx := context.Background()

	r.reconcileMailDirectories(ctx)
	clearBackoff(r)
	r.reconcileMailDirectories(ctx)
	if n := len(directoryApplies(ag)); n != 2 {
		t.Fatalf("applies = %d, want 2", n)
	}
}

func TestReconcileMailDirectories_BudgetPerTick(t *testing.T) {
	s := &mdStore{groups: map[string]bool{}, resources: map[string]bool{}}
	for i := 0; i < mailDirectoryApplyBudgetPerTick+5; i++ {
		id := fmt.Sprintf("d%02d", i)
		name := fmt.Sprintf("n%02d.test", i)
		s.domains = append(s.domains, models.Domain{ID: id, Name: name, EmailEnabled: true})
		s.mailboxes = append(s.mailboxes, models.Mailbox{ID: "m" + id, DomainID: id, LocalPart: "a", EmailCached: "a@" + name})
	}
	ag := &fakeAgent{}
	r := newDirectoryReconciler(s, ag, true)
	ctx := context.Background()

	r.reconcileMailDirectories(ctx)
	if n := len(directoryApplies(ag)); n != mailDirectoryApplyBudgetPerTick {
		t.Fatalf("first tick applies = %d, want %d", n, mailDirectoryApplyBudgetPerTick)
	}
	r.reconcileMailDirectories(ctx)
	if n := len(directoryApplies(ag)); n != mailDirectoryApplyBudgetPerTick+5 {
		t.Fatalf("after second tick applies = %d, want %d", n, mailDirectoryApplyBudgetPerTick+5)
	}
}

func TestMailDirectoryName(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	cases := map[string]string{
		"  Alice  ":       "Alice",
		"Bob\x00\x1b[31m": "Bob[31m",
		"Line\nBreak":     "LineBreak",
		long:              strings.Repeat("é", 127),
		"":                "",
		"​Zero\u0085W":    "​ZeroW",
	}
	for in, want := range cases {
		if got := mailDirectoryName(in); got != want {
			t.Errorf("mailDirectoryName(%q) = %q, want %q", in, got, want)
		}
	}
}

// An address not in the canonical form the agent accepts is left out rather
// than sent, so one bad row cannot fail the whole domain's apply.
func TestBuildMailDirectoryPlan_LeavesOutNonCanonicalRows(t *testing.T) {
	dom := models.Domain{ID: "d1", Name: "One.Test", EmailEnabled: true}
	plan := buildMailDirectoryPlan(dom, []models.Mailbox{
		{ID: "a", DomainID: "d1", LocalPart: "alice", EmailCached: "alice@one.test"},
		{ID: "b", DomainID: "d1", LocalPart: "Bob", EmailCached: "Bob@one.test"},
		{ID: "c", DomainID: "d1", LocalPart: "carol", EmailCached: "carol@other.test"},
	})
	if plan.Spec.HostEmail != "jabali-directory@one.test" {
		t.Errorf("host = %q", plan.Spec.HostEmail)
	}
	if plan.Spec.DisplayName != "one.test directory" {
		t.Errorf("display name = %q", plan.Spec.DisplayName)
	}
	if strings.Join(plan.Spec.Readers, ",") != "alice@one.test" || len(plan.Spec.Entries) != 1 {
		t.Errorf("readers = %v entries = %v, want only alice", plan.Spec.Readers, plan.Spec.Entries)
	}
}

// The book's name is capped like a mailbox name: a long domain must not make
// the agent refuse the whole directory.
func TestBuildMailDirectoryPlan_CapsTheBookName(t *testing.T) {
	label := strings.Repeat("a", 63)
	name := strings.Join([]string{label, label, label, strings.Repeat("b", 61)}, ".") // 253 bytes, the longest a domain can be
	plan := buildMailDirectoryPlan(models.Domain{ID: "d1", Name: name, EmailEnabled: true}, []models.Mailbox{
		{ID: "a", DomainID: "d1", LocalPart: "alice", EmailCached: "alice@" + name},
	})
	if !plan.Needed {
		t.Fatal("plan not needed")
	}
	if n := len(plan.Spec.DisplayName); n > mailDirectoryMaxName {
		t.Fatalf("display name is %d bytes, want at most %d", n, mailDirectoryMaxName)
	}
}
