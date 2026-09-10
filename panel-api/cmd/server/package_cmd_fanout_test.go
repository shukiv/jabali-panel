package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes: the two narrow interfaces markPackagePHPPoolsPending accepts ---

type fakePkgUserLister struct {
	users   []models.User
	listErr error
}

func (f *fakePkgUserLister) List(ctx context.Context, opts repository.ListOptions) ([]models.User, int64, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.users, int64(len(f.users)), nil
}

type setStatusCall struct {
	poolID string
	status string
}

type fakePoolMarker struct {
	poolsByUser map[string][]models.PHPPool
	listErrUser string // ListByUserID returns an error for this user id
	setErrPool  string // SetStatus returns an error for this pool id
	calls       []setStatusCall
}

func (f *fakePoolMarker) ListByUserID(ctx context.Context, userID string) ([]models.PHPPool, error) {
	if f.listErrUser != "" && userID == f.listErrUser {
		return nil, errors.New("boom list pools")
	}
	return f.poolsByUser[userID], nil
}

func (f *fakePoolMarker) SetStatus(ctx context.Context, id, status string, lastErr *string) error {
	if f.setErrPool != "" && id == f.setErrPool {
		return errors.New("boom set status")
	}
	f.calls = append(f.calls, setStatusCall{poolID: id, status: status})
	return nil
}

// Only serving pools (active or ready) of users ON the package are flipped;
// pending/error pools and users on other packages (or no package) are left
// alone.
func TestMarkPackagePHPPoolsPending_OnlyPackageUsersServingPools(t *testing.T) {
	pkgA := "pkgAAAAAAAAAAAAAAAAAAAAAAAA"
	pkgB := "pkgBBBBBBBBBBBBBBBBBBBBBBBB"
	users := &fakePkgUserLister{users: []models.User{
		{ID: "u1", PackageID: &pkgA},
		{ID: "u2", PackageID: &pkgA},
		{ID: "u3", PackageID: &pkgB}, // different package — untouched
		{ID: "u4", PackageID: nil},   // no package — untouched
	}}
	pools := &fakePoolMarker{poolsByUser: map[string][]models.PHPPool{
		"u1": {
			{ID: "p1a", UserID: "u1", Status: "active"},
			{ID: "p1b", UserID: "u1", Status: "ready"},
			{ID: "p1c", UserID: "u1", Status: "pending"}, // already in sweep set
			{ID: "p1d", UserID: "u1", Status: "error"},   // preserve LastError
		},
		"u2": {
			{ID: "p2a", UserID: "u2", Status: "ready"},
		},
		"u3": {
			{ID: "p3a", UserID: "u3", Status: "active"}, // other package
		},
	}}

	n, err := markPackagePHPPoolsPending(context.Background(), users, pools, pkgA)
	require.NoError(t, err)
	require.Equal(t, 3, n) // p1a, p1b, p2a

	got := map[string]bool{}
	for _, c := range pools.calls {
		require.Equal(t, "pending", c.status)
		got[c.poolID] = true
	}
	require.Equal(t, map[string]bool{"p1a": true, "p1b": true, "p2a": true}, got)
	require.NotContains(t, got, "p1c") // pending untouched
	require.NotContains(t, got, "p1d") // error untouched
	require.NotContains(t, got, "p3a") // other package untouched
}

// A pool a tenant left "ready" via a settings-save is still flipped — the sweep
// re-applies only pending/error, so without this a php_exec tighten never
// reaches it. Guards the active-OR-ready filter (falsify: drop "ready").
func TestMarkPackagePHPPoolsPending_ReadyPoolFlipped(t *testing.T) {
	pkg := "pkg00000000000000000000000000"
	users := &fakePkgUserLister{users: []models.User{{ID: "u1", PackageID: &pkg}}}
	pools := &fakePoolMarker{poolsByUser: map[string][]models.PHPPool{
		"u1": {{ID: "ponly", UserID: "u1", Status: "ready"}},
	}}

	n, err := markPackagePHPPoolsPending(context.Background(), users, pools, pkg)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Len(t, pools.calls, 1)
	require.Equal(t, "ponly", pools.calls[0].poolID)
}

