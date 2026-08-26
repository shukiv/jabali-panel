package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1229: the SSH reconciler converges jabali-ssh-forward group membership
// from the durable per-user opt-in flag (users.ssh_forwarding_enabled), so the
// opt-in survives user reprovision — unlike the v1 group-only opt-in, which
// reverted to OFF. Membership excludes the user from the JAB-352 forwarding
// lockdown Match block and into the loopback-only one (VS Code Remote-SSH).

func forwardGroupCalls(t *testing.T, sshEnabled, forwardingEnabled bool) map[string][]fakeCall {
	t.Helper()
	uname := "shop"
	pkgID := "pkg1"
	users := &ftpUsersRepo{byID: map[string]*models.User{
		"u1": {ID: "u1", Username: &uname, PackageID: &pkgID, SSHForwardingEnabled: forwardingEnabled},
	}}
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{}}
	r := New(nil, users, agent, slog.Default(), Config{})
	r.WithSSHKeys(homeModeSSHKeysRepo{})
	r.WithPackages(&homeModePkgRepo{pkg: &models.HostingPackage{ID: pkgID, SSHEnabled: sshEnabled}})

	if err := r.ReconcileSSHKeysForUser(context.Background(), "u1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return ftpCallsByMethod(agent)
}

func TestSSHForwardGroup_OptInJoins(t *testing.T) {
	calls := forwardGroupCalls(t, true, true)
	if len(calls["ssh.user.join_forward_group"]) != 1 || len(calls["ssh.user.leave_forward_group"]) != 0 {
		t.Fatalf("opted-in SSH user must JOIN jabali-ssh-forward exactly once; got join=%d leave=%d",
			len(calls["ssh.user.join_forward_group"]), len(calls["ssh.user.leave_forward_group"]))
	}
}

func TestSSHForwardGroup_OptOutLeaves(t *testing.T) {
	calls := forwardGroupCalls(t, true, false)
	if len(calls["ssh.user.leave_forward_group"]) != 1 || len(calls["ssh.user.join_forward_group"]) != 0 {
		t.Fatalf("SSH user without the opt-in must LEAVE jabali-ssh-forward; got join=%d leave=%d",
			len(calls["ssh.user.join_forward_group"]), len(calls["ssh.user.leave_forward_group"]))
	}
}

func TestSSHForwardGroup_NonSSHUserAlwaysLeaves(t *testing.T) {
	// Even with the flag set, a user whose package grants no SSH shell is
	// removed from the forward group (forwarding is meaningless without a shell).
	calls := forwardGroupCalls(t, false, true)
	if len(calls["ssh.user.leave_forward_group"]) != 1 || len(calls["ssh.user.join_forward_group"]) != 0 {
		t.Fatalf("non-SSH user must LEAVE the forward group regardless of the flag; got join=%d leave=%d",
			len(calls["ssh.user.join_forward_group"]), len(calls["ssh.user.leave_forward_group"]))
	}
}
