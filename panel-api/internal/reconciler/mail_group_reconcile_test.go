package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type mgReconcileFake struct {
	repository.MailGroupRepository
	groups  []repository.MailGroupWithDomain
	members []repository.MailGroupMemberEmail
}

func (f *mgReconcileFake) ListAllWithDomain(context.Context) ([]repository.MailGroupWithDomain, error) {
	return f.groups, nil
}
func (f *mgReconcileFake) ListAllDeliverableMembers(context.Context) ([]repository.MailGroupMemberEmail, error) {
	return f.members, nil
}

func newMailGroupReconciler(t *testing.T, ag *fakeAgent, mg *mgReconcileFake, mailEnabled bool) *Reconciler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	r := New(&domFake{}, nil, ag, log, Config{Interval: time.Second}).WithMailGroups(mg)
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{MailEnabled: mailEnabled}}
	return r
}

func mgRow(id, email, kind string) repository.MailGroupWithDomain {
	return repository.MailGroupWithDomain{
		MailGroup:          models.MailGroup{ID: id, EmailCached: email, GroupKind: kind, DisplayName: "Team"},
		DomainEmailEnabled: true,
	}
}

func applyCalls(ag *fakeAgent) []map[string]any {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var out []map[string]any
	for _, c := range ag.calls {
		if c.method != "mailgroup.apply" {
			continue
		}
		b, _ := json.Marshal(c.params)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		out = append(out, m)
	}
	return out
}

// GH #1818: every distribution group is (re)applied from the DB, which is how
// existing boxes convert their old Group-account projection into a mailing
// list after `jabali update`, and how a failed fire-and-forget apply heals.
func TestReconcileMailGroups_AppliesDistributionGroupsOnly(t *testing.T) {
	ag := &fakeAgent{}
	mg := &mgReconcileFake{
		groups: []repository.MailGroupWithDomain{
			mgRow("g1", "sales@example.org", "distribution"),
			mgRow("g2", "team@example.org", "resource"),
		},
		members: []repository.MailGroupMemberEmail{
			{GroupID: "g1", Email: "alice@example.org"},
			{GroupID: "g1", Email: "bob@example.org"},
			{GroupID: "g2", Email: "carol@example.org"},
		},
	}
	r := newMailGroupReconciler(t, ag, mg, true)

	r.reconcileMailGroups(context.Background())

	calls := applyCalls(ag)
	require.Len(t, calls, 1, "only the distribution group is reconciled")
	require.Equal(t, "sales@example.org", calls[0]["email"])
	require.Equal(t, "distribution", calls[0]["group_kind"])
	require.Equal(t, []any{"alice@example.org", "bob@example.org"}, calls[0]["member_emails"])
}

func TestReconcileMailGroups_SteadyStateIsNoop(t *testing.T) {
	ag := &fakeAgent{}
	mg := &mgReconcileFake{
		groups:  []repository.MailGroupWithDomain{mgRow("g1", "sales@example.org", "distribution")},
		members: []repository.MailGroupMemberEmail{{GroupID: "g1", Email: "alice@example.org"}},
	}
	r := newMailGroupReconciler(t, ag, mg, true)

	r.reconcileMailGroups(context.Background())
	r.reconcileMailGroups(context.Background())
	require.Len(t, applyCalls(ag), 1, "an unchanged group must not be re-applied every tick")

	// A member change (added, removed, disabled — all change the deliverable set)
	// re-applies on the next tick.
	mg.members = append(mg.members, repository.MailGroupMemberEmail{GroupID: "g1", Email: "bob@example.org"})
	r.reconcileMailGroups(context.Background())
	calls := applyCalls(ag)
	require.Len(t, calls, 2)
	require.Equal(t, []any{"alice@example.org", "bob@example.org"}, calls[1]["member_emails"])
}

func TestReconcileMailGroups_StaleEntryReapplies(t *testing.T) {
	ag := &fakeAgent{}
	mg := &mgReconcileFake{groups: []repository.MailGroupWithDomain{mgRow("g1", "sales@example.org", "distribution")}}
	r := newMailGroupReconciler(t, ag, mg, true)

	r.reconcileMailGroups(context.Background())
	r.mailGroupMu.Lock()
	e := r.mailGroupDone["g1"]
	e.at = e.at.Add(-mailGroupReDispatchInterval - time.Minute)
	r.mailGroupDone["g1"] = e
	r.mailGroupMu.Unlock()

	r.reconcileMailGroups(context.Background())
	require.Len(t, applyCalls(ag), 2, "drift on the mail server heals on the periodic re-apply")
}

