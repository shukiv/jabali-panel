package reconciler

import (
	"context"
	"encoding/json"
	"errors"
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
	return repository.MailGroupWithDomain{MailGroup: models.MailGroup{ID: id, EmailCached: email, GroupKind: kind, DisplayName: "Team"}}
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
