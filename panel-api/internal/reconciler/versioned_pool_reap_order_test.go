package reconciler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Moving a domain off a versioned PHP pool only changes the DB: its vhost keeps
// pointing at the old pool's socket until the domain loop re-renders it. The
// orphan pool used to be reaped before the loop, so the site returned 502 until
// its vhost caught up (61 domains moved 8.1/8.0 → 8.4 at once: 34 sites 502).

func versionedReapFixture(t *testing.T) (*Reconciler, *fakeAgent, *fakeDomainRepo, *fakePHPPoolRepo) {
	t.Helper()
	agent := &fakeAgent{}
	domains := &fakeDomainRepo{domains: map[string]*models.Domain{}}
	users := &fakeUserRepo{users: map[string]*models.User{}}
	pools := &fakePHPPoolRepo{pools: map[string]*models.PHPPool{}}
	username := "alice"
	users.users["user-1"] = &models.User{ID: "user-1", Email: "alice@example.com", Username: &username}
	base := time.Now().UTC().Add(-time.Hour) // both pools are past the reap grace
	for id, v := range map[string]string{"pool-default": "8.4", "pool-81": "8.1"} {
		created := base
		if id == "pool-81" {
			created = base.Add(time.Minute) // the default pool is the user's earliest
		}
		pools.pools[id] = &models.PHPPool{ID: id, UserID: "user-1", PHPVersion: v, PmMode: "ondemand",
			PmMaxChildren: 20, ProcessIdleTimeoutSeconds: 60, Status: "active", CreatedAt: created}
	}
	p81 := "pool-81"
	domains.domains["domain-1"] = &models.Domain{OwnershipState: verifiedOwnership, ID: "domain-1", UserID: "user-1",
		Name: "example.com", DocRoot: "/home/alice/domains/example.com/public_html", IsEnabled: true,
		PHPPoolID: &p81, CreatedAt: base, UpdatedAt: base}
	r := New(domains, users, agent, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithPHPPools(pools)
	r.socketReady = func(context.Context, string, time.Duration, time.Duration) bool { return true }
	return r, agent, domains, pools
}

func moveToDefaultPool(d *fakeDomainRepo) {
	def := "pool-default"
	d.domains["domain-1"].PHPPoolID = &def
}

func firstCallIndex(ag *fakeAgent, method string) int {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	for i, c := range ag.calls {
		if c.method == method {
			return i
		}
	}
	return -1
}

// nginx's reload returns before its old workers finish: on .13 they served two
// requests on the old config after the vhost write, and both hit the removed
// pool. So the pool must outlive the tick in which its last vhost moves off it.
func TestReconcileAll_VersionedPoolNotRemovedInTheTickItsVhostMoves(t *testing.T) {
	r, agent, domains, pools := versionedReapFixture(t)
	ctx := context.Background()
	require.NoError(t, r.ReconcileAll(ctx)) // the vhost is rendered on 8.1
	require.Zero(t, countMethod(agent, "php.pool.remove"), "a pool with a bound domain is never reaped")

	moveToDefaultPool(domains) // what `jabali domain php-version set` does
	agent.set(func(f *fakeAgent) { f.calls = nil })
	require.NoError(t, r.ReconcileAll(ctx))

	require.GreaterOrEqual(t, firstCallIndex(agent, "domain.create"), 0, "the moved domain's vhost must be re-rendered")
	require.Zero(t, countMethod(agent, "php.pool.remove"),
		"old nginx workers may still send requests to the 8.1 pool right after the reload")
	_, err := pools.FindByID(ctx, "pool-81")
	require.NoError(t, err)

	require.NoError(t, r.ReconcileAll(ctx))
	require.Zero(t, countMethod(agent, "php.pool.remove"), "the drain has not elapsed")

	backdateReleasable(r, "pool-81")
	require.NoError(t, r.ReconcileAll(ctx))
	require.Equal(t, 1, countMethod(agent, "php.pool.remove"), "after the drain the orphan is reaped")
	_, err = pools.FindByID(ctx, "pool-81")
	require.ErrorIs(t, err, repository.ErrNotFound)
}

// backdateReleasable moves a pool's drain start an hour back, past any drain.
func backdateReleasable(r *Reconciler, poolID string) {
	r.releasableMu.Lock()
	defer r.releasableMu.Unlock()
	if _, ok := r.releasableSince[poolID]; ok {
		r.releasableSince[poolID] = time.Now().Add(-time.Hour)
	}
}

func releasableStamped(r *Reconciler, poolID string) bool {
	r.releasableMu.Lock()
	defer r.releasableMu.Unlock()
	_, ok := r.releasableSince[poolID]
	return ok
}

// A pool rebound during its drain and orphaned again starts a fresh drain.
func TestReconcileAll_VersionedPoolRebindRestartsDrain(t *testing.T) {
	r, agent, domains, _ := versionedReapFixture(t)
	ctx := context.Background()
	require.NoError(t, r.ReconcileAll(ctx))
	moveToDefaultPool(domains)
	require.NoError(t, r.ReconcileAll(ctx))
	backdateReleasable(r, "pool-81")

	p81 := "pool-81"
	domains.domains["domain-1"].PHPPoolID = &p81 // back to 8.1
	require.NoError(t, r.ReconcileAll(ctx))
	require.False(t, releasableStamped(r, "pool-81"), "a bound pool is not releasable")

	moveToDefaultPool(domains)
	require.NoError(t, r.ReconcileAll(ctx))
	require.Zero(t, countMethod(agent, "php.pool.remove"), "the old drain start must not carry over")
}

func TestReconcileAll_VersionedPoolKeptWhileVhostWriteFails(t *testing.T) {
	r, agent, domains, pools := versionedReapFixture(t)
	ctx := context.Background()
	require.NoError(t, r.ReconcileAll(ctx)) // the vhost is rendered on 8.1

	moveToDefaultPool(domains)
	agent.set(func(f *fakeAgent) { f.calls = nil; f.failMethod = "domain.create" })
	require.NoError(t, r.ReconcileAll(ctx))
	require.Zero(t, countMethod(agent, "php.pool.remove"),
		"the vhost on disk still uses the 8.1 socket, so the 8.1 pool must stay")
	require.False(t, releasableStamped(r, "pool-81"), "a pool a vhost uses does not start its drain")
	_, err := pools.FindByID(ctx, "pool-81")
	require.NoError(t, err)

	agent.set(func(f *fakeAgent) { f.calls = nil; f.failMethod = "" })
	require.NoError(t, r.ReconcileAll(ctx)) // the vhost moves; the drain starts
	require.Zero(t, countMethod(agent, "php.pool.remove"))
	backdateReleasable(r, "pool-81")
	require.NoError(t, r.ReconcileAll(ctx))
	require.Equal(t, 1, countMethod(agent, "php.pool.remove"), "once the vhost moved and drained, the orphan is reaped")
}
