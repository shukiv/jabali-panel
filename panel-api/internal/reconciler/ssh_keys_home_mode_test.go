package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1053 vs M12: a shell-enabled tenant's home is normally flipped to
// user-owned (mode=ssh), but sshd refuses to chroot a subaccount into a
// non-root-owned /home/<u> — the two passes flipped ownership back and
// forth and subaccount logins died right after auth (live FileZilla
// repro). With any enabled SFTP subaccount, the sftp home layout must win.

type homeModeSSHKeysRepo struct {
	repository.SSHKeyRepository
}

func (homeModeSSHKeysRepo) ListByUserID(context.Context, string) ([]models.SSHKey, error) {
	return nil, nil
}

type homeModePkgRepo struct {
	repository.PackageRepository
	pkg *models.HostingPackage
}

func (f *homeModePkgRepo) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return f.pkg, nil
}

func homeModeCalls(t *testing.T, ftpRows []models.FtpAccount) map[string][]fakeCall {
	t.Helper()
	uname := "shop"
	pkgID := "pkg1"
	users := &ftpUsersRepo{byID: map[string]*models.User{
		"u1": {ID: "u1", Username: &uname, PackageID: &pkgID},
	}}
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{}}
	r := New(nil, users, agent, slog.Default(), Config{})
	r.WithSSHKeys(homeModeSSHKeysRepo{})
	r.WithPackages(&homeModePkgRepo{pkg: &models.HostingPackage{ID: pkgID, SSHEnabled: true}})
	r.WithFtpAccounts(&fakeFtpAccountRepo{rows: ftpRows})

	if err := r.ReconcileSSHKeysForUser(context.Background(), "u1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return ftpCallsByMethod(agent)
}

func TestSSHKeysHomeMode_FtpSubaccountForcesSftpLayout(t *testing.T) {
	calls := homeModeCalls(t, []models.FtpAccount{{
		ID: "a1", UserID: "u1", Username: "shop_deploy",
		HomePath: "/home/shop/site", SFTPAccess: true, IsEnabled: true,
	}})
	chowns := calls["ssh.user.home_chown"]
	if len(chowns) != 1 {
		t.Fatalf("expected 1 home_chown, got %d", len(chowns))
	}
	if mode := chowns[0].params.(map[string]interface{})["mode"]; mode != "sftp" {
		t.Fatalf("shell tenant WITH sftp subaccount must keep the root-owned chroot home, got mode=%v", mode)
	}
	// The tenant's own shell access is untouched — still leaves the
	// sftp group.
	if len(calls["ssh.user.leave_sftp_group"]) != 1 {
		t.Fatal("tenant must still leave jabali-sftp (shell stays enabled)")
	}
}

func TestSSHKeysHomeMode_NoSubaccountsKeepsShellLayout(t *testing.T) {
	calls := homeModeCalls(t, nil)
	chowns := calls["ssh.user.home_chown"]
	if len(chowns) != 1 {
		t.Fatalf("expected 1 home_chown, got %d", len(chowns))
	}
	if mode := chowns[0].params.(map[string]interface{})["mode"]; mode != "ssh" {
		t.Fatalf("shell tenant without subaccounts keeps the user-owned home, got mode=%v", mode)
	}
}

func TestSSHKeysHomeMode_DisabledSubaccountDoesNotForce(t *testing.T) {
	calls := homeModeCalls(t, []models.FtpAccount{{
		ID: "a1", UserID: "u1", Username: "shop_deploy",
		HomePath: "/home/shop/site", SFTPAccess: true, IsEnabled: false,
	}})
	if mode := calls["ssh.user.home_chown"][0].params.(map[string]interface{})["mode"]; mode != "ssh" {
		t.Fatalf("disabled subaccount must not force the sftp layout, got mode=%v", mode)
	}
}
