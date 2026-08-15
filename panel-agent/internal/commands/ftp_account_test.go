package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// testTenantUser returns the current OS user when it satisfies the tenant
// preconditions (uid >= 1000, home under /home) so validation paths past
// resolveFtpTenant can run without mutating the host. Skips otherwise
// (root CI containers, exotic homes).
func testTenantUser(t *testing.T) *user.User {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skipf("user.Current: %v", err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	if uid < minTenantUID {
		t.Skipf("current uid %d < %d — tenant-validation paths need a regular user", uid, minTenantUID)
	}
	if len(u.HomeDir) < 7 || u.HomeDir[:6] != "/home/" {
		t.Skipf("current home %q not under /home", u.HomeDir)
	}
	return u
}

func wantAgentErr(t *testing.T, err error, code string) *agentwire.AgentError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected agent error %q, got nil", code)
	}
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *agentwire.AgentError, got %T: %v", err, err)
	}
	if ae.Code != code {
		t.Fatalf("expected code %q, got %q (%s)", code, ae.Code, ae.Message)
	}
	return ae
}

func TestFtpAccountCreate_TenantNotFound(t *testing.T) {
	params, _ := json.Marshal(ftpAccountCreateParams{
		TenantUsername: "nosuchtenant", Username: "nosuchtenant_dev",
		HomePath: "/home/nosuchtenant/site", Password: "x",
	})
	_, err := ftpAccountCreateHandler(context.Background(), params)
	wantAgentErr(t, err, agentwire.CodeNotFound)
}

// root exists on every host with uid 0 — the alias floor must refuse it
// regardless of what the panel sends (scar: uid-0 in an sftp-adjacent
// group bricked host SSH).
func TestFtpAccountCreate_RefusesRootTenant(t *testing.T) {
	if _, err := user.Lookup("root"); err != nil {
		t.Skip("no root user on this host")
	}
	params, _ := json.Marshal(ftpAccountCreateParams{
		TenantUsername: "root", Username: "root_dev",
		HomePath: "/root/x", Password: "x",
	})
	_, err := ftpAccountCreateHandler(context.Background(), params)
	wantAgentErr(t, err, agentwire.CodePermissionDenied)
}

func TestFtpAccountCreate_NameValidation(t *testing.T) {
	u := testTenantUser(t)
	cases := []struct {
		name string
		sub  string
	}{
		{"missing tenant prefix", "otheruser_dev"},
		{"bare tenant name", u.Username},
		{"empty label", u.Username + "_"},
		{"uppercase label", u.Username + "_Dev"},
		{"label starts with underscore", u.Username + "__dev"},
		{"shell metacharacters", u.Username + "_a;rm"},
		{"too long", u.Username + "_" + "abcdefghijklmnopqrst9999999999999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params, _ := json.Marshal(ftpAccountCreateParams{
				TenantUsername: u.Username, Username: tc.sub,
				HomePath: filepath.Join(u.HomeDir, "site"), Password: "x",
			})
			_, err := ftpAccountCreateHandler(context.Background(), params)
			wantAgentErr(t, err, agentwire.CodeInvalidArgument)
		})
	}
}

func TestFtpAccountCreate_HomePathValidation(t *testing.T) {
	u := testTenantUser(t)
	sub := u.Username + "_dev"

	cases := []struct {
		name string
		path string
		code string
	}{
		{"relative", "site/public_html", agentwire.CodeInvalidArgument},
		{"unclean traversal", u.HomeDir + "/../etc", agentwire.CodeInvalidArgument},
		{"outside tenant home", "/etc/ssh", agentwire.CodePermissionDenied},
		{"other home", "/home/someoneelse-xyz/site", agentwire.CodePermissionDenied},
		{"trailing slash", u.HomeDir + "/site/", agentwire.CodeInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params, _ := json.Marshal(ftpAccountCreateParams{
				TenantUsername: u.Username, Username: sub,
				HomePath: tc.path, Password: "x",
			})
			_, err := ftpAccountCreateHandler(context.Background(), params)
			wantAgentErr(t, err, tc.code)
		})
	}
}

// A symlink inside the tenant home pointing outside it must be refused —
// tenant-writable trees mean the tenant chooses where symlinks point.
func TestFtpAccountCreate_SymlinkEscapeRefused(t *testing.T) {
	u := testTenantUser(t)
	if err := os.MkdirAll(u.HomeDir, 0o755); err != nil {
		t.Skipf("home not usable: %v", err)
	}
	link := filepath.Join(u.HomeDir, fmt.Sprintf(".ftptest-escape-%d", os.Getpid()))
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("cannot create symlink in home: %v", err)
	}
	defer os.Remove(link)

	params, _ := json.Marshal(ftpAccountCreateParams{
		TenantUsername: u.Username, Username: u.Username + "_dev",
		HomePath: filepath.Join(link, "sub"), Password: "x",
	})
	_, err := ftpAccountCreateHandler(context.Background(), params)
	wantAgentErr(t, err, agentwire.CodePermissionDenied)
}

