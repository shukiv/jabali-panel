package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/trustedsenders"
)

// GH #2017: every mailbox's trusted senders follow its rows.

// tsrRows serves the batch read (listed) and the apply-time re-read (owned)
// separately, so a test can land a delete between the two.
type tsrRows struct {
	repository.MailboxTrustedSenderRepository
	listed []models.MailboxTrustedSender
	owned  map[string][]models.MailboxTrustedSender
}

func (f *tsrRows) ListAll(context.Context) ([]models.MailboxTrustedSender, error) {
	return f.listed, nil
}

func (f *tsrRows) ListByMailbox(_ context.Context, mbID string) ([]models.MailboxTrustedSender, error) {
	return f.owned[mbID], nil
}

func (f *tsrRows) add(mbID, address string) {
	row := models.MailboxTrustedSender{ID: mbID + "-" + address, MailboxID: mbID, Address: address}
	f.listed = append(f.listed, row)
	if f.owned == nil {
		f.owned = map[string][]models.MailboxTrustedSender{}
	}
	f.owned[mbID] = append(f.owned[mbID], row)
}

func (f *tsrRows) clear(mbID string) {
	var keep []models.MailboxTrustedSender
	for _, r := range f.listed {
		if r.MailboxID != mbID {
			keep = append(keep, r)
		}
	}
	f.listed = keep
	delete(f.owned, mbID)
}

func newTrustedReconciler(s *msStore, rows *tsrRows, ag *fakeAgent, mailEnabled bool) *Reconciler {
	r := New(msDomains{s: s}, nil, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithMailboxTrustedSenders(rows, msMailboxes{s: s})
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{MailEnabled: mailEnabled}}
	return r
}

// trustedPushes returns "email: a,b" per mailbox.trusted_senders.apply call,
// sorted, and clears the calls.
func trustedPushes(ag *fakeAgent) []string {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var out []string
	for _, c := range ag.calls {
		if c.method != trustedsenders.AgentVerb {
			continue
		}
		b, _ := json.Marshal(c.params)
		var spec trustedsenders.Spec
		_ = json.Unmarshal(b, &spec)
		out = append(out, spec.Email+": "+strings.Join(spec.Addresses, ","))
	}
	ag.calls = nil
	sort.Strings(out)
	return out
}

func TestReconcileMailboxTrustedSenders_AppliesOnceThenOnChange(t *testing.T) {
	s := newMSStore()
	rows := &tsrRows{}
	rows.add("alice", "b@x.com")
	rows.add("alice", "a@x.com")
	rows.add("bob", "c@y.com")
	ag := &fakeAgent{}
	r := newTrustedReconciler(s, rows, ag, true)
	ctx := context.Background()

	r.reconcileMailboxTrustedSenders(ctx)
	want := []string{"alice@one.test: a@x.com,b@x.com", "bob@one.test: c@y.com"}
	if got := trustedPushes(ag); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("first tick pushed %v, want %v", got, want)
	}
	r.reconcileMailboxTrustedSenders(ctx)
	if got := trustedPushes(ag); len(got) != 0 {
		t.Fatalf("steady tick pushed %v", got)
	}
	rows.add("alice", "d@z.com")
	r.reconcileMailboxTrustedSenders(ctx)
	if got := trustedPushes(ag); len(got) != 1 || got[0] != "alice@one.test: a@x.com,b@x.com,d@z.com" {
		t.Fatalf("after a change pushed %v", got)
	}
}

// No mail module, or a domain whose mail is off or hosted elsewhere: there
// is no Stalwart account to write to.
func TestReconcileMailboxTrustedSenders_OnlyWhereThePanelHostsTheMail(t *testing.T) {
	s := newMSStore()
	s.domains["dext"] = models.Domain{ID: "dext", Name: "ext.test", EmailEnabled: true, MailProvider: "external"}
	s.mailboxes["erin"] = models.Mailbox{ID: "erin", DomainID: "dext", EmailCached: "erin@ext.test"}
	rows := &tsrRows{}
	rows.add("dave", "a@x.com")  // domain doff: mail off
	rows.add("erin", "a@x.com")  // domain dext: mail elsewhere
	rows.add("ghost", "a@x.com") // no such mailbox
	ag := &fakeAgent{}
	newTrustedReconciler(s, rows, ag, true).reconcileMailboxTrustedSenders(context.Background())
	if got := trustedPushes(ag); len(got) != 0 {
		t.Fatalf("pushed %v", got)
	}

	rows.add("alice", "a@x.com")
	newTrustedReconciler(s, rows, ag, false).reconcileMailboxTrustedSenders(context.Background())
	if got := trustedPushes(ag); len(got) != 0 {
		t.Fatalf("mail module off: pushed %v", got)
	}
}