func TestReconcileMailGroups_FailedApplyRetriesAfterBackoff(t *testing.T) {
	ag := &fakeAgent{errByMethod: map[string]error{"mailgroup.apply": errors.New("agent down")}}
	mg := &mgReconcileFake{groups: []repository.MailGroupWithDomain{mgRow("g1", "sales@example.org", "distribution")}}
	r := newMailGroupReconciler(t, ag, mg, true)

	r.reconcileMailGroups(context.Background())
	r.reconcileMailGroups(context.Background())
	require.Len(t, applyCalls(ag), 1, "a failing group backs off instead of hammering the agent every tick")

	r.mailGroupMu.Lock()
	e := r.mailGroupDone["g1"]
	e.at = e.at.Add(-mailGroupRetryInterval - time.Minute)
	r.mailGroupDone["g1"] = e
	r.mailGroupMu.Unlock()

	r.reconcileMailGroups(context.Background())
	require.Len(t, applyCalls(ag), 2, "a failed apply is retried after the backoff")
}

func TestReconcileMailGroups_MailDisabledSkips(t *testing.T) {
	ag := &fakeAgent{}
	mg := &mgReconcileFake{groups: []repository.MailGroupWithDomain{mgRow("g1", "sales@example.org", "distribution")}}
	r := newMailGroupReconciler(t, ag, mg, false)

	r.reconcileMailGroups(context.Background())
	require.Empty(t, applyCalls(ag))
}

// Turning mail off for a domain (soft disable or the mail-only purge) keeps
// its mail_groups rows. Re-applying them would re-create the Stalwart domain
// and the list for a domain whose mail the admin just switched off.
func TestReconcileMailGroups_SkipsDomainsWithMailOff(t *testing.T) {
	ag := &fakeAgent{}
	row := mgRow("g1", "sales@example.org", "distribution")
	row.DomainEmailEnabled = false
	mg := &mgReconcileFake{groups: []repository.MailGroupWithDomain{row}}
	r := newMailGroupReconciler(t, ag, mg, true)

	r.reconcileMailGroups(context.Background())
	require.Empty(t, applyCalls(ag), "a domain with mail turned off must not be re-projected")
}

type slowMailGroupAgent struct {
	*fakeAgent
	delay time.Duration
}

func (s slowMailGroupAgent) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	time.Sleep(s.delay)
	return s.fakeAgent.Call(ctx, method, params)
}

// Converting a legacy group copies its stored mail, which can be slow. The
// pass stops starting new applies once its time budget is spent, so one slow
// tick cannot stall the rest of ReconcileAll; the remaining groups follow on
// later ticks.
func TestReconcileMailGroups_TimeBudgetDefersRemainingGroups(t *testing.T) {
	orig := mailGroupTickBudget
	mailGroupTickBudget = 50 * time.Millisecond
	t.Cleanup(func() { mailGroupTickBudget = orig })

	inner := &fakeAgent{}
	mg := &mgReconcileFake{}
	for i := 0; i < 5; i++ {
		mg.groups = append(mg.groups, mgRow(fmt.Sprintf("g%d", i), fmt.Sprintf("l%d@example.org", i), "distribution"))
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	r := New(&domFake{}, nil, slowMailGroupAgent{fakeAgent: inner, delay: 30 * time.Millisecond}, log, Config{Interval: time.Second}).WithMailGroups(mg)
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{MailEnabled: true}}

	r.reconcileMailGroups(context.Background())
	first := len(applyCalls(inner))
	require.Less(t, first, 5, "the pass must stop once its time budget is spent")
	require.Greater(t, first, 0)

	for i := 0; i < 5 && len(applyCalls(inner)) < 5; i++ {
		r.reconcileMailGroups(context.Background())
	}
	require.Len(t, applyCalls(inner), 5, "deferred groups are applied on later ticks")
}
