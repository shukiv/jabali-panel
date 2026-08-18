package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sshTestEnv redirects all three sshd drop-ins into a temp dir and skips the
// real sshd -t / reload, so the serialization + generation logic is exercised
// without touching the host.
func sshTestEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("JABALI_SSHD_XFER_DROPIN_PATH", filepath.Join(dir, "jabali-xfer.conf"))
	t.Setenv("JABALI_SSHD_DROPIN_PATH", filepath.Join(dir, "jabali-sshd.conf"))
	t.Setenv("JABALI_SSHD_SFTP_DROPIN_PATH", filepath.Join(dir, "jabali-sftp.conf"))
	t.Setenv("JABALI_SSHD_TEST_SKIP_VALIDATE", "1")
	t.Setenv("JABALI_SSHD_TEST_SKIP_RELOAD", "1")
	return filepath.Join(dir, "jabali-xfer.conf")
}

func syncGen(t *testing.T, gen int64, accts ...ftpSSHDSyncAccount) ftpSSHDSyncResponse {
	t.Helper()
	params, _ := json.Marshal(ftpSSHDSyncParams{Accounts: accts, Generation: gen})
	res, err := ftpSSHDSyncHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("sshd_sync gen=%d: %v", gen, err)
	}
	return res.(ftpSSHDSyncResponse)
}

// JAB-267: a stale (lower-generation) snapshot that lands after a newer one
// must be DROPPED, not applied — otherwise it resurrects a Match block a newer
// revocation already removed, restoring working password SFTP access.
func TestFtpSSHDSync_StaleGenerationDropped(t *testing.T) {
	path := sshTestEnv(t)
	lastXferSyncGen = 0 // isolate from other tests sharing the package global

	acct := ftpSSHDSyncAccount{Username: "shop_dev", ChrootDir: "/home/shop", StartDir: "/"}

	// gen=200 is the newer REVOCATION: the account is gone (empty set).
	if res := syncGen(t, 200); res.Stale {
		t.Fatal("newer revocation must not be marked stale")
	}
	if c, _ := os.ReadFile(path); strings.Contains(string(c), "Match User shop_dev") {
		t.Fatalf("account should be revoked after gen=200: %s", c)
	}

	// gen=100 is the STALE snapshot that still lists the account, arriving late.
	res := syncGen(t, 100, acct)
	if !res.Stale {
		t.Fatal("stale gen=100 after gen=200 must be dropped (Stale=true)")
	}
	if res.Changed {
		t.Fatal("a dropped stale sync must not report a change")
	}
	if c, _ := os.ReadFile(path); strings.Contains(string(c), "Match User shop_dev") {
		t.Fatalf("stale sync RESTORED a revoked account — JAB-267 regression: %s", c)
	}
}

// A newer or equal-or-unversioned generation still applies; gen=0 is the
// unversioned (older-panel) escape hatch and is never gated.
func TestFtpSSHDSync_GenerationOrdering(t *testing.T) {
	path := sshTestEnv(t)
	lastXferSyncGen = 0

	a := ftpSSHDSyncAccount{Username: "a_dev", ChrootDir: "/home/a", StartDir: "/"}
	b := ftpSSHDSyncAccount{Username: "b_dev", ChrootDir: "/home/b", StartDir: "/"}

	syncGen(t, 100, a)
	if c, _ := os.ReadFile(path); !strings.Contains(string(c), "Match User a_dev") {
		t.Fatal("gen=100 should apply")
	}
	// Higher gen applies and replaces.
	syncGen(t, 300, b)
	if c, _ := os.ReadFile(path); !strings.Contains(string(c), "Match User b_dev") || strings.Contains(string(c), "Match User a_dev") {
		t.Fatal("gen=300 should replace with b_dev only")
	}
	// gen=0 (unversioned) always applies, even though 0 < high-water.
	syncGen(t, 0, a)
	if c, _ := os.ReadFile(path); !strings.Contains(string(c), "Match User a_dev") {
		t.Fatal("unversioned gen=0 must apply ungated")
	}
}

// JAB-267 acceptance: concurrent sshd_sync calls (shuffled generations) racing
// system.set_ssh_config must leave the xfer drop-in equal to the HIGHEST-gen
// render — never a stale one — with no data race in the shared critical
// section. Run under -race.
func TestSSHSync_ConcurrentHighestGenerationWins(t *testing.T) {
	path := sshTestEnv(t)
	lastXferSyncGen = 0

	const n = 40
	gens := rand.Perm(n)   // 0..n-1 shuffled → worker gens 1..n
	maxGen := int64(n + 1) // strictly above every worker gen, so the winner always wins

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		g := int64(gens[i]) + 1 // 1..n
		wg.Add(1)
		go func(gen int64) {
			defer wg.Done()
			acct := ftpSSHDSyncAccount{
				Username:  fmt.Sprintf("u%d_dev", gen),
				ChrootDir: fmt.Sprintf("/home/u%d", gen),
				StartDir:  "/",
			}
			params, _ := json.Marshal(ftpSSHDSyncParams{Accounts: []ftpSSHDSyncAccount{acct}, Generation: gen})
			_, _ = ftpSSHDSyncHandler(context.Background(), params)
		}(g)
		// Concurrently hammer the sibling SSH-config writer to prove the shared
		// sshOpMu covers both handlers (they both run sshd -t on the combined
		// config).
		wg.Add(1)
		go func() {
			defer wg.Done()
			params, _ := json.Marshal(systemSetSSHConfigParams{Port: 22, PasswordAuth: true, UserPasswordAuth: true})
			_, _ = systemSetSSHConfigHandler(context.Background(), params)
		}()
	}
	// The definitive highest generation, applied last-or-gating-everything-after.
	syncGen(t, maxGen, ftpSSHDSyncAccount{Username: "winner_dev", ChrootDir: "/home/winner", StartDir: "/"})
	wg.Wait()

	c, _ := os.ReadFile(path)
	if !strings.Contains(string(c), "Match User winner_dev") {
		t.Fatalf("highest-generation render did not win: %s", c)
	}
	if strings.Contains(string(c), "\nMatch User u") {
		t.Fatalf("a lower-generation snapshot survived alongside the winner: %s", c)
	}
}
