package commands

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: an uploaded backup is extracted with its symlinks kept, because a
// home or an app's data folder legitimately holds them and the restore copies
// them as links. Everything else in the staged tree is read by root: the
// manifest, the metadata, database dumps, the mail tree and the stage folders
// themselves. Root must never reach those through a link the archive carries.

const stagedJob = "01JABCDEFGHJKMNPQRSTVWXYZ0"

// extractUpload extracts a tar with a job dir holding a manifest and metadata,
// plus entries, through the real untrusted-tar extractor, and returns the
// staging dir.
func extractUpload(t *testing.T, entries []tentry) string {
	t.Helper()
	base := []tentry{
		{typ: tar.TypeDir, name: stagedJob},
		{typ: tar.TypeDir, name: stagedJob + "/manifest"},
		{typ: tar.TypeReg, name: stagedJob + "/manifest/manifest.json", body: `{"user":{"username":"u"}}`},
		{typ: tar.TypeDir, name: stagedJob + "/meta"},
		{typ: tar.TypeReg, name: stagedJob + "/meta/metadata.json", body: `{"from":"archive"}`},
	}
	staging := t.TempDir()
	if _, err := extractTarStream(buildTar(t, append(base, entries...)), staging); err != nil {
		t.Fatalf("extract: %v", err)
	}
	return staging
}

// outsideTree returns a dir outside the staging tree holding a file of every
// name a stage reads, so a link to it would resolve.
func outsideTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"manifest.json", "metadata.json", "u_x.sql"} {
		mustWrite(t, filepath.Join(dir, name), `{"from":"outside"}`)
	}
	return dir
}

func TestReadExtractedUpload_RefusesALinkOutsideAHomeOrAppData(t *testing.T) {
	out := outsideTree(t)
	j := stagedJob + "/"
	apps := j + "docker" + dockerAppDataRoot
	cases := map[string][]tentry{
		"meta stage":   {{typ: tar.TypeSymlink, name: j + "meta/other.json", link: out + "/metadata.json"}},
		"stage folder": {{typ: tar.TypeSymlink, name: j + "db", link: out}},
		"dump file": {{typ: tar.TypeDir, name: j + "db"},
			{typ: tar.TypeSymlink, name: j + "db/u_x.sql", link: out + "/u_x.sql"}},
		"mail tree":       {{typ: tar.TypeSymlink, name: j + "mail", link: out}},
		"home stage":      {{typ: tar.TypeSymlink, name: j + "home", link: out}},
		"homes folder":    {{typ: tar.TypeDir, name: j + "home"}, {typ: tar.TypeSymlink, name: j + "home/home", link: out}},
		"home itself":     {{typ: tar.TypeDir, name: j + "home/home"}, {typ: tar.TypeSymlink, name: j + "home/home/u", link: out}},
		"app data itself": {{typ: tar.TypeDir, name: apps}, {typ: tar.TypeSymlink, name: apps + "/app1", link: out}},
		"apps folder":     {{typ: tar.TypeDir, name: j + "docker/var/lib/jabali"}, {typ: tar.TypeSymlink, name: apps, link: out}},
		"next to the job": {{typ: tar.TypeSymlink, name: "stray", link: out}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			staging := extractUpload(t, entries)
			if _, _, _, err := readExtractedUpload(staging); err == nil {
				t.Fatalf("an archive with a link at the %s was accepted", name)
			}
		})
	}
}

// The manifest and metadata are read only when they are files of the archive.
func TestReadExtractedUpload_RefusesALinkedManifestOrMetadata(t *testing.T) {
	out := outsideTree(t)
	for name, entries := range map[string][]tentry{
		"metadata": {
			{typ: tar.TypeDir, name: stagedJob},
			{typ: tar.TypeDir, name: stagedJob + "/manifest"},
			{typ: tar.TypeReg, name: stagedJob + "/manifest/manifest.json", body: `{"user":{"username":"u"}}`},
			{typ: tar.TypeDir, name: stagedJob + "/meta"},
			{typ: tar.TypeSymlink, name: stagedJob + "/meta/metadata.json", link: out + "/metadata.json"},
		},
		"manifest folder": {
			{typ: tar.TypeDir, name: stagedJob},
			{typ: tar.TypeSymlink, name: stagedJob + "/manifest", link: out},
		},
		"manifest file": {
			{typ: tar.TypeDir, name: stagedJob},
			{typ: tar.TypeDir, name: stagedJob + "/manifest"},
			{typ: tar.TypeSymlink, name: stagedJob + "/manifest/manifest.json", link: out + "/manifest.json"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			staging := t.TempDir()
			if _, err := extractTarStream(buildTar(t, entries), staging); err != nil {
				t.Fatalf("extract: %v", err)
			}
			_, manifest, metadata, err := readExtractedUpload(staging)
			if err == nil {
				t.Fatalf("accepted (manifest %q, metadata %q)", manifest, metadata)
			}
		})
	}
}

