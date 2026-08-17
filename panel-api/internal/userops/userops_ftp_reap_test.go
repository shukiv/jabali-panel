package userops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeReapFtpRepo returns a fixed account set and records Delete calls; the
// embedded interface panics on any other method (scope guard).
type fakeReapFtpRepo struct {
	repository.FtpAccountRepository
	accts      []models.FtpAccount
	deletedIDs []string
	delErr     error
}

func (f *fakeReapFtpRepo) ListByUserID(_ context.Context, _ string) ([]models.FtpAccount, error) {
	return f.accts, nil
}

func (f *fakeReapFtpRepo) Delete(_ context.Context, id string) error {
	f.deletedIDs = append(f.deletedIDs, id)
	return f.delErr
}

func countFtpDeletes(ag *recordingAgent) int {
	n := 0
	for _, c := range ag.calls {
		if c.method == "ftpaccount.delete" {
			n++
		}
	}
	return n
}

// JAB-265: a tenant delete must reap every FTP subaccount — dispatch the agent
// teardown (userdel + jail) AND delete the row — so no live Unix credential or
// jail is orphaned.
func TestReapTenantFtpAccounts(t *testing.T) {
	repo := &fakeReapFtpRepo{accts: []models.FtpAccount{
		{ID: "a1", Username: "alice_deploy"},
		{ID: "a2", Username: "alice_printer"},
	}}
	ag := &recordingAgent{}
	reapTenantFtpAccounts(context.Background(), Deps{Agent: ag}, DeleteDeps{FtpAccounts: repo}, "u1", "alice")

	if n := countFtpDeletes(ag); n != 2 {
		t.Fatalf("ftpaccount.delete calls = %d, want 2", n)
	}
	// The tenant username is threaded so the agent can resolve the tenant
	// (which must still exist at reap time).
	if p, ok := ag.calls[0].params.(map[string]any); !ok || p["tenant_username"] != "alice" {
		t.Fatalf("first call params = %v, want tenant_username=alice", ag.calls[0].params)
	}
	if len(repo.deletedIDs) != 2 || repo.deletedIDs[0] != "a1" || repo.deletedIDs[1] != "a2" {
		t.Fatalf("deleted rows = %v, want [a1 a2]", repo.deletedIDs)
	}
}

// The row MUST be deleted even when the agent teardown fails, so the rowless
// alias falls to the stray-alias reaper instead of persisting as a live orphan
// the reaper can't see (it keys on a MISSING row) — the JAB-265 core.
func TestReapTenantFtpAccounts_RowDeletedDespiteAgentFailure(t *testing.T) {
	repo := &fakeReapFtpRepo{accts: []models.FtpAccount{{ID: "a1", Username: "alice_deploy"}}}
	ag := &recordingAgent{retErr: errors.New("agent down")}
	reapTenantFtpAccounts(context.Background(), Deps{Agent: ag}, DeleteDeps{FtpAccounts: repo}, "u1", "alice")

	if len(repo.deletedIDs) != 1 {
		t.Fatalf("row must be deleted even when the agent teardown fails; deletedIDs = %v", repo.deletedIDs)
	}
}

// nil repo (unwired) and empty username (admin, no OS user) are safe no-ops.
func TestReapTenantFtpAccounts_Guards(t *testing.T) {
	ag := &recordingAgent{}
	reapTenantFtpAccounts(context.Background(), Deps{Agent: ag}, DeleteDeps{}, "u1", "alice")
	reapTenantFtpAccounts(context.Background(), Deps{Agent: ag}, DeleteDeps{FtpAccounts: &fakeReapFtpRepo{}}, "u1", "")
	if countFtpDeletes(ag) != 0 {
		t.Fatal("guarded no-op cases must not dispatch")
	}
}
