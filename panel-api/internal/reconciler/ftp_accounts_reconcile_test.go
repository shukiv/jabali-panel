package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeFtpAccountRepo struct {
	repository.FtpAccountRepository
	rows []models.FtpAccount
}

func (f *fakeFtpAccountRepo) List(context.Context) ([]models.FtpAccount, error) {
	return f.rows, nil
}

type ftpUsersRepo struct {
	repository.UserRepository
	byID map[string]*models.User
}

func (f *ftpUsersRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

func ftpTestReconciler(t *testing.T, agent *fakeAgent, rows []models.FtpAccount, tenants map[string]string) *Reconciler {
	t.Helper()
	byID := map[string]*models.User{}
	for userID, uname := range tenants {
		name := uname
		byID[userID] = &models.User{ID: userID, Username: &name}
	}
	r := New(nil, &ftpUsersRepo{byID: byID}, agent, slog.Default(), Config{})
	r.WithFtpAccounts(&fakeFtpAccountRepo{rows: rows})
	return r
}

func ftpCallsByMethod(a *fakeAgent) map[string][]fakeCall {
	out := map[string][]fakeCall{}
	for _, c := range a.calls {
		out[c.method] = append(out[c.method], c)
	}
	return out
}

func hostListResult(t *testing.T, entries []agentFtpListEntry) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"accounts": entries})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReconcileFtpAccounts_RecreatesMissingAlias(t *testing.T) {
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"ftpaccount.list": hostListResult(t, nil), // host has nothing
	}}
	rows := []models.FtpAccount{{
		ID: "a1", UserID: "u1", Username: "shop_deploy",
		HomePath: "/home/shop/site", FTPAccess: true, SFTPAccess: true, IsEnabled: true,
	}}
	r := ftpTestReconciler(t, agent, rows, map[string]string{"u1": "shop"})

	r.reconcileFtpAccounts(context.Background())

	calls := ftpCallsByMethod(agent)
	creates := calls["ftpaccount.create"]
	if len(creates) != 1 {
		t.Fatalf("expected 1 create, got %d", len(creates))
	}
	params := creates[0].params.(map[string]any)
	if params["username"] != "shop_deploy" || params["tenant_username"] != "shop" {
		t.Fatalf("create params wrong: %v", params)
	}
	// The throwaway password must be present and high-entropy (64 hex),
	// never empty — and is unknowable, so the account can't log in until
	// a real password reset.
	pw, _ := params["password"].(string)
	if len(pw) != 64 {
		t.Fatalf("expected 64-char throwaway password, got %d chars", len(pw))
	}
	if len(calls["ftpaccount.sshd_sync"]) != 1 {
		t.Fatal("expected sshd_sync call")
	}
	if len(calls["ssh.user.home_chown"]) != 1 {
		t.Fatal("expected tenant home chroot-perms flip")
	}
}

func TestReconcileFtpAccounts_RemovesStrayAlias(t *testing.T) {
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"ftpaccount.list": hostListResult(t, []agentFtpListEntry{
			{Username: "shop_deploy", FTPAccess: false, Locked: false}, // desired
			{Username: "shop_old", FTPAccess: true, Locked: false},     // stray
		}),
	}}
	rows := []models.FtpAccount{{
		ID: "a1", UserID: "u1", Username: "shop_deploy",
		HomePath: "/home/shop", FTPAccess: false, SFTPAccess: true, IsEnabled: true,
	}}
	r := ftpTestReconciler(t, agent, rows, map[string]string{"u1": "shop"})

	r.reconcileFtpAccounts(context.Background())

	calls := ftpCallsByMethod(agent)
	if len(calls["ftpaccount.delete"]) != 1 {
		t.Fatalf("expected 1 stray delete, got %d", len(calls["ftpaccount.delete"]))
	}
	if calls["ftpaccount.delete"][0].params.(map[string]any)["username"] != "shop_old" {
		t.Fatal("deleted the wrong account")
	}
	if len(calls["ftpaccount.create"]) != 0 {
		t.Fatal("no creates expected — desired alias already present")
	}
}

func TestReconcileFtpAccounts_RepairsAccessDrift(t *testing.T) {
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"ftpaccount.list": hostListResult(t, []agentFtpListEntry{
			// host: ftp on + unlocked; DB wants ftp off + disabled(locked)
			{Username: "shop_deploy", FTPAccess: true, Locked: false},
		}),
	}}
	rows := []models.FtpAccount{{
		ID: "a1", UserID: "u1", Username: "shop_deploy",
		HomePath: "/home/shop", FTPAccess: false, SFTPAccess: true, IsEnabled: false,
	}}
	r := ftpTestReconciler(t, agent, rows, map[string]string{"u1": "shop"})

	r.reconcileFtpAccounts(context.Background())

	calls := ftpCallsByMethod(agent)
	if len(calls["ftpaccount.set_access"]) != 1 {
		t.Fatalf("expected 1 set_access, got %d", len(calls["ftpaccount.set_access"]))
	}
	p := calls["ftpaccount.set_access"][0].params.(map[string]any)
	if p["ftp_access"] != false || p["enabled"] != false {
		t.Fatalf("set_access params wrong: %v", p)
	}
}