func TestReadExtractedUpload_KeepsLinksInsideAHomeOrAppData(t *testing.T) {
	j := stagedJob + "/"
	app := j + "docker" + dockerAppDataRoot + "/app1"
	staging := extractUpload(t, []tentry{
		{typ: tar.TypeDir, name: j + "home/home/u/public_html"},
		{typ: tar.TypeSymlink, name: j + "home/home/u/public_html/current", link: "releases/2"},
		{typ: tar.TypeSymlink, name: j + "home/home/u/bin", link: "/opt/tool/bin"},
		{typ: tar.TypeSymlink, name: j + "home/home/u/.config/app", link: "../public_html"},
		{typ: tar.TypeDir, name: app + "/data"},
		{typ: tar.TypeSymlink, name: app + "/data/current", link: "v2"},
	})

	root, manifest, metadata, err := readExtractedUpload(staging)
	if err != nil {
		t.Fatalf("a backup whose links are inside the home and an app's data was refused: %v", err)
	}
	if root != filepath.Join(staging, stagedJob) {
		t.Errorf("root = %s, want the job dir", root)
	}
	if !strings.Contains(string(manifest), `"username":"u"`) || string(metadata) != `{"from":"archive"}` {
		t.Errorf("manifest %q, metadata %q: want the archive's own", manifest, metadata)
	}
}

func TestStageMaterializedInTar_IgnoresALinkedStage(t *testing.T) {
	root := t.TempDir()
	mustSymlink(t, t.TempDir(), filepath.Join(root, "db"))
	if stageMaterializedInTar(root, "db") {
		t.Error("a stage folder that is a link counts as materialized")
	}
}

func TestAccountRestore_ReadsNoDumpThroughALink(t *testing.T) {
	me := currentUsername(t)
	db := me + "_x"
	for name, setup := range map[string]func(t *testing.T, root, out string){
		"dump file": func(t *testing.T, root, out string) {
			mustWrite(t, filepath.Join(out, db+".sql"), "SELECT 1;")
			mustSymlink(t, filepath.Join(out, db+".sql"), filepath.Join(root, "db", db+".sql"))
		},
		"postgres dump file": func(t *testing.T, root, out string) {
			mustWrite(t, filepath.Join(out, db+".pgdump"), "x")
			mustSymlink(t, filepath.Join(out, db+".pgdump"), filepath.Join(root, "db", db+".pgdump"))
		},
		"stdin dump": func(t *testing.T, root, out string) {
			mustWrite(t, filepath.Join(out, "stdin"), "SELECT 1;")
			mustSymlink(t, filepath.Join(out, "stdin"), filepath.Join(root, "db", "stdin"))
		},
		"dump folder": func(t *testing.T, root, out string) {
			mustWrite(t, filepath.Join(out, db+".sql"), "SELECT 1;")
			mustSymlink(t, out, filepath.Join(root, "db"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			cmds := uploadExecRecorder(t)
			root, out := t.TempDir(), t.TempDir()
			setup(t, root, out)
			stages := []backup.ManifestStage{{Name: backup.StageDB, Items: []string{db}}}
			results := []backupRestoreStage{{Name: backup.StageDB, Status: backup.StageStatusOK}}
			enf := restoreEnforcement{Mode: restoreModeUpload, AllowedDBNames: []string{db}, DBPrefix: me + "_"}

			_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

			for _, c := range *cmds {
				if strings.Contains(c, "CREATE DATABASE") || strings.Contains(c, "createdb") || strings.Contains(c, "pg_restore") {
					t.Errorf("ran %q for a dump reached through a link (warnings %v)", c, warnings)
				}
			}
			if !hasWarning(warnings, "dump file not found") {
				t.Errorf("warnings %v should say the dump was not found", warnings)
			}
		})
	}
}

func TestAccountRestore_PrunesNoMailTreeThroughALink(t *testing.T) {
	uploadExecRecorder(t)
	me := currentUsername(t)
	root, out := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(out, "bob.org", "info", "cur", "1"), "x")
	mustWrite(t, filepath.Join(out, "alice.org", "info", "cur", "1"), "x")
	mustSymlink(t, out, filepath.Join(root, "mail"))
	stages := []backup.ManifestStage{{Name: backup.StageMail}}
	results := []backupRestoreStage{{Name: backup.StageMail, Status: backup.StageStatusOK}}
	enf := restoreEnforcement{Mode: restoreModeUpload, AllowedDBNames: []string{}, AllowedMailDomains: []string{"alice.org"}}

	_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	if _, err := os.Stat(filepath.Join(out, "bob.org")); err != nil {
		t.Errorf("a folder the mail stage reached through a link was removed: %v (warnings %v)", err, warnings)
	}
	if !hasWarning(warnings, "mail: no message tree") {
		t.Errorf("warnings %v should say the archive has no message tree", warnings)
	}
}

