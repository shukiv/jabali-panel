package fsperm

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// JAB-229. The property that actually matters is INHERITANCE, not the bits on
// the directories the restore happened to create.
//
// jabali's isolation model assumes a docroot is setgid www-data, so anything the
// tenant's PHP-FPM worker creates inherits group www-data — nginx serves as
// www-data and has to traverse it. A migrated tree arrived with the source's
// ownership and no setgid below public_html. Existing directories had been
// chgrp'd, so the site looked healthy; the first directory WordPress created on
// its own (a new uploads/YYYY/MM on the 1st) inherited the tenant's primary
// group instead and came out drwxr-x--- <user>:<user> under umask 027. New
// uploads 404'd as blank thumbnails while old media still served.
//
// So this asserts the inherited group of a NEWLY created subdirectory, which is
// what breaks, rather than asserting the mode of what already exists.
func TestRepairDocrootGroup_NewSubdirInheritsGroup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runs as an unprivileged user; chgrp to an arbitrary gid would succeed as root")
	}
	// Use one of our own supplementary groups as the stand-in for www-data —
	// an unprivileged process may only chgrp to a group it belongs to.
	groups, err := os.Getgroups()
	if err != nil || len(groups) == 0 {
		t.Skip("no supplementary groups available")
	}
	target := -1
	for _, g := range groups {
		if g != os.Getegid() {
			target = g
			break
		}
	}
	if target < 0 {
		t.Skip("no group distinct from the primary one to test inheritance against")
	}

	home := t.TempDir()
	docroot := filepath.Join(home, "domains", "example.com", "public_html")
	deep := filepath.Join(docroot, "wp-content", "uploads", "2026", "07")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("seed tree: %v", err)
	}

	if err := RepairDocrootGroup(home, docroot, target); err != nil {
		t.Fatalf("RepairDocrootGroup: %v", err)
	}

	// The subtree — not just the top level — must carry setgid, or a directory
	// created deeper down inherits the wrong group.
	for _, dir := range []string{
		docroot,
		filepath.Join(docroot, "wp-content"),
		filepath.Join(docroot, "wp-content", "uploads"),
		deep,
	} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if fi.Mode()&os.ModeSetgid == 0 {
			t.Errorf("%s is not setgid — a directory created under it inherits the tenant's group", dir)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("no syscall.Stat_t")
		}
		if int(st.Gid) != target {
			t.Errorf("%s group = %d, want %d", dir, st.Gid, target)
		}
	}

	// The actual regression: create a NEW month folder the way WordPress would,
	// under a restrictive umask, and check what it inherited.
	old := syscall.Umask(0o027)
	defer syscall.Umask(old)

	newMonth := filepath.Join(deep, "..", "08")
	if err := os.Mkdir(newMonth, 0o755); err != nil {
		t.Fatalf("mkdir new month: %v", err)
	}
	fi, err := os.Stat(newMonth)
	if err != nil {
		t.Fatalf("stat new month: %v", err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if int(st.Gid) != target {
		t.Errorf("newly created month folder group = %d, want %d — this is the upload break: "+
			"nginx (www-data) cannot traverse a dir owned by the tenant's primary group", st.Gid, target)
	}
	if fi.Mode()&os.ModeSetgid == 0 {
		t.Error("new directory did not inherit setgid — the NEXT level down would break instead")
	}
}
