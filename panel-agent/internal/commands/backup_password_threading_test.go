package commands

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The per-destination restic password (M30.2.x) reaches the agent as
// `password_file` on backup.create / system.backup. Every code path that
// opens the destination repo must carry it forward: a stage that omits it
// silently falls back to the legacy shared
// /etc/jabali-panel/restic-repo.password, which cannot open a rotated
// destination's repo. The failure is quiet — the stage fails, and the job
// can still seal a "partial" manifest a tenant later restores and finds
// empty.
//
// These tests marshal each stage's params the way the orchestrator does and
// assert password_file survives. They fail if a future edit drops the field
// from one of the re-marshals again.

// TestStageMarshalsForwardPasswordFile reads the orchestrator's source and
// asserts every stage-dispatch marshal forwards PasswordFile.
//
// A value-level test can't catch this regression: each stage builds a fresh
// params struct from `req`, so a test that constructs its own literal would
// pass while the real marshal drops the field. Asserting on the source is
// what actually pins the behaviour — same approach as
// TestNoRepoDirReadsBeforeClone, which guards an equivalent
// silently-degrading ordering rule in install.sh.
func TestStageMarshalsForwardPasswordFile(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("backup_create.go")
	if err != nil {
		t.Fatalf("read backup_create.go: %v", err)
	}
	body := string(src)

	for _, fn := range []string{"runHomeStage", "runDatabaseStage", "runMailStage"} {
		start := strings.Index(body, "func "+fn+"(")
		if start < 0 {
			t.Fatalf("could not find %s in backup_create.go — if it was renamed, "+
				"this test needs updating and the rule it protects still applies", fn)
		}
		// Bound the search at the next top-level func so one stage's marshal
		// can't satisfy the assertion for another.
		end := strings.Index(body[start+1:], "\nfunc ")
		region := body[start:]
		if end >= 0 {
			region = body[start : start+1+end]
		}
		marshalAt := strings.Index(region, "json.Marshal(")
		if marshalAt < 0 {
			t.Errorf("%s: no json.Marshal call found", fn)
			continue
		}
		if !strings.Contains(region, "PasswordFile: req.PasswordFile") {
			t.Errorf("%s does not forward PasswordFile into its stage params. "+
				"The stage then falls back to the legacy shared password file and "+
				"cannot open a destination with its own sealed password (M30.2.x), "+
				"failing silently against a rotated destination.", fn)
		}
	}
}

// bkEnsureRepoReady probes AND initialises the repo, so it must use the
// destination's own password too — probing with the legacy shared file made
// a rotated destination look unopenable, and hard-failed once the reconciler
// purged that file.
func TestEnsureRepoReadyTakesPasswordFile(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("backup_helpers.go")
	if err != nil {
		t.Fatalf("read backup_helpers.go: %v", err)
	}
	body := string(src)
	sig := "func bkEnsureRepoReady(ctx context.Context, repoURL, credentialsRef, destKind, passwordFile string"
	if !strings.Contains(body, sig) {
		t.Errorf("bkEnsureRepoReady must accept a passwordFile parameter; want signature starting %q", sig)
	}
	// The hardcoded default must not be what the probe/init use directly.
	if strings.Contains(body, "SnapshotsRemote(ctx, nil, repoURL, backup.DefaultPasswordFile") ||
		strings.Contains(body, "InitRemote(ctx, nil, repoURL, backup.DefaultPasswordFile") {
		t.Error("bkEnsureRepoReady still hardcodes backup.DefaultPasswordFile for the " +
			"probe/init; it must use the destination's password when one is supplied")
	}
}

// system.backup writes to the same destinations as account backups, so its
// params must accept password_file too — it previously had no field at all
// and built its restic config without a password.
func TestSystemBackupParamsAcceptPasswordFile(t *testing.T) {
	const pw = "/run/jabali/restic-pw/destpw-sys"
	raw := []byte(`{
		"job_id":"01JABALIJOB0000000000000AA",
		"repo_url":"sftp:backup@host:/srv/repo",
		"password_file":"` + pw + `"
	}`)
	var req systemBackupParams
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.PasswordFile != pw {
		t.Fatalf("systemBackupParams.PasswordFile = %q, want %q", req.PasswordFile, pw)
	}
}

// removePasswordTempFile is what gives the tempfile the job's lifetime. It
// must refuse paths outside the agent-owned directory (it runs as root) and
// treat a blank path as a no-op (the legacy shared-password path).
func TestRemovePasswordTempFileRefusesForeignPaths(t *testing.T) {
	if err := removePasswordTempFile(""); err != nil {
		t.Errorf("blank path should be a no-op, got %v", err)
	}
	for _, bad := range []string{
		"/etc/jabali-panel/restic-repo.password",
		"/etc/shadow",
		"/run/jabali/restic-pw-elsewhere/x",
		"/run/jabali/restic-pw/../../etc/shadow",
	} {
		if err := removePasswordTempFile(bad); err == nil {
			t.Errorf("removePasswordTempFile(%q) should have been refused", bad)
		}
	}
	// A path under the owned dir is accepted (missing file is not an error).
	if err := removePasswordTempFile(passwordTempDir + "/destpw-does-not-exist"); err != nil {
		t.Errorf("owned path should be accepted, got %v", err)
	}
}
