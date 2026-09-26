package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestApplyLedgerDecide(t *testing.T) {
	var l applyLedger
	p := Phase{Name: "test.phase", AuditInterval: 10 * time.Minute}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

	if got := l.decide(p, "r1", "h1", now, RunNormal); got != decisionApply {
		t.Fatalf("a resource this process never applied must apply, got %v", got)
	}
	l.stamp(p, "r1", "h1", now)

	cases := []struct {
		name string
		hash string
		at   time.Time
		mode RunMode
		want decision
	}{
		{"unchanged within the interval", "h1", now.Add(time.Minute), RunNormal, decisionSkip},
		{"changed within the interval", "h2", now.Add(time.Minute), RunNormal, decisionApply},
		{"unchanged past the interval", "h1", now.Add(10 * time.Minute), RunNormal, decisionAudit},
		{"audit run, unchanged", "h1", now.Add(time.Minute), RunAudit, decisionAudit},
		{"audit run, changed", "h2", now.Add(time.Minute), RunAudit, decisionApply},
		{"force run, unchanged", "h1", now.Add(time.Minute), RunForce, decisionApply},
		{"empty hash", "", now.Add(time.Minute), RunNormal, decisionApply},
	}
	for _, c := range cases {
		if got := l.decide(p, "r1", c.hash, c.at, c.mode); got != c.want {
			t.Errorf("%s: decide = %v, want %v", c.name, got, c.want)
		}
	}

	other := Phase{Name: "other.phase", AuditInterval: time.Hour}
	if got := l.decide(other, "r1", "h1", now.Add(time.Minute), RunNormal); got != decisionApply {
		t.Error("a stamp in one phase must not satisfy another phase for the same resource id")
	}

	l.stamp(p, "r2", "", now)
	if _, ok := l.lookup(p, "r2"); ok {
		t.Error("an empty hash must never be stamped")
	}
}

// The ticket's first criterion, measured: an unchanged second normal run
// sends no vhost or zone to the Agent, and its report says so.
func TestRun_UnchangedSecondNormalRunSkipsAndReportsIt(t *testing.T) {
	r, ag, _ := plannerFixture(t)
	ctx := context.Background()

	first, err := r.Run(ctx, RunNormal)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Phase{PhaseDomainVhost, PhaseDNSZone} {
		if c := first.Phases[p.Name]; c.Applied != 1 || c.Skipped != 0 {
			t.Errorf("first run, %s: %+v, want applied=1", p.Name, c)
		}
	}
	creates, pushes := countMethod(ag, "domain.create"), countMethod(ag, "dns.zone.upsert")

	second, err := r.Run(ctx, RunNormal)
	if err != nil {
		t.Fatal(err)
	}
	if got := countMethod(ag, "domain.create"); got != creates {
		t.Errorf("an unchanged second run must not re-send domain.create (%d → %d)", creates, got)
	}
	if got := countMethod(ag, "dns.zone.upsert"); got != pushes {
		t.Errorf("an unchanged second run must not re-push the zone (%d → %d)", pushes, got)
	}
	for _, p := range []Phase{PhaseDomainVhost, PhaseDNSZone} {
		if c := second.Phases[p.Name]; c.Skipped != 1 || c.Applied != 0 || c.Audited != 0 {
			t.Errorf("second run, %s: %+v, want skipped=1 only", p.Name, c)
		}
	}
	if second.Mode != RunNormal {
		t.Errorf("report mode = %v", second.Mode)
	}
}

func TestRun_AuditReappliesUnchangedResources(t *testing.T) {
	r, ag, _ := plannerFixture(t)
	ctx := context.Background()
	if _, err := r.Run(ctx, RunNormal); err != nil {
		t.Fatal(err)
	}
	creates, pushes := countMethod(ag, "domain.create"), countMethod(ag, "dns.zone.upsert")

	rep, err := r.Run(ctx, RunAudit)
	if err != nil {
		t.Fatal(err)
	}
	if countMethod(ag, "domain.create") != creates+1 || countMethod(ag, "dns.zone.upsert") != pushes+1 {
		t.Fatal("an audit run must re-send every unchanged vhost and zone")
	}
	for _, p := range []Phase{PhaseDomainVhost, PhaseDNSZone} {
		if c := rep.Phases[p.Name]; c.Audited != 1 || c.Applied != 0 {
			t.Errorf("audit run, %s: %+v, want audited=1", p.Name, c)
		}
	}

	// The audit stamped the ledger, so the next normal run skips again.
	if rep, _ := r.Run(ctx, RunNormal); rep.Phases[PhaseDNSZone.Name].Skipped != 1 {
		t.Errorf("a normal run after an audit must skip: %+v", rep.Phases[PhaseDNSZone.Name])
	}
}

