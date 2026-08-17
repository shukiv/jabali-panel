package commands

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeWPInstall creates a tmpdir with a stub wp-load.php so CreateWordPressSSOFile
// can resolve the path. Returns the install dir.
func fakeWPInstall(t *testing.T) string {
	t.Helper()
	// The SSO writer binds install_path to the os_user's home (Gitea #470),
	// so the fixture must live under the current user's home, not /tmp.
	me, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	dir, err := os.MkdirTemp(me.HomeDir, "jabali-ssotest-")
	if err != nil {
		t.Fatalf("mkdtemp under home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "wp-load.php"), []byte("<?php // stub\n"), 0o644); err != nil {
		t.Fatalf("seed wp-load.php: %v", err)
	}
	return dir
}

func TestCreateWordPressSSOFile_HappyPath(t *testing.T) {
	withRealExec(t) // GH #994: needs the real read-only command (stub returns empty output)
	dir := fakeWPInstall(t)
	me, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}

	resp, err := CreateWordPressSSOFile(context.Background(), createSSOFileReq{
		InstallPath: dir,
		OSUser:      me.Username, // chown to ourselves so it works without root
		InstallID:   validULID,
		AdminUsername: "admin",
	})
	if err != nil {
		// chown to www-data fails for non-root, so accept that as a skip.
		if strings.Contains(err.Error(), "chown") {
			t.Skipf("chown to www-data unavailable in this test env: %v", err)
		}
		t.Fatalf("CreateWordPressSSOFile: %v", err)
	}

	// File name shape
	if !regexp.MustCompile(`^jabali-sso-[A-Za-z0-9_-]{43}\.php$`).MatchString(resp.FileName) {
		t.Errorf("FileName %q does not match jabali-sso-<43chars>.php", resp.FileName)
	}

	// File exists at the expected path
	full := filepath.Join(dir, resp.FileName)
	if _, err := os.Stat(full); err != nil {
		t.Errorf("file not on disk at %s: %v", full, err)
	}

	// Content contains the install id and the wp-load path
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body), validULID) {
		t.Errorf("file body missing install id")
	}
	if !strings.Contains(string(body), "wp-load.php") {
		t.Errorf("file body missing wp-load.php path")
	}

	// Expiry is now + 60s (allow a few seconds slack for test timing)
	now := time.Now().Unix()
	if resp.ExpiresAtUnix < now+58 || resp.ExpiresAtUnix > now+62 {
		t.Errorf("ExpiresAtUnix = %d, want roughly now+60 (= %d)", resp.ExpiresAtUnix, now+60)
	}
}

func TestCreateWordPressSSOFile_RejectsNonAbsoluteInstallPath(t *testing.T) {
	_, err := CreateWordPressSSOFile(context.Background(), createSSOFileReq{
		InstallPath: "relative/path",
		OSUser:      "irrelevant",
		InstallID:   validULID,
		AdminUsername: "admin",
	})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("expected absolute-path error, got %v", err)
	}
}

func TestCreateWordPressSSOFile_MissingWPLoad(t *testing.T) {
	dir := t.TempDir() // no wp-load.php seeded
	_, err := CreateWordPressSSOFile(context.Background(), createSSOFileReq{
		InstallPath: dir,
		OSUser:      "irrelevant",
		InstallID:   validULID,
		AdminUsername: "admin",
	})
	if err == nil || !strings.Contains(err.Error(), "wp-load.php not found") {
		t.Errorf("expected wp-load-not-found error, got %v", err)
	}
}

func TestCreateWordPressSSOFile_RejectsInvalidInstallID(t *testing.T) {
	dir := fakeWPInstall(t)
	_, err := CreateWordPressSSOFile(context.Background(), createSSOFileReq{
		InstallPath: dir,
		OSUser:      "x",
		InstallID:   "not-a-ulid",
		AdminUsername: "admin",
	})
	if err == nil || !strings.Contains(err.Error(), "ULID") {
		t.Errorf("expected ULID error, got %v", err)
	}
}

func TestCreateWordPressSSOFile_RejectsInvalidAdminUsername(t *testing.T) {
	dir := fakeWPInstall(t)
	_, err := CreateWordPressSSOFile(context.Background(), createSSOFileReq{
		InstallPath:   dir,
		OSUser:        "x",
		InstallID:     validULID,
		AdminUsername: "", // empty rejected by RenderWordPressSSOTemplate
	})
	if err == nil || !strings.Contains(err.Error(), "adminUsername") {
		t.Errorf("expected adminUsername error, got %v", err)
	}
}

func TestCreateWordPressSSOFile_ChownFailureCleansUp(t *testing.T) {
	// Point at a definitely-nonexistent user so chown fails. Verify
	// no jabali-sso-*.php file is left behind in the install dir.
	dir := fakeWPInstall(t)
	_, err := CreateWordPressSSOFile(context.Background(), createSSOFileReq{
		InstallPath: dir,
		OSUser:      "definitely-not-a-real-user-9876543210",
		InstallID:   validULID,
		AdminUsername: "admin",
	})
	if err == nil {
		t.Fatalf("expected chown error, got nil")
	}

	// No jabali-sso-*.php should remain
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "jabali-sso-") {
			t.Errorf("jabali-sso file leaked after chown failure: %s", e.Name())
		}
		if strings.HasPrefix(e.Name(), ".jabali-sso-") {
			t.Errorf("jabali-sso tmp file leaked after chown failure: %s", e.Name())
		}
	}
}