func TestAccountRestore_CopiesNoLinkedAppDataFolder(t *testing.T) {
	cmds := uploadExecRecorder(t)
	me := currentUsername(t)
	root, out, live := t.TempDir(), t.TempDir(), t.TempDir()
	prev := restoreDockerRoot
	restoreDockerRoot = live
	t.Cleanup(func() { restoreDockerRoot = prev })
	mustWrite(t, filepath.Join(out, "compose.yml"), "services: {}")
	mustSymlink(t, out, filepath.Join(root, backup.StageDocker, dockerAppDataRoot, "app1"))
	stages := []backup.ManifestStage{{Name: backup.StageDocker, Items: []string{"app1"}}}
	results := []backupRestoreStage{{Name: backup.StageDocker, Status: backup.StageStatusOK}}

	_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, restoreEnforcement{})

	for _, c := range *cmds {
		if strings.HasPrefix(c, "rsync ") {
			t.Errorf("ran %q for an app data folder that is a link (warnings %v)", c, warnings)
		}
	}
	if !hasWarning(warnings, "docker app1: source") {
		t.Errorf("warnings %v should say the app's source is missing", warnings)
	}
}

func TestStagedEntry(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a", "b", "f"), "x")
	mustSymlink(t, filepath.Join(root, "a"), filepath.Join(root, "la"))
	mustSymlink(t, filepath.Join(root, "a", "b", "f"), filepath.Join(root, "a", "lf"))
	for _, tc := range []struct {
		path    string
		wantDir bool
		ok      bool
	}{
		{"a/b", true, true},
		{"a/b/f", false, true},
		{"a/b/f", true, false}, // a file is not a dir
		{"a/b", false, false},  // a dir is not a file
		{"la/b", true, false},  // under a linked parent
		{"la/b/f", false, false},
		{"la", true, false}, // the link itself
		{"a/lf", false, false},
		{"a/missing", false, false},
		{"../x", true, false}, // outside the root
	} {
		err := stagedEntry(root, filepath.Join(root, tc.path), tc.wantDir)
		if (err == nil) != tc.ok {
			t.Errorf("stagedEntry(%s, dir=%v) = %v, want ok=%v", tc.path, tc.wantDir, err, tc.ok)
		}
	}
	if err := stagedEntry(root, root, true); err != nil {
		t.Errorf("the root itself: %v", err)
	}
	if err := stagedEntry(root, filepath.Dir(root), true); err == nil {
		t.Error("the folder above the root was accepted")
	}
	if err := stagedEntry(filepath.Join(root, "la"), filepath.Join(root, "la", "b"), true); err == nil {
		t.Error("a root that is a symlink was accepted")
	}
}

func TestResolveExtractedRoot_FollowsNoSymlink(t *testing.T) {
	out := outsideTree(t)
	for name, link := range map[string]string{
		"manifest folder":    stagedJob + "/manifest",
		"unwrapped manifest": "manifest",
		"manifest file":      stagedJob + "/manifest/manifest.json",
	} {
		t.Run(name, func(t *testing.T) {
			staging := t.TempDir()
			if err := os.MkdirAll(filepath.Join(staging, stagedJob), 0o755); err != nil {
				t.Fatal(err)
			}
			target := out
			if strings.HasSuffix(link, ".json") {
				target = filepath.Join(out, "manifest.json")
			}
			mustSymlink(t, target, filepath.Join(staging, link))
			if root, err := resolveExtractedRoot(staging); err == nil {
				t.Fatalf("resolved %s through a symlink", root)
			}
		})
	}
}

func TestReadStagedFile_FollowsNoSymlink(t *testing.T) {
	root, out := t.TempDir(), outsideTree(t)
	mustWrite(t, filepath.Join(root, "meta", "metadata.json"), "own")
	mustSymlink(t, filepath.Join(out, "metadata.json"), filepath.Join(root, "meta", "linked.json"))
	mustSymlink(t, out, filepath.Join(root, "other"))
	if b, err := readStagedFile(root, filepath.Join(root, "meta", "metadata.json")); err != nil || string(b) != "own" {
		t.Errorf("own file = %q, %v", b, err)
	}
	for _, p := range []string{"meta/linked.json", "other/metadata.json"} {
		if b, err := readStagedFile(root, filepath.Join(root, p)); err == nil {
			t.Errorf("%s read %q through a symlink", p, b)
		}
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