func TestRun_ForceReappliesAndReportsForce(t *testing.T) {
	r, ag, _ := plannerFixture(t)
	ctx := context.Background()
	if _, err := r.Run(ctx, RunNormal); err != nil {
		t.Fatal(err)
	}
	pushes := countMethod(ag, "dns.zone.upsert")

	rep, err := r.Run(ctx, RunForce)
	if err != nil {
		t.Fatal(err)
	}
	if countMethod(ag, "dns.zone.upsert") != pushes+1 {
		t.Fatal("a force run must re-push the zone")
	}
	if rep.Mode != RunForce || rep.Phases[PhaseDNSZone.Name].Applied != 1 {
		t.Errorf("force report: mode=%v dns=%+v", rep.Mode, rep.Phases[PhaseDNSZone.Name])
	}
}

// A failed apply is counted, never stamped, and retried on the next run.
func TestRun_FailedApplyIsCountedAndRetried(t *testing.T) {
	r, ag, _ := plannerFixture(t)
	ag.failMethod = "domain.create"
	ctx := context.Background()

	rep, _ := r.Run(ctx, RunNormal)
	if c := rep.Phases[PhaseDomainVhost.Name]; c.Failed != 1 || c.Applied != 0 {
		t.Fatalf("failed apply: %+v, want failed=1", c)
	}
	rep, _ = r.Run(ctx, RunNormal)
	if c := rep.Phases[PhaseDomainVhost.Name]; c.Failed != 1 || c.Skipped != 0 {
		t.Fatalf("a failed resource must be retried, not skipped: %+v", c)
	}
	if got := countMethod(ag, "domain.create"); got != 2 {
		t.Fatalf("domain.create attempts = %d, want 2", got)
	}
}

// The FTP pass used to keep its own cache and knew nothing of Force.
func TestReconcileFtpAccounts_ForceRunBypassesTheLedger(t *testing.T) {
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"ftpaccount.list":     hostListResult(t, []agentFtpListEntry{{Username: "shop_deploy"}}),
		"ftpaccount.list_all": hostListAllResult(t, []agentFtpListEntry{{Username: "shop_deploy"}}),
	}}
	rows := []models.FtpAccount{{
		ID: "a1", UserID: "u1", Username: "shop_deploy",
		HomePath: "/home/shop", SFTPAccess: true, IsEnabled: true,
	}}
	r := ftpTestReconciler(t, agent, rows, map[string]string{"u1": "shop"})

	r.reconcileFtpAccounts(context.Background())
	settled := len(agent.calls)
	r.reconcileFtpAccounts(context.Background())
	if len(agent.calls) != settled {
		t.Fatal("precondition: the steady-state normal pass is a no-op")
	}

	forceCtx, rr := withRun(context.Background(), RunForce)
	r.reconcileFtpAccounts(forceCtx)
	if countMethod(agent, "ftpaccount.sshd_sync") != 2 {
		t.Fatal("a force run must re-sync FTP accounts the ledger already holds")
	}
	if c := rr.report().Phases[PhaseFTPAccounts.Name]; c.Applied != 1 {
		t.Errorf("force FTP report: %+v", c)
	}
}

// The SSH-keys pass: steady state skips, a force run re-applies.
func TestReconcileSSHKeys_SteadyStateSkipsAndForceReapplies(t *testing.T) {
	uname := "shop"
	pkgID := "pkg1"
	users := &ftpUsersRepo{byID: map[string]*models.User{
		"u1": {ID: "u1", Username: &uname, PackageID: &pkgID},
	}}
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{}}
	r := New(nil, users, agent, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	r.WithSSHKeys(homeModeSSHKeysRepo{})
	r.WithPackages(&homeModePkgRepo{pkg: &models.HostingPackage{ID: pkgID, SSHEnabled: true}})

	if err := r.ReconcileSSHKeysForUser(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	settled := countMethod(agent, "ssh.user.set_shell")
	if err := r.ReconcileSSHKeysForUser(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if got := countMethod(agent, "ssh.user.set_shell"); got != settled {
		t.Fatalf("an unchanged user must be skipped (set_shell %d → %d)", settled, got)
	}

	forceCtx, rr := withRun(context.Background(), RunForce)
	if err := r.ReconcileSSHKeysForUser(forceCtx, "u1"); err != nil {
		t.Fatal(err)
	}
	if got := countMethod(agent, "ssh.user.set_shell"); got != settled+1 {
		t.Fatalf("a force run must re-apply the user's SSH state (set_shell %d → %d)", settled, got)
	}
	if c := rr.report().Phases[PhaseSSHKeys.Name]; c.Applied != 1 {
		t.Errorf("force SSH report: %+v", c)
	}
}
