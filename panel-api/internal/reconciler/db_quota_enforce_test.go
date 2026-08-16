package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type dbqUsersRepo struct {
	repository.UserRepository
	rows []models.User
}

func (f *dbqUsersRepo) List(context.Context, repository.ListOptions) ([]models.User, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

type dbqPkgRepo struct {
	repository.PackageRepository
	pkg *models.HostingPackage
}

func (f *dbqPkgRepo) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return f.pkg, nil
}

type dbqDBRepo struct {
	repository.DatabaseRepository
	rows []models.Database
}

func (f *dbqDBRepo) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Database, int64, error) {
	out := []models.Database{}
	for _, d := range f.rows {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, int64(len(out)), nil
}

type dbqDBUserRepo struct {
	repository.DatabaseUserRepository
	byID map[string]*models.DatabaseUser
}

func (f *dbqDBUserRepo) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

type dbqGrantRepo struct {
	repository.DatabaseUserGrantRepository
	byDB map[string][]models.DatabaseUserGrant
}

func (f *dbqGrantRepo) ListByDatabaseID(_ context.Context, dbID string) ([]models.DatabaseUserGrant, error) {
	return f.byDB[dbID], nil
}

// dbqReconciler builds the sweep harness: one tenant (u1, package quota
// 100 MiB) with one mariadb database db1 owned by db-user alice (rw).
func dbqReconciler(t *testing.T, ag *fakeAgent, usedBytes int64) *Reconciler {
	t.Helper()
	pid := "p1"
	ag.resultByMethod = map[string]json.RawMessage{
		"db.usage.by_schema": json.RawMessage(
			`{"schemas":[{"schema":"tenant_db","bytes":` + jsonInt(usedBytes) + `}]}`),
		"db_user.revoke": json.RawMessage(`{}`),
		"db_user.grant":  json.RawMessage(`{}`),
	}
	r := New(nil, &dbqUsersRepo{rows: []models.User{{ID: "u1", PackageID: &pid}}}, ag, slog.Default(), Config{})
	r.WithPackages(&dbqPkgRepo{pkg: &models.HostingPackage{ID: "p1", DiskQuotaMB: 100}})
	r.WithDBQuotaEnforce(
		&dbqDBRepo{rows: []models.Database{{ID: "d1", UserID: "u1", Name: "tenant_db", Engine: "mariadb"}}},
		&dbqDBUserRepo{byID: map[string]*models.DatabaseUser{"dbu1": {ID: "dbu1", UserID: "u1", Username: "alice", Engine: "mariadb"}}},
		&dbqGrantRepo{byDB: map[string][]models.DatabaseUserGrant{"d1": {{ID: "g1", DatabaseID: "d1", DatabaseUserID: "dbu1", GrantLevel: "rw", Privileges: "ALL"}}}},
	)
	return r
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func dbqCalls(a *fakeAgent, method string) int {
	n := 0
	for _, c := range a.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

// JAB-243: at/over quota → write privileges revoked, SELECT-preserving set.
func TestDBQuota_OverQuotaFreezesWrites(t *testing.T) {
	ag := &fakeAgent{}
	r := dbqReconciler(t, ag, 150<<20) // 150 MiB used, 100 MiB quota
	r.reconcileDBQuotaEnforce(context.Background())
	if n := dbqCalls(ag, "db_user.revoke"); n != 1 {
		t.Fatalf("revoke calls = %d, want 1", n)
	}
	if n := dbqCalls(ag, "db_user.grant"); n != 0 {
		t.Fatalf("grant calls = %d, want 0 on freeze", n)
	}
	for _, c := range ag.calls {
		if c.method != "db_user.revoke" {
			continue
		}
		p := c.params.(map[string]any)
		privs := p["privileges"].([]string)
		for _, pr := range privs {
			if pr == "SELECT" || pr == "DELETE" || pr == "DROP" {
				t.Fatalf("freeze revoked %s — tenant must keep read + free-space privileges", pr)
			}
		}
	}
}

// JAB-243: under 90% after a freeze → the grant row's own level is
// restored; healthy steady state never re-grants.
func TestDBQuota_RestoreOnlyAfterFreeze(t *testing.T) {
	ag := &fakeAgent{}
	r := dbqReconciler(t, ag, 150<<20)
	r.reconcileDBQuotaEnforce(context.Background())

	// Drop usage under 90% and force the interval gate open.
	ag.resultByMethod["db.usage.by_schema"] = json.RawMessage(`{"schemas":[{"schema":"tenant_db","bytes":10}]}`)
	r.dbQuotaEnforceLastRun = r.dbQuotaEnforceLastRun.Add(-2 * dbQuotaEnforceInterval)
	r.reconcileDBQuotaEnforce(context.Background())

	if n := dbqCalls(ag, "db_user.grant"); n != 1 {
		t.Fatalf("grant calls after recovery = %d, want 1", n)
	}
	// Healthy tenant on the next sweep: no further grant traffic.
	r.dbQuotaEnforceLastRun = r.dbQuotaEnforceLastRun.Add(-2 * dbQuotaEnforceInterval)
	r.reconcileDBQuotaEnforce(context.Background())
	if n := dbqCalls(ag, "db_user.grant"); n != 1 {
		t.Fatalf("steady-state re-grant detected (calls=%d)", n)
	}
}

// JAB-243 hysteresis: between 90% and 100% nothing changes in either
// direction.
func TestDBQuota_HysteresisHoldsState(t *testing.T) {
	ag := &fakeAgent{}
	r := dbqReconciler(t, ag, 95<<20) // 95% — never frozen, never restored
	r.reconcileDBQuotaEnforce(context.Background())
	if len(ag.calls) != 1 { // only the usage query
		t.Fatalf("agent calls at 95%% = %v, want usage query only", ag.calls)
	}
	// Frozen tenant at 95%: stays frozen (no restore below-100 flap).
	ag2 := &fakeAgent{}
	r2 := dbqReconciler(t, ag2, 150<<20)
	r2.reconcileDBQuotaEnforce(context.Background())
	ag2.resultByMethod["db.usage.by_schema"] = json.RawMessage(
		`{"schemas":[{"schema":"tenant_db","bytes":` + jsonInt(95<<20) + `}]}`)
	r2.dbQuotaEnforceLastRun = r2.dbQuotaEnforceLastRun.Add(-2 * dbQuotaEnforceInterval)
	r2.reconcileDBQuotaEnforce(context.Background())
	if n := dbqCalls(ag2, "db_user.grant"); n != 0 {
		t.Fatalf("95%% restored a frozen tenant — hysteresis broken (grants=%d)", n)
	}
}

// JAB-243: postgres databases are excluded from both the sum and the
// grant convergence.
func TestDBQuota_PostgresExcluded(t *testing.T) {
	ag := &fakeAgent{}
	pid := "p1"
	ag.resultByMethod = map[string]json.RawMessage{
		"db.usage.by_schema": json.RawMessage(`{"schemas":[]}`),
	}
	r := New(nil, &dbqUsersRepo{rows: []models.User{{ID: "u1", PackageID: &pid}}}, ag, slog.Default(), Config{})
	r.WithPackages(&dbqPkgRepo{pkg: &models.HostingPackage{ID: "p1", DiskQuotaMB: 100}})
	r.WithDBQuotaEnforce(
		&dbqDBRepo{rows: []models.Database{{ID: "d1", UserID: "u1", Name: "pg_db", Engine: "postgres"}}},
		&dbqDBUserRepo{byID: map[string]*models.DatabaseUser{}},
		&dbqGrantRepo{byDB: map[string][]models.DatabaseUserGrant{}},
	)
	r.reconcileDBQuotaEnforce(context.Background())
	if len(ag.calls) != 1 {
		t.Fatalf("pg-only tenant caused grant traffic: %v", ag.calls)
	}
}
