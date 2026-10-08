package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: "Overwrite existing items with the backup" is off by default, so
// an upload restore adds only what isn't on this server yet (keep_existing).
// Home files already there stay, and a database with data or a docker app
// with data is left as it is and not claimed. Without keep_existing the
// restore replaces, as before.

// keepExecRecorder stubs every command and records it as one line.
// MariaDB: "SHOW TABLES <db>" answers a table for withTables, ERROR 1049 for
// missing, nothing otherwise (exists, empty). PostgreSQL: pgWithTables exist
// and count 3 tables.
func keepExecRecorder(t *testing.T, withTables, missing, pgWithTables []string) *[]string {
	t.Helper()
	in := func(xs []string, x string) bool { return containsString(xs, x) }
	var cmds []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		cmds = append(cmds, line)
		last := args[len(args)-1]
		switch {
		case name == "mariadb" && strings.Contains(line, "SHOW TABLES"):
			if in(withTables, last) {
				return exec.CommandContext(ctx, "echo", "wp_posts")
			}
			if in(missing, last) {
				return exec.CommandContext(ctx, "sh", "-c", "echo \"ERROR 1049 (42000): Unknown database '"+last+"'\" >&2; exit 1")
			}
		case name == "mariadb" && strings.Contains(line, "information_schema.ROUTINES"):
			if in(withTables, last) {
				return exec.CommandContext(ctx, "echo", "1")
			}
			if in(missing, last) {
				return exec.CommandContext(ctx, "sh", "-c", "echo \"ERROR 1049 (42000): Unknown database '"+last+"'\" >&2; exit 1")
			}
			return exec.CommandContext(ctx, "echo", "0")
		case strings.Contains(line, "pg_proc"):
			return exec.CommandContext(ctx, "echo", "3")
		case strings.Contains(line, "pg_database WHERE datname"):
			for _, db := range pgWithTables {
				if strings.Contains(line, "'"+db+"'") {
					return exec.CommandContext(ctx, "echo", "1")
				}
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &cmds
}

func homeRsync(cmds []string, home string) string {
	for _, c := range cmds {
		if strings.HasPrefix(c, "rsync ") && strings.Contains(c, home) {
			return c
		}
	}
	return ""
}

func TestKeepExisting_HomeAddsOnlyMissingFiles(t *testing.T) {
	me := currentUsername(t)
	for _, keep := range []bool{true, false} {
		root, homes := t.TempDir(), t.TempDir()
		prev := restoreHomeRoot
		restoreHomeRoot = homes
		t.Cleanup(func() { restoreHomeRoot = prev })
		home := filepath.Join(homes, me)
		mustWrite(t, filepath.Join(home, "index.html"), "mine")
		mustWrite(t, filepath.Join(root, "home", "home", me, "index.html"), "from the backup")
		mustWrite(t, filepath.Join(root, "home", "home", me, "new.txt"), "added")
		cmds := keepExecRecorder(t, nil, nil, nil)
		stages := []backup.ManifestStage{{Name: backup.StageHome}}
		results := []backupRestoreStage{{Name: backup.StageHome, Status: backup.StageStatusOK}}

		applied, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results,
			restoreEnforcement{KeepExisting: keep})

		rsync := homeRsync(*cmds, home)
		if keep {
			if rsync != "" {
				t.Errorf("keep-existing ran %q", rsync)
			}
			for path, want := range map[string]string{"index.html": "mine", "new.txt": "added"} {
				if got, err := os.ReadFile(filepath.Join(home, path)); err != nil || string(got) != want {
					t.Errorf("keep-existing %s = %q (%v), want %q (warnings %v)", path, got, err, want, warnings)
				}
			}
		} else if !strings.Contains(rsync, "--delete") {
			t.Errorf("replacing home: rsync %q, want the backup mirrored (--delete)", rsync)
		}
		if keep != hasWarning(applied, "(files already there kept)") {
			t.Errorf("keep=%v: applied %v", keep, applied)
		}
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func TestAddMissingHomeFiles_KeepsWhatIsThere(t *testing.T) {
	src, home := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, "index.html"), "from the backup")
	mustWrite(t, filepath.Join(src, "new.txt"), "added")
	mustWrite(t, filepath.Join(src, "domains", "a.test", "public_html", "wp-config.php"), "backup config")
	mustWrite(t, filepath.Join(src, "notes", "deep", "todo.txt"), "restored")
	mustWrite(t, filepath.Join(src, "bin", "run.sh"), "#!/bin/sh")
	if err := os.Chmod(filepath.Join(src, "bin", "run.sh"), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "notes"), 0o751); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(src, "new.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, "index.html"), "mine")
	mustWrite(t, filepath.Join(home, "domains", "a.test", "public_html", "wp-config.php"), "my config")
	if err := os.Chmod(filepath.Join(home, "domains"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := addMissingHomeFiles(context.Background(), src+"/", home+"/", os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"index.html": "mine",
		"new.txt":    "added",
		"domains/a.test/public_html/wp-config.php": "my config",
		"notes/deep/todo.txt":                      "restored",
	} {
		if got := readString(t, filepath.Join(home, path)); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	for path, want := range map[string]os.FileMode{"bin/run.sh": 0o775, "notes": 0o751, "domains": 0o700} {
		if st, err := os.Stat(filepath.Join(home, path)); err != nil || st.Mode().Perm() != want {
			t.Errorf("%s mode %v (%v), want %v", path, st.Mode(), err, want)
		}
	}
	if st, err := os.Stat(filepath.Join(home, "new.txt")); err != nil || !st.ModTime().Equal(old) {
		t.Errorf("new.txt time %v (%v), want the backup's %v", st.ModTime(), err, old)
	}
}

// SECURITY: a tenant's symlink is "already there". Nothing is written
// through it, whatever the backup has at that name.
func TestAddMissingHomeFiles_NeverWritesThroughALink(t *testing.T) {
	src, home, outside := t.TempDir(), t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, "uploads", "evil.php"), "backup")
	mustWrite(t, filepath.Join(src, "index.html"), "backup")
	mustWrite(t, filepath.Join(src, "dangling"), "backup")
	mustWrite(t, filepath.Join(outside, "index.html"), "outside")
	for link, target := range map[string]string{
		"uploads":    outside,                              // a directory in the backup
		"index.html": filepath.Join(outside, "index.html"), // a file in the backup
		"dangling":   filepath.Join(outside, "dangling"),   // points at nothing yet
	} {
		if err := os.Symlink(target, filepath.Join(home, link)); err != nil {
			t.Fatal(err)
		}
	}

	if err := addMissingHomeFiles(context.Background(), src, home, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 || readString(t, filepath.Join(outside, "index.html")) != "outside" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the copy wrote through a link: outside has %v, index.html %q", names, readString(t, filepath.Join(outside, "index.html")))
	}
}

func TestAddMissingHomeFiles_DirectorySwappedForALinkMidCopy(t *testing.T) {
	src, home, outside := t.TempDir(), t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, "uploads", "evil.php"), "backup")
	if err := os.Mkdir(filepath.Join(home, "uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	keepHomeRace = func(name string) {
		if name != "uploads" {
			return
		}
		p := filepath.Join(home, "uploads")
		if err := os.Rename(p, p+".old"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(outside, p); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { keepHomeRace = nil })

	if err := addMissingHomeFiles(context.Background(), src, home, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("the copy followed the swapped-in link: outside has %d entries", len(entries))
	}
}

// A link the tenant plants at a missing name after the copy looked is
// still never written through.
func TestAddMissingHomeFiles_LinkPlantedMidCopy(t *testing.T) {
	src, home, outside := t.TempDir(), t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, "index.html"), "backup")
	mustWrite(t, filepath.Join(src, "uploads", "evil.php"), "backup")
	mustWrite(t, filepath.Join(outside, "index.html"), "outside")
	keepHomeRace = func(name string) {
		target := map[string]string{"index.html": filepath.Join(outside, "index.html"), "uploads": outside}[name]
		if err := os.Symlink(target, filepath.Join(home, name)); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { keepHomeRace = nil })

	if err := addMissingHomeFiles(context.Background(), src, home, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 || readString(t, filepath.Join(outside, "index.html")) != "outside" {
		t.Errorf("the copy wrote through a planted link: outside has %d entries, index.html %q", len(entries), readString(t, filepath.Join(outside, "index.html")))
	}
}

// The staged file is read only when it is still the regular file the copy
// looked at, never through a link and never from a fifo.
func TestAddMissingHomeFiles_ReadsOnlyTheStagedFileItLookedAt(t *testing.T) {
	for name, swap := range map[string]func(t *testing.T, p, outside string){
		"a link": func(t *testing.T, p, outside string) {
			if err := os.Symlink(filepath.Join(outside, "other"), p+".new"); err != nil {
				t.Error(err)
			}
		},
		"another file": func(t *testing.T, p, outside string) { mustWrite(t, p+".new", "outside") },
		"a fifo": func(t *testing.T, p, outside string) {
			if err := syscall.Mkfifo(p+".new", 0o600); err != nil {
				t.Error(err)
			}
		},
		// Removed first, so the fifo may reuse the file's inode number.
		"a fifo in its place": func(t *testing.T, p, outside string) {
			if err := os.Remove(p); err != nil {
				t.Error(err)
			}
			if err := syscall.Mkfifo(p+".new", 0o600); err != nil {
				t.Error(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			src, home, outside := t.TempDir(), t.TempDir(), t.TempDir()
			mustWrite(t, filepath.Join(src, "index.html"), "backup")
			mustWrite(t, filepath.Join(outside, "other"), "outside")
			keepHomeRace = func(n string) {
				if n != "index.html" {
					return
				}
				// Put the new entry in place with a rename, so the old file
				// is still there and its inode isn't reused.
				p := filepath.Join(src, n)
				swap(t, p, outside)
				if err := os.Rename(p+".new", p); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(func() { keepHomeRace = nil })

			done := make(chan error, 1)
			go func() { done <- addMissingHomeFiles(context.Background(), src, home, os.Getuid(), os.Getgid()) }()
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the copy blocked opening the staged file")
			}
			if err == nil {
				t.Error("a staged file replaced mid-copy was copied without an error")
			}
			if _, lerr := os.Lstat(filepath.Join(home, "index.html")); !os.IsNotExist(lerr) {
				t.Errorf("home/index.html was created (%v); want nothing added for it", lerr)
			}
		})
	}
}

// The copy sets the owner of the link it created, and of nothing the tenant
// puts at that name before the owner is set.
func TestAddMissingHomeFiles_SetsTheOwnerOfOnlyTheLinkItCreated(t *testing.T) {
	src, home := t.TempDir(), t.TempDir()
	if err := os.Symlink("releases/v2", filepath.Join(src, "current")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	keepHomeRace = func(name string) {
		if name != "current" {
			return
		}
		if calls++; calls != 2 { // after the link is created
			return
		}
		p := filepath.Join(home, "current")
		if err := os.Remove(p); err != nil {
			t.Error(err)
		}
		mustWrite(t, p, "tenant")
	}
	t.Cleanup(func() { keepHomeRace = nil })

	err := addMissingHomeFiles(context.Background(), src, home, os.Getuid(), os.Getgid())
	if calls != 2 {
		t.Errorf("race hook ran %d times for the link, want 2 (before creating it and before setting its owner)", calls)
	}
	if err == nil {
		t.Error("an entry swapped in for the created link had its owner set without an error")
	}
	if got := readString(t, filepath.Join(home, "current")); got != "tenant" {
		t.Errorf("home/current = %q, want the tenant's file left as it is", got)
	}
}

func TestAddMissingHomeFiles_CopiesALinkAsALink(t *testing.T) {
	src, home := t.TempDir(), t.TempDir()
	if err := os.Symlink("releases/v2", filepath.Join(src, "current")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := addMissingHomeFiles(context.Background(), src, home, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(home, "current")); err != nil || target != "releases/v2" {
		t.Errorf("current -> %q (%v), want the backup's link", target, err)
	}
	if _, err := os.Lstat(filepath.Join(home, "pipe")); !os.IsNotExist(err) {
		t.Errorf("a fifo was copied (%v)", err)
	}
}

func TestAddMissingHomeFiles_CreatesAMissingHome(t *testing.T) {
	src, home := t.TempDir(), filepath.Join(t.TempDir(), "alice")
	if err := os.Chmod(src, 0o751); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, ".profile"), "restored")

	if err := addMissingHomeFiles(context.Background(), src+"/", home+"/", os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, filepath.Join(home, ".profile")); got != "restored" {
		t.Errorf(".profile = %q", got)
	}
	if st, err := os.Stat(home); err != nil || st.Mode().Perm() != 0o751 {
		t.Errorf("new home mode %v (%v), want the backup's 0751", st.Mode(), err)
	}
}

func TestAddMissingHomeFiles_ReportsWhatItCouldNotAdd(t *testing.T) {
	src, home := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, "locked", "a.txt"), "x")
	if err := os.Chmod(filepath.Join(src, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "locked"), 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	err := addMissingHomeFiles(context.Background(), src, home, os.Getuid(), os.Getgid())
	if err == nil || !strings.Contains(err.Error(), "1 files not added: locked") {
		t.Fatalf("err %v, want the unreadable directory reported", err)
	}
	err = addMissingHomeFiles(context.Background(), filepath.Join(src, "gone"), home, os.Getuid(), os.Getgid())
	if err == nil || !strings.Contains(err.Error(), "read the backup's files") {
		t.Fatalf("missing source: err %v", err)
	}
}

// Keep-existing leaves the owners of the files already in the home alone:
// the user's own copy needs no chown pass. Seen through a set-user-ID file:
// a chown clears the bit, even to the owner the file already has.
func TestKeepExisting_HomeSkipsTheChownPass(t *testing.T) {
	me := currentUsername(t)
	for _, keep := range []bool{true, false} {
		root, homes := t.TempDir(), t.TempDir()
		prev := restoreHomeRoot
		restoreHomeRoot = homes
		t.Cleanup(func() { restoreHomeRoot = prev })
		tool := filepath.Join(homes, me, "bin", "tool")
		mustWrite(t, tool, "#!/bin/sh\n")
		if err := os.Chmod(tool, 0o755|os.ModeSetuid); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(root, "home", "home", me, "index.html"), "hi")
		keepExecRecorder(t, nil, nil, nil)
		stages := []backup.ManifestStage{{Name: backup.StageHome}}
		results := []backupRestoreStage{{Name: backup.StageHome, Status: backup.StageStatusOK}}

		_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results,
			restoreEnforcement{KeepExisting: keep})

		st, err := os.Stat(tool)
		if err != nil {
			t.Fatal(err)
		}
		if walked := st.Mode()&os.ModeSetuid == 0; walked == keep {
			t.Errorf("keep=%v: chown pass ran=%v (warnings %v)", keep, walked, warnings)
		}
	}
}

