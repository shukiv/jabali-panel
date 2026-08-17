package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func xferEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "jabali-xfer.conf")
	t.Setenv("JABALI_SSHD_XFER_DROPIN_PATH", path)
	t.Setenv("JABALI_SSHD_TEST_SKIP_VALIDATE", "1")
	t.Setenv("JABALI_SSHD_TEST_SKIP_RELOAD", "1")
	return path
}

func TestRenderXferDropin_DeterministicAndComplete(t *testing.T) {
	accounts := []ftpSSHDSyncAccount{
		{Username: "zeta_dev", ChrootDir: "/home/zeta", StartDir: "/site"},
		{Username: "alpha_dev", ChrootDir: "/home/alpha", StartDir: "/"},
	}
	out := renderXferDropin(accounts)

	// Sorted by username regardless of input order.
	if strings.Index(out, "Match User alpha_dev") > strings.Index(out, "Match User zeta_dev") {
		t.Fatal("blocks not sorted by username")
	}
	for _, want := range []string{
		"Match User alpha_dev",
		// JAB-264: -u 0007 strips OTHER bits from uploads.
		"ForceCommand internal-sftp -u 0007 -d /",
		"ChrootDirectory /home/alpha",
		"Match User zeta_dev",
		"ForceCommand internal-sftp -u 0007 -d /site",
		"ChrootDirectory /home/zeta",
		"AllowTcpForwarding no",
		"PasswordAuthentication yes",
		// Load-bearing: without an explicit no, sshd falls back to the
		// global default (yes) and reads authorized_keys from the alias's
		// REAL home — a tenant-writable docroot — before chroot. That is
		// a password-rotation-proof backdoor.
		"PubkeyAuthentication no",
		"KbdInteractiveAuthentication no",
		"PermitEmptyPasswords no",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q", want)
		}
	}
	// Identical input → identical output (change-gate depends on it).
	if out != renderXferDropin(accounts) {
		t.Fatal("render not deterministic")
	}
}

func TestFtpSSHDSync_WritesAndReportsChange(t *testing.T) {
	path := xferEnv(t)
	params, _ := json.Marshal(ftpSSHDSyncParams{Accounts: []ftpSSHDSyncAccount{
		{Username: "shop_deploy", ChrootDir: "/home/shop", StartDir: "/example.com/public_html"},
	}})

	res, err := ftpSSHDSyncHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !res.(ftpSSHDSyncResponse).Changed {
		t.Fatal("first sync must report changed=true")
	}
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "Match User shop_deploy") {
		t.Fatalf("file missing block: %s", content)
	}

	// Same payload again → unchanged, no rewrite.
	res, err = ftpSSHDSyncHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if res.(ftpSSHDSyncResponse).Changed {
		t.Fatal("identical resync must report changed=false")
	}
}

func TestFtpSSHDSync_EmptySetRendersHeaderOnly(t *testing.T) {
	path := xferEnv(t)
	params, _ := json.Marshal(ftpSSHDSyncParams{})
	if _, err := ftpSSHDSyncHandler(context.Background(), params); err != nil {
		t.Fatalf("sync: %v", err)
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "\nMatch User ") {
		t.Fatalf("empty set must render no Match blocks: %s", content)
	}
}

func TestFtpSSHDSync_Validation(t *testing.T) {
	xferEnv(t)
	cases := []struct {
		name string
		acct ftpSSHDSyncAccount
	}{
		{"no underscore (not a subaccount)", ftpSSHDSyncAccount{Username: "shopdeploy", ChrootDir: "/home/shop", StartDir: "/"}},
		{"newline injection in username", ftpSSHDSyncAccount{Username: "shop_a\nPermitRootLogin yes", ChrootDir: "/home/shop", StartDir: "/"}},
		{"chroot outside /home", ftpSSHDSyncAccount{Username: "shop_dev", ChrootDir: "/etc", StartDir: "/"}},
		{"unclean chroot", ftpSSHDSyncAccount{Username: "shop_dev", ChrootDir: "/home/shop/../other", StartDir: "/"}},
		{"relative start dir", ftpSSHDSyncAccount{Username: "shop_dev", ChrootDir: "/home/shop", StartDir: "site"}},
		{"start dir traversal", ftpSSHDSyncAccount{Username: "shop_dev", ChrootDir: "/home/shop", StartDir: "/../../etc"}},
		{"space splits -d argv", ftpSSHDSyncAccount{Username: "shop_dev", ChrootDir: "/home/shop", StartDir: "/my site"}},
		{"quote in start dir", ftpSSHDSyncAccount{Username: "shop_dev", ChrootDir: "/home/shop", StartDir: "/a\"b"}},
		{"space in chroot", ftpSSHDSyncAccount{Username: "shop_dev", ChrootDir: "/home/my shop", StartDir: "/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params, _ := json.Marshal(ftpSSHDSyncParams{Accounts: []ftpSSHDSyncAccount{tc.acct}})
			_, err := ftpSSHDSyncHandler(context.Background(), params)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if ae := asAgentErr(t, err); ae.Code != agentwire.CodeInvalidArgument {
				t.Fatalf("expected invalid_argument, got %v", err)
			}
		})
	}

	t.Run("duplicate usernames", func(t *testing.T) {
		a := ftpSSHDSyncAccount{Username: "shop_dev", ChrootDir: "/home/shop", StartDir: "/"}
		params, _ := json.Marshal(ftpSSHDSyncParams{Accounts: []ftpSSHDSyncAccount{a, a}})
		if _, err := ftpSSHDSyncHandler(context.Background(), params); err == nil {
			t.Fatal("expected duplicate rejection")
		}
	})
}