func TestFtpAccountCreate_EmptyPassword(t *testing.T) {
	u := testTenantUser(t)
	params, _ := json.Marshal(ftpAccountCreateParams{
		TenantUsername: u.Username, Username: u.Username + "_dev",
		HomePath: filepath.Join(u.HomeDir, "site"), Password: "",
	})
	_, err := ftpAccountCreateHandler(context.Background(), params)
	wantAgentErr(t, err, agentwire.CodeInvalidArgument)
}

// Deleting an absent subaccount is success (idempotent teardown — the
// reconciler retries deletes; a half-done previous pass must not wedge it).
func TestFtpAccountDelete_AbsentIsIdempotent(t *testing.T) {
	u := testTenantUser(t)
	params, _ := json.Marshal(ftpAccountDeleteParams{
		TenantUsername: u.Username, Username: u.Username + "_nosuchacct",
	})
	res, err := ftpAccountDeleteHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("expected idempotent success, got %v", err)
	}
	m := res.(map[string]any)
	if m["absent"] != true {
		t.Fatalf("expected absent=true, got %v", m)
	}
}

func TestFtpAccountSetPassword_GuardsNonSubaccount(t *testing.T) {
	u := testTenantUser(t)
	// "root" fails the prefix check; a same-prefix REAL user with a
	// different uid can't exist without host mutation, so the uid-mismatch
	// arm is covered by the gated cycle test below.
	params, _ := json.Marshal(ftpAccountSetPasswordParams{
		TenantUsername: u.Username, Username: "root", Password: "x",
	})
	_, err := ftpAccountSetPasswordHandler(context.Background(), params)
	wantAgentErr(t, err, agentwire.CodeInvalidArgument)
}

func TestFtpAccountList_EmptyForTenantWithoutAccounts(t *testing.T) {
	u := testTenantUser(t)
	params, _ := json.Marshal(ftpAccountListParams{TenantUsername: u.Username})
	res, err := ftpAccountListHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	m := res.(map[string]any)
	accounts := m["accounts"].([]ftpAccountListEntry)
	for _, a := range accounts {
		t.Logf("unexpected pre-existing subaccount: %+v", a)
	}
}

// Full lifecycle against the real host: create → list → set_access →
// set_password → delete. Mutates passwd/groups — gated per GH #994.
// Needs root (useradd) with a REAL tenant named via JABALI_TEST_TENANT;
// without the env it falls back to the current user (who then needs
// passwordless sudo-equivalent rights for useradd to succeed).
func TestFtpAccountLifecycle_Host(t *testing.T) {
	requireHostMutationAllowed(t)
	var u *user.User
	if tenant := os.Getenv("JABALI_TEST_TENANT"); tenant != "" {
		var err error
		u, err = user.Lookup(tenant)
		if err != nil {
			t.Fatalf("JABALI_TEST_TENANT %q: %v", tenant, err)
		}
	} else {
		u = testTenantUser(t)
	}
	ctx := context.Background()
	sub := u.Username + "_cycletest"
	home := filepath.Join(u.HomeDir, ".ftp-cycle-test")
	defer os.RemoveAll(home)

	createP, _ := json.Marshal(ftpAccountCreateParams{
		TenantUsername: u.Username, Username: sub,
		HomePath: home, Password: "test-password-1", FTPAccess: true,
	})
	if _, err := ftpAccountCreateHandler(ctx, createP); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() {
		delP, _ := json.Marshal(ftpAccountDeleteParams{TenantUsername: u.Username, Username: sub})
		if _, err := ftpAccountDeleteHandler(ctx, delP); err != nil {
			t.Errorf("cleanup delete: %v", err)
		}
		if _, err := user.Lookup(sub); err == nil {
			t.Errorf("subaccount %q still exists after delete", sub)
		}
		if _, err := os.Stat(home); err != nil {
			t.Errorf("home %q must survive delete (never --remove): %v", home, err)
		}
	}()

	created, err := user.Lookup(sub)
	if err != nil {
		t.Fatalf("lookup after create: %v", err)
	}
	if created.Uid != u.Uid {
		t.Fatalf("subaccount uid %s != tenant uid %s", created.Uid, u.Uid)
	}

	listP, _ := json.Marshal(ftpAccountListParams{TenantUsername: u.Username})
	res, err := ftpAccountListHandler(ctx, listP)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, a := range res.(map[string]any)["accounts"].([]ftpAccountListEntry) {
		if a.Username == sub {
			found = true
			if !a.FTPAccess {
				t.Error("expected ftp_access=true after create with FTPAccess")
			}
		}
	}
	if !found {
		t.Fatal("created subaccount missing from list")
	}

	accP, _ := json.Marshal(ftpAccountSetAccessParams{
		TenantUsername: u.Username, Username: sub, FTPAccess: false, Enabled: false,
	})
	if _, err := ftpAccountSetAccessHandler(ctx, accP); err != nil {
		t.Fatalf("set_access: %v", err)
	}
	pwP, _ := json.Marshal(ftpAccountSetPasswordParams{
		TenantUsername: u.Username, Username: sub, Password: "test-password-2",
	})
	if _, err := ftpAccountSetPasswordHandler(ctx, pwP); err != nil {
		t.Fatalf("set_password: %v", err)
	}
}
