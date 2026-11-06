package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// TestSSHUserSetShellHandler_RefusesProtectedUsers guards GH #658: a protected
// system account (root, the service user) must never be chsh'd into the tenant
// sandbox shell — that's what locked the operator out of SSH after a
// mis-created "root" panel user (GH #429).
func TestSSHUserSetShellHandler_RefusesProtectedUsers(t *testing.T) {
	for _, u := range []string{"root", "jabali"} {
		params, _ := json.Marshal(map[string]string{
			"username": u,
			"shell":    "/usr/local/bin/jabali-ssh-shell",
		})
		_, err := sshUserSetShellHandler(context.Background(), params)
		if err == nil {
			t.Fatalf("user %q: expected refusal, got nil", u)
		}
		ae, ok := err.(*agentwire.AgentError)
		if !ok || ae.Code != agentwire.CodePermissionDenied {
			t.Fatalf("user %q: want PermissionDenied AgentError, got %v", u, err)
		}
	}
}

// setShellExec stubs execCommandContext: getent prints passwdRow, chsh
// succeeds. calls returns every command started.
func setShellExec(t *testing.T, passwdRow string) (calls func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		mu.Lock()
		got = append(got, name+" "+strings.Join(args, " "))
		mu.Unlock()
		if name == "getent" {
			return exec.CommandContext(ctx, "printf", "%s\n", passwdRow)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func wantSetShellRefusal(t *testing.T, username, shell string, code string) {
	t.Helper()
	calls := setShellExec(t, username+":x:1:1::/home/"+username+":/bin/bash")
	params, _ := json.Marshal(map[string]string{"username": username, "shell": shell})
	_, err := sshUserSetShellHandler(context.Background(), params)
	ae, ok := err.(*agentwire.AgentError)
	if !ok || ae.Code != code {
		t.Fatalf("%q %q: want %s, got %v", username, shell, code, err)
	}
	if c := calls(); len(c) != 0 {
		t.Fatalf("%q %q: refused call must run nothing, ran %q", username, shell, c)
	}
}

// The tenant sandbox wrapper is the only shell the panel sets. Any other
// shell, or a username that isn't a plain account name, is refused before
// getent or chsh runs.
func TestSSHUserSetShellHandler_RefusesBadInput(t *testing.T) {
	for _, sh := range []string{
		"/bin/bash",
		"/bin/sh",
		"/usr/sbin/nologin",
		"/usr/local/bin/jabali-ssh-shell/../../../bin/bash",
		"/usr/local/bin/jabali-ssh-shell ",
	} {
		wantSetShellRefusal(t, "alice", sh, agentwire.CodeInvalidArgument)
	}
	for _, u := range []string{"-s", "--help", "a b", "../alice", "Alice", "alice:x", strings.Repeat("a", 33)} {
		wantSetShellRefusal(t, u, sshTenantShell, agentwire.CodeInvalidArgument)
	}
}

// A system account (uid below the tenant range, or nobody) keeps its shell.
func TestSSHUserSetShellHandler_RefusesSystemAccounts(t *testing.T) {
	checked := 0
	for _, u := range []string{"daemon", "www-data", "nobody"} {
		if _, err := user.Lookup(u); err != nil {
			t.Logf("no %s account here: %v", u, err)
			continue
		}
		wantSetShellRefusal(t, u, sshTenantShell, agentwire.CodePermissionDenied)
		checked++
	}
	if checked == 0 {
		t.Skip("none of the system accounts exist here")
	}
}

func TestSSHShellAccount(t *testing.T) {
	for _, tc := range []struct {
		uid, gecos string
		ok         bool
	}{
		{"1001", "", true},
		{"1001", "Alice Example,,,", true},
		{"59999", "", true},
		{"0", "", false},
		{"33", "", false},
		{"999", "", false},
		{"60000", "", false},
		{"65534", "", false},
		{"x", "", false},
		{"1001", ftpAliasGecos, false},
		{"1001", ftpAliasGecosFor("alice"), false},
		{"1002", ftpAliasGecosFor("alice") + ",,,", false},
	} {
		err := sshShellAccount(&user.User{Username: "alice_ftp", Uid: tc.uid, Name: tc.gecos})
		if (err == nil) != tc.ok {
			t.Errorf("uid %s gecos %q: ok=%v, err=%v", tc.uid, tc.gecos, tc.ok, err)
		}
		if err != nil && err.Code != agentwire.CodePermissionDenied {
			t.Errorf("uid %s gecos %q: want PermissionDenied, got %s", tc.uid, tc.gecos, err.Code)
		}
	}
}

// A hosting user still gets the wrapper.
func TestSSHUserSetShellHandler_SetsTheWrapperForATenant(t *testing.T) {
	tenant := &user.User{Username: "alice", Uid: "1001", Gid: "1001", HomeDir: "/home/alice"}
	prev := lookupShellUser
	lookupShellUser = func(name string) (*user.User, error) {
		if name != tenant.Username {
			return nil, user.UnknownUserError(name)
		}
		return tenant, nil
	}
	t.Cleanup(func() { lookupShellUser = prev })
	calls := setShellExec(t, "alice:x:1001:1001::/home/alice:/bin/bash")
	params, _ := json.Marshal(map[string]string{"username": "alice", "shell": sshTenantShell})
	res, err := sshUserSetShellHandler(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	r := res.(*sshUserSetShellResponse)
	if !r.Changed || r.WasShell != "/bin/bash" {
		t.Fatalf("response %+v", r)
	}
	c := calls()
	if len(c) != 2 || c[0] != "getent passwd alice" || c[1] != "chsh -s "+sshTenantShell+" alice" {
		t.Fatalf("calls %q", c)
	}

	// An FTP alias of that tenant, same uid, keeps its shell.
	tenant = &user.User{Username: "alice", Uid: "1001", Name: ftpAliasGecosFor("bob")}
	calls = setShellExec(t, "alice:x:1001:1001::/home/alice:/usr/sbin/nologin")
	if _, err := sshUserSetShellHandler(context.Background(), params); err == nil {
		t.Fatal("an FTP alias must be refused")
	}
	if c := calls(); len(c) != 0 {
		t.Fatalf("a refused alias must run nothing, ran %q", c)
	}
}