// A copy that fails is a warning, never an "applied" line.
func TestKeepExisting_FailedHomeCopyIsNotApplied(t *testing.T) {
	me := currentUsername(t)
	root, homes := t.TempDir(), t.TempDir()
	prevHome := restoreHomeRoot
	restoreHomeRoot = homes
	t.Cleanup(func() { restoreHomeRoot = prevHome })
	mustWrite(t, filepath.Join(homes, me), "not a directory")
	mustWrite(t, filepath.Join(root, "home", "home", me, "index.html"), "hi")
	keepExecRecorder(t, nil, nil, nil)
	stages := []backup.ManifestStage{{Name: backup.StageHome}}
	results := []backupRestoreStage{{Name: backup.StageHome, Status: backup.StageStatusOK}}

	applied, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results,
		restoreEnforcement{KeepExisting: true})

	if hasWarning(applied, "home →") {
		t.Errorf("a failed copy is reported applied: %v", applied)
	}
	if !hasWarning(warnings, "home: open "+filepath.Join(homes, me)+": not a directory") {
		t.Errorf("warnings %v should name the failed copy", warnings)
	}
}

func TestKeepExisting_LeavesADatabaseWithDataAndClaimsOnlyWhatItLoads(t *testing.T) {
	me := currentUsername(t)
	full, empty, fresh := me+"_full", me+"_empty", me+"_fresh"
	run := func(keep bool) ([]string, []string, map[string]bool) {
		cmds := keepExecRecorder(t, []string{full}, []string{fresh}, nil)
		root := t.TempDir()
		var stages []backup.ManifestStage
		for _, n := range []string{full, empty, fresh} {
			stages = append(stages, backup.ManifestStage{Name: backup.StageDB, Items: []string{n}})
		}
		results := stageUpload(t, root, stages)
		claims := &restoreClaims{}
		enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_", AllowedDBNames: []string{full, empty},
			ForeignDBNames: []string{}, Claims: claims, KeepExisting: keep}
		_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)
		return claims.Databases, warnings, createStatements(*cmds)
	}

	claimed, warnings, created := run(true)
	if _, ok := created[full]; ok || containsString(claimed, full) {
		t.Errorf("keep-existing: %s already has data and was loaded (created %v, claimed %v)", full, created, claimed)
	}
	if !hasWarning(warnings, "db "+full+`: kept: it already has data on this server; check "Overwrite existing items with the backup" to replace it`) {
		t.Errorf("warnings %v should say %s was kept and how to replace it", warnings, full)
	}
	if strings.Join(claimed, ",") != empty+","+fresh {
		t.Errorf("keep-existing: claimed %v (warnings %v), want the empty and the new database", claimed, warnings)
	}

	claimed, _, created = run(false)
	if _, ok := created[full]; !ok || strings.Join(claimed, ",") != full+","+empty+","+fresh {
		t.Errorf("replace: created %v claimed %v, want every database loaded", created, claimed)
	}
}