func TestReconcileFtpAccounts_SSHDSyncPayloadShape(t *testing.T) {
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"ftpaccount.list": hostListResult(t, []agentFtpListEntry{
			{Username: "shop_deploy"},
			{Username: "shop_ftponly", FTPAccess: true},
			{Username: "shop_off", Locked: true},
		}),
	}}
	rows := []models.FtpAccount{
		// sftp+enabled → rendered, start dir = home relative to chroot
		{ID: "a1", UserID: "u1", Username: "shop_deploy", HomePath: "/home/shop/example.com/public_html", SFTPAccess: true, IsEnabled: true},
		// ftp-only → NOT in sshd render
		{ID: "a2", UserID: "u1", Username: "shop_ftponly", HomePath: "/home/shop", FTPAccess: true, SFTPAccess: false, IsEnabled: true},
		// disabled → NOT in sshd render
		{ID: "a3", UserID: "u1", Username: "shop_off", HomePath: "/home/shop", SFTPAccess: true, IsEnabled: false},
	}
	r := ftpTestReconciler(t, agent, rows, map[string]string{"u1": "shop"})

	r.reconcileFtpAccounts(context.Background())

	calls := ftpCallsByMethod(agent)
	if len(calls["ftpaccount.sshd_sync"]) != 1 {
		t.Fatal("expected one sshd_sync")
	}
	payload := calls["ftpaccount.sshd_sync"][0].params.(map[string]any)
	raw, _ := json.Marshal(payload["accounts"])
	var accounts []struct {
		Username  string `json:"username"`
		ChrootDir string `json:"chroot_dir"`
		StartDir  string `json:"start_dir"`
	}
	if err := json.Unmarshal(raw, &accounts); err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected exactly the sftp+enabled account rendered, got %d", len(accounts))
	}
	a := accounts[0]
	if a.Username != "shop_deploy" || a.ChrootDir != "/home/shop" || a.StartDir != "/example.com/public_html" {
		t.Fatalf("payload wrong: %+v", a)
	}
}

func TestReconcileFtpAccounts_HashGateSkipsSteadyState(t *testing.T) {
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"ftpaccount.list": hostListResult(t, []agentFtpListEntry{
			{Username: "shop_deploy"},
		}),
	}}
	rows := []models.FtpAccount{{
		ID: "a1", UserID: "u1", Username: "shop_deploy",
		HomePath: "/home/shop", SFTPAccess: true, IsEnabled: true,
	}}
	r := ftpTestReconciler(t, agent, rows, map[string]string{"u1": "shop"})

	r.reconcileFtpAccounts(context.Background())
	first := len(agent.calls)
	if first == 0 {
		t.Fatal("first pass must dispatch")
	}
	r.reconcileFtpAccounts(context.Background())
	if len(agent.calls) != first {
		t.Fatalf("steady-state second pass must be a no-op, got %d extra calls", len(agent.calls)-first)
	}
}

func TestReconcileFtpAccounts_FailureKeepsGateOpen(t *testing.T) {
	agent := &fakeAgent{
		failMethod: "ftpaccount.sshd_sync",
		resultByMethod: map[string]json.RawMessage{
			"ftpaccount.list": hostListResult(t, []agentFtpListEntry{{Username: "shop_deploy"}}),
		},
	}
	rows := []models.FtpAccount{{
		ID: "a1", UserID: "u1", Username: "shop_deploy",
		HomePath: "/home/shop", SFTPAccess: true, IsEnabled: true,
	}}
	r := ftpTestReconciler(t, agent, rows, map[string]string{"u1": "shop"})

	r.reconcileFtpAccounts(context.Background())
	if _, cached := r.ftpDispatchCache.Load("all"); cached {
		t.Fatal("failed pass must not cache the hash — drift would never heal")
	}
}

func TestReconcileFtpAccounts_NoRepoIsNoop(t *testing.T) {
	agent := &fakeAgent{}
	r := New(nil, &ftpUsersRepo{}, agent, slog.Default(), Config{Interval: time.Second})
	r.reconcileFtpAccounts(context.Background())
	if len(agent.calls) != 0 {
		t.Fatal("nil repo must be a silent no-op")
	}
}
