package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

func testSSOKey(t *testing.T, seed byte) *ssokey.Key {
	t.Helper()
	var k ssokey.Key
	for i := range k {
		k[i] = seed + byte(i)
	}
	return &k
}

func withSSOKey(t *testing.T, k *ssokey.Key) {
	t.Helper()
	orig := retentionSSOKey
	retentionSSOKey = func() *ssokey.Key { return k }
	t.Cleanup(func() { retentionSSOKey = orig })
}

func withSharedPasswordFile(t *testing.T, path string) {
	t.Helper()
	orig := resticPasswordFile
	resticPasswordFile = path
	t.Cleanup(func() { resticPasswordFile = orig })
}

func sealedDest(t *testing.T, k *ssokey.Key, id, password string) *models.BackupDestination {
	t.Helper()
	sealed, err := k.Seal([]byte(password))
	if err != nil {
		t.Fatal(err)
	}
	return &models.BackupDestination{ID: id, Name: "rotated-" + id, URL: "/srv/" + id, PasswordEnc: sealed}
}

func TestDestPasswords_UnrotatedDestinationUsesTheSharedFile(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "restic-repo.password")
	if err := os.WriteFile(shared, []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	withSharedPasswordFile(t, shared)
	pw := newDestPasswords()
	defer pw.close()
	r, err := pw.repo(&models.BackupDestination{ID: "d1", URL: "/srv/d1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.PasswordFile != shared {
		t.Errorf("password file = %q, want the shared %q", r.PasswordFile, shared)
	}

	withSharedPasswordFile(t, filepath.Join(t.TempDir(), "gone"))
	if _, err := pw.repo(&models.BackupDestination{ID: "d2", URL: "/srv/d2"}); err == nil {
		t.Error("an unrotated destination with no shared password file must fail")
	}
}

func TestDestPasswords_RotatedDestinationGetsItsOwnPasswordFile(t *testing.T) {
	k := testSSOKey(t, 1)
	withSSOKey(t, k)
	// The shared file is gone (the reconciler deletes it once every
	// destination is rotated); a rotated destination must not need it.
	withSharedPasswordFile(t, filepath.Join(t.TempDir(), "gone"))
	pw := newDestPasswords()
	d := sealedDest(t, k, "d1", "rotated-secret")
	r, err := pw.repo(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(r.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "rotated-secret" {
		t.Errorf("password file holds %q, want the unsealed password", got)
	}
	if fi, _ := os.Stat(r.PasswordFile); fi.Mode().Perm() != 0o600 {
		t.Errorf("password file mode = %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(r.PasswordFile)); fi.Mode().Perm() != 0o700 {
		t.Errorf("password dir mode = %v, want 0700", fi.Mode().Perm())
	}
	if !strings.Contains(strings.Join(r.args(), " "), "--password-file "+r.PasswordFile) {
		t.Errorf("restic args must use the destination's password file: %v", r.args())
	}
	for _, a := range r.args() {
		if strings.Contains(a, "rotated-secret") {
			t.Fatalf("the password must never appear in argv: %v", r.args())
		}
	}
	again, err := pw.repo(d)
	if err != nil || again.PasswordFile != r.PasswordFile {
		t.Errorf("second lookup = %q, %v; want the same file", again.PasswordFile, err)
	}

	pw.close()
	if _, err := os.Stat(filepath.Dir(r.PasswordFile)); !os.IsNotExist(err) {
		t.Errorf("close must remove the password files, stat err = %v", err)
	}
}

func TestDestPasswords_RotatedDestinationWithoutTheKeyFails(t *testing.T) {
	k := testSSOKey(t, 1)
	d := sealedDest(t, k, "d1", "rotated-secret")

	withSSOKey(t, nil)
	pw := newDestPasswords()
	if _, err := pw.repo(d); err == nil || !strings.Contains(err.Error(), "rotated") {
		t.Errorf("no SSO key: err = %v, want a rotated-password error", err)
	}

	withSSOKey(t, testSSOKey(t, 99))
	pw = newDestPasswords()
	defer pw.close()
	if _, err := pw.repo(d); err == nil || !strings.Contains(err.Error(), "unseal") {
		t.Errorf("wrong SSO key: err = %v, want an unseal error", err)
	}
}

// A destination whose password was rotated is no longer opened by the shared
// file. The sweep must use its own password, end to end on a real repository.
func TestForgetForSchedule_RealRestic_RotatedDestination(t *testing.T) {
	r := newTestRepo(t)
	for _, j := range []struct{ id, at string }{
		{"R1", "2026-01-01 10:00:00"}, {"R2", "2026-01-02 10:00:00"}, {"R3", "2026-01-03 10:00:00"},
	} {
		r.backupStage(t, j.id, "home", j.at)
		r.backupStage(t, j.id, "manifest", j.at)
	}
	repoPassword, err := os.ReadFile(r.pw)
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(t.TempDir(), "restic-repo.password")
	if err := os.WriteFile(shared, []byte("the-old-shared-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withSharedPasswordFile(t, shared)
	k := testSSOKey(t, 7)
	withSSOKey(t, k)
	d := sealedDest(t, k, "d1", string(repoPassword))
	d.URL = r.dir
	sched := models.BackupSchedule{ID: "s1", KeepDaily: keep(1)}
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{}}

	// Control: the shared file the sweep used before does not open it.
	if err := forgetForSchedule(context.Background(), newRetentionTestCmd(), sched, resticRepo{d, shared}, jobs, false); err == nil {
		t.Fatal("control: the shared password opened the rotated repository; the scenario does not exercise the bug")
	}

	pw := newDestPasswords()
	defer pw.close()
	rr, err := pw.repo(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := forgetForSchedule(context.Background(), newRetentionTestCmd(), sched, rr, jobs, false); err != nil {
		t.Fatalf("forgetForSchedule with the destination's own password: %v", err)
	}
	got := stagesByJob(r.snapshots(t))
	if len(got) != 1 || got["R3"] == nil {
		t.Errorf("want only R3 kept, got %v", got)
	}
}