func TestKeepExisting_LeavesAPostgresDatabaseWithData(t *testing.T) {
	me := currentUsername(t)
	db := me + "_pg"
	keepExecRecorder(t, nil, nil, []string{db})
	root := t.TempDir()
	stages := []backup.ManifestStage{{Name: backup.StageDB, Items: []string{db}}}
	results := []backupRestoreStage{{Name: backup.StageDB, Status: backup.StageStatusOK}}
	mustWrite(t, filepath.Join(root, "db", db+".pgdump"), "PGDMP")
	claims := &restoreClaims{}
	enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_", AllowedDBNames: []string{db},
		ForeignDBNames: []string{}, Claims: claims, KeepExisting: true}

	applied, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	if len(claims.Databases) != 0 || hasWarning(applied, "db → "+db) {
		t.Errorf("claimed %v applied %v: the PostgreSQL database with data was restored", claims.Databases, applied)
	}
	if !hasWarning(warnings, "db "+db+" (postgres): kept: it already has data on this server") {
		t.Errorf("warnings %v should say %s was kept", warnings, db)
	}
}

func TestKeepExisting_LeavesADockerAppWithData(t *testing.T) {
	me := currentUsername(t)
	cmds := keepExecRecorder(t, nil, nil, nil)
	root, live := t.TempDir(), t.TempDir()
	prev := restoreDockerRoot
	restoreDockerRoot = live
	t.Cleanup(func() { restoreDockerRoot = prev })
	mustWrite(t, filepath.Join(live, "mine", "compose.yml"), "services: {}")
	var stages []backup.ManifestStage
	for _, s := range []string{"mine", "fresh"} {
		stages = append(stages, backup.ManifestStage{Name: backup.StageDocker, Items: []string{s}})
	}
	results := stageUpload(t, root, stages)
	claims := &restoreClaims{}
	enf := restoreEnforcement{Mode: restoreModeUpload, AllowedDBNames: []string{}, OwnedDockerSlugs: []string{"mine"},
		ForeignDockerSlugs: []string{}, Claims: claims, KeepExisting: true}

	_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	for _, c := range *cmds {
		if strings.Contains(c, filepath.Join(live, "mine")) {
			t.Errorf("the app with data was touched: %q", c)
		}
	}
	if strings.Join(claims.DockerSlugs, ",") != "fresh" {
		t.Errorf("claimed %v (warnings %v), want only the new app", claims.DockerSlugs, warnings)
	}
	if !hasWarning(warnings, "docker mine: kept: its data is already on this server") {
		t.Errorf("warnings %v should say mine was kept", warnings)
	}
}

func TestAgentVersion_ReportsKeepExisting(t *testing.T) {
	out, err := agentVersionHandler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if caps := out.(agentVersionResponse).Capabilities; !containsString(caps, capRestoreKeepExisting) {
		t.Fatalf("capabilities %v should include %s", caps, capRestoreKeepExisting)
	}
}

// The panel's keep_existing reaches the restore.
func TestBackupRestoreFromTar_KeepExistingParam(t *testing.T) {
	for raw, want := range map[string]bool{`{"keep_existing":true}`: true, `{}`: false} {
		var p backupRestoreFromTarParams
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		if got := p.enforcement().KeepExisting; got != want {
			t.Errorf("%s: KeepExisting=%v, want %v", raw, got, want)
		}
	}
}
