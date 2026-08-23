package commands

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// webdavWorkerEnabled is the reconciler's drift signal: true iff the per-instance
// drop-in dir exists (written by enable, removed by disable).
func TestWebdavWorkerEnabled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("JABALI_SYSTEMD_ROOT", root)
	if webdavWorkerEnabled("t1_web") {
		t.Fatal("no drop-in dir yet → must report disabled")
	}
	if err := os.MkdirAll(filepath.Join(root, "jabali-webdav@t1_web.service.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !webdavWorkerEnabled("t1_web") {
		t.Fatal("drop-in dir present → must report enabled")
	}
}

// deriveWebdavWorker must chroot isolated (#1145) accounts to their jail and
// serve the bind-mounted /data, while legacy same-uid aliases serve their passwd
// home with no chroot.
func TestDeriveWebdavWorker(t *testing.T) {
	tenant := &ftpTenant{Username: "t1", UID: 1000, GID: 1000, HomeDir: "/home/t1"}

	t.Run("isolated", func(t *testing.T) {
		u := &user.User{Username: "t1_web", Uid: "1000000042", Gid: "1000000042", HomeDir: "/var/lib/jabali-ftp-jails/t1/t1_web"}
		wp, aerr := deriveWebdavWorker(tenant, u)
		if aerr != nil {
			t.Fatalf("deriveWebdavWorker: %v", aerr)
		}
		if wp.UID != 1000000042 || wp.GID != 1000000042 {
			t.Fatalf("uid/gid = %d/%d, want 1000000042/1000000042", wp.UID, wp.GID)
		}
		if wp.ServedRoot != "/data" {
			t.Fatalf("ServedRoot = %q, want /data (bind mount inside chroot)", wp.ServedRoot)
		}
		if wp.Jail != "/var/lib/jabali-ftp-jails/t1/t1_web" {
			t.Fatalf("Jail = %q, want the canonical jail", wp.Jail)
		}
	})

	t.Run("legacy same-uid alias", func(t *testing.T) {
		u := &user.User{Username: "t1_leg", Uid: "1000", Gid: "1000", HomeDir: "/home/t1/public_html"}
		wp, aerr := deriveWebdavWorker(tenant, u)
		if aerr != nil {
			t.Fatalf("deriveWebdavWorker: %v", aerr)
		}
		if wp.ServedRoot != "/home/t1/public_html" {
			t.Fatalf("ServedRoot = %q, want the passwd home", wp.ServedRoot)
		}
		if wp.Jail != "" {
			t.Fatalf("Jail = %q, want empty (no chroot for legacy)", wp.Jail)
		}
	})
}

// buildWebdavDropin encodes the security-relevant systemd override: NUMERIC
// uid/gid (a name may not resolve inside the chroot), the jail as RootDirectory,
// and the static binary bound into the jail — for isolated accounts only.
func TestBuildWebdavDropin(t *testing.T) {
	isolated := buildWebdavDropin(webdavWorkerParams{
		Username: "t1_web", UID: 1000000042, GID: 1000000042,
		ServedRoot: "/data", Jail: "/var/lib/jabali-ftp-jails/t1/t1_web",
	})
	for _, want := range []string{
		"User=1000000042",
		"Group=1000000042",
		"Environment=WEBDAV_ROOT=/data",
		"Environment=WEBDAV_PREFIX=/dav",
		"RootDirectory=/var/lib/jabali-ftp-jails/t1/t1_web",
		"BindReadOnlyPaths=/usr/local/bin/jabali-webdav",
	} {
		if !strings.Contains(isolated, want) {
			t.Errorf("isolated drop-in missing %q\n---\n%s", want, isolated)
		}
	}

	legacy := buildWebdavDropin(webdavWorkerParams{
		Username: "t1_leg", UID: 1000, GID: 1000,
		ServedRoot: "/home/t1/public_html", Jail: "",
	})
	if !strings.Contains(legacy, "User=1000") || !strings.Contains(legacy, "Environment=WEBDAV_ROOT=/home/t1/public_html") {
		t.Errorf("legacy drop-in missing uid or served root\n---\n%s", legacy)
	}
	// A legacy alias has no jail: it must NOT chroot or bind the binary in.
	if strings.Contains(legacy, "RootDirectory=") || strings.Contains(legacy, "BindReadOnlyPaths=") {
		t.Errorf("legacy drop-in must not chroot\n---\n%s", legacy)
	}
}