// A failed flip on one pool surfaces as an error but must not skip the others,
// and the count reflects the flips that landed.
func TestMarkPackagePHPPoolsPending_PartialFailureJoinsErrors(t *testing.T) {
	pkg := "pkg11111111111111111111111111"
	users := &fakePkgUserLister{users: []models.User{
		{ID: "u1", PackageID: &pkg},
		{ID: "u2", PackageID: &pkg},
	}}
	pools := &fakePoolMarker{
		poolsByUser: map[string][]models.PHPPool{
			"u1": {{ID: "bad", UserID: "u1", Status: "active"}},
			"u2": {{ID: "good", UserID: "u2", Status: "active"}},
		},
		setErrPool: "bad",
	}

	n, err := markPackagePHPPoolsPending(context.Background(), users, pools, pkg)
	require.Error(t, err)
	require.Equal(t, 1, n)
	require.Len(t, pools.calls, 1)
	require.Equal(t, "good", pools.calls[0].poolID)
}

// A list-pools failure for one user is collected but the others still process.
func TestMarkPackagePHPPoolsPending_ListPoolsErrorForOneUserContinues(t *testing.T) {
	pkg := "pkg22222222222222222222222222"
	users := &fakePkgUserLister{users: []models.User{
		{ID: "u1", PackageID: &pkg},
		{ID: "u2", PackageID: &pkg},
	}}
	pools := &fakePoolMarker{
		poolsByUser: map[string][]models.PHPPool{
			"u2": {{ID: "p2", UserID: "u2", Status: "active"}},
		},
		listErrUser: "u1",
	}

	n, err := markPackagePHPPoolsPending(context.Background(), users, pools, pkg)
	require.Error(t, err)
	require.Equal(t, 1, n)
	require.Len(t, pools.calls, 1)
	require.Equal(t, "p2", pools.calls[0].poolID)
}

// A user-list failure fans out nothing.
func TestMarkPackagePHPPoolsPending_ListUsersErrorNoWrites(t *testing.T) {
	users := &fakePkgUserLister{listErr: errors.New("db down")}
	pools := &fakePoolMarker{poolsByUser: map[string][]models.PHPPool{}}

	n, err := markPackagePHPPoolsPending(context.Background(), users, pools, "pkg")
	require.Error(t, err)
	require.Equal(t, 0, n)
	require.Empty(t, pools.calls)
}

// Source contract for the edit-command fan-out: the php_exec change is captured
// before the flags mutate the row (value gate), and the fan-out runs strictly
// after persistence (AC5: a failed Update fans out nothing). These orderings are
// load-bearing and easy to break in a refactor, so pin them in source.
func TestPackageEdit_PHPExecFanoutSourceContract(t *testing.T) {
	src, err := os.ReadFile("package_cmd.go")
	require.NoError(t, err)
	s := string(src)

	iPrev := strings.Index(s, "prevPHPExec := p.PHPExecEnabled")
	iApply := strings.Index(s, "applyPackageEditFlags(cmd.Flags().Changed")
	iUpdate := strings.Index(s, "repo.Update(ctx, p)")
	iGate := strings.Index(s, "if p.PHPExecEnabled != prevPHPExec {")
	iFanout := strings.Index(s, "markPackagePHPPoolsPending(fanCtx")

	require.Greater(t, iPrev, 0, "edit must capture the pre-edit php_exec state")
	require.Greater(t, iApply, 0)
	require.Greater(t, iUpdate, 0)
	require.Greater(t, iGate, 0, "fan-out must be value-gated on a php_exec change")
	require.Greater(t, iFanout, 0, "edit must fan out a php_exec change")

	require.Less(t, iPrev, iApply, "prevPHPExec must be read before applyPackageEditFlags mutates the row")
	require.Less(t, iUpdate, iFanout, "fan-out must run strictly after repo.Update (AC5)")
	require.Less(t, iGate, iFanout, "the value gate must guard the fan-out")
}