// The push sends the rows as they are when it runs, so a sender deleted
// after the tick's batch read is not put back.
func TestReconcileMailboxTrustedSenders_PushesTheRowsAsTheyAreNow(t *testing.T) {
	s := newMSStore()
	rows := &tsrRows{}
	rows.add("alice", "a@x.com")
	rows.add("alice", "gone@x.com")
	rows.owned["alice"] = rows.owned["alice"][:1] // gone@x.com deleted meanwhile
	ag := &fakeAgent{}
	newTrustedReconciler(s, rows, ag, true).reconcileMailboxTrustedSenders(context.Background())
	if got := trustedPushes(ag); len(got) != 1 || got[0] != "alice@one.test: a@x.com" {
		t.Fatalf("pushed %v", got)
	}
}

// A failed push is retried, after a pause so a mailbox that keeps failing
// does not use every tick's budget.
func TestReconcileMailboxTrustedSenders_RetriesAFailedPush(t *testing.T) {
	s := newMSStore()
	rows := &tsrRows{}
	rows.add("alice", "a@x.com")
	ag := &fakeAgent{failMethod: trustedsenders.AgentVerb}
	r := newTrustedReconciler(s, rows, ag, true)
	ctx := context.Background()

	r.reconcileMailboxTrustedSenders(ctx)
	if got := trustedPushes(ag); len(got) != 1 {
		t.Fatalf("first tick pushed %v", got)
	}
	ag.failMethod = ""
	r.reconcileMailboxTrustedSenders(ctx)
	if got := trustedPushes(ag); len(got) != 0 {
		t.Fatalf("retried inside the back-off: %v", got)
	}
	r.trustedSendersRetryAt["alice"] = time.Now().Add(-time.Second)
	r.reconcileMailboxTrustedSenders(ctx)
	if got := trustedPushes(ag); len(got) != 1 {
		t.Fatalf("after the back-off pushed %v, want a retry", got)
	}
	r.reconcileMailboxTrustedSenders(ctx)
	if got := trustedPushes(ag); len(got) != 0 {
		t.Fatalf("after a good retry pushed %v", got)
	}
}

// A mailbox whose rows are all gone drops out of the pass and out of the
// ledger. Trusting the same senders again later is pushed, not skipped as
// already applied.
func TestReconcileMailboxTrustedSenders_ForgetsAMailboxWithNoRows(t *testing.T) {
	s := newMSStore()
	rows := &tsrRows{}
	rows.add("alice", "a@x.com")
	ag := &fakeAgent{}
	r := newTrustedReconciler(s, rows, ag, true)
	ctx := context.Background()
	r.reconcileMailboxTrustedSenders(ctx)
	_ = trustedPushes(ag)

	rows.clear("alice") // the API pushed the empty list and deleted the row
	r.reconcileMailboxTrustedSenders(ctx)
	if got := trustedPushes(ag); len(got) != 0 {
		t.Fatalf("pushed %v for a mailbox with no rows", got)
	}
	rows.add("alice", "a@x.com") // trusted again; say the API's push failed
	r.reconcileMailboxTrustedSenders(ctx)
	if got := trustedPushes(ag); len(got) != 1 || got[0] != "alice@one.test: a@x.com" {
		t.Fatalf("pushed %v, want the re-added list", got)
	}
}

// At most trustedSendersApplyBudgetPerTick mailboxes per tick.
func TestReconcileMailboxTrustedSenders_Budget(t *testing.T) {
	s := newMSStore()
	rows := &tsrRows{}
	n := trustedSendersApplyBudgetPerTick + 5
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("mb%02d", i)
		s.mailboxes[id] = models.Mailbox{ID: id, DomainID: "d1", EmailCached: id + "@one.test"}
		rows.add(id, "a@x.com")
	}
	ag := &fakeAgent{}
	r := newTrustedReconciler(s, rows, ag, true)
	r.reconcileMailboxTrustedSenders(context.Background())
	if got := trustedPushes(ag); len(got) != trustedSendersApplyBudgetPerTick {
		t.Fatalf("first tick pushed %d", len(got))
	}
	r.reconcileMailboxTrustedSenders(context.Background())
	if got := trustedPushes(ag); len(got) != 5 {
		t.Fatalf("second tick pushed %d, want the rest", len(got))
	}
}

func TestReconcileAll_RunsTheTrustedSendersPass(t *testing.T) {
	r, ag, dom := plannerFixture(t)
	dom.EmailEnabled = true
	selector := "jabali"
	dom.DkimSelector = &selector // provisioned already: no email_enable retry
	r.serverSettings.(*fakeServerSettingsRepo).settings.MailEnabled = true

	s := newMSStore()
	mb := s.mailboxes["alice"]
	mb.DomainID = dom.ID
	s.mailboxes["alice"] = mb
	rows := &tsrRows{}
	rows.add("alice", "a@x.com")
	r.WithMailboxTrustedSenders(rows, msMailboxes{s: s})

	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("ReconcileAll: %v", err)
	}
	if got := trustedPushes(ag); len(got) != 1 || got[0] != "alice@one.test: a@x.com" {
		t.Fatalf("ReconcileAll pushed %v", got)
	}
}

func TestReconcileMailboxTrustedSenders_NotWired(t *testing.T) {
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, &fakeAgent{}, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second})
	r.reconcileMailboxTrustedSenders(context.Background()) // must not panic
}
