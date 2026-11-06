package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// The exclude list is load-bearing (GH #646 invariant 2): if any jabali-managed
// file drops off it, a refresh clobbers the dest's DB creds / Redis integration.
func TestJabaliManagedRefreshExcludes(t *testing.T) {
	must := []string{
		"wp-config.php",
		"wp-content/object-cache.php",
		"wp-content/advanced-cache.php",
		".user.ini",
	}
	have := map[string]bool{}
	for _, e := range jabaliManagedRefreshExcludes {
		have[e] = true
	}
	for _, m := range must {
		if !have[m] {
			t.Errorf("refresh exclude list is missing load-bearing entry %q — a refresh would clobber it", m)
		}
	}
}

// refreshBox is a fake host for the refresh verbs: homes for tenant alice and
// tenant bob under a temp /home, a staging root with a job, and stubs for the
// account lookup, the commands and the tree chown.
type refreshBox struct {
	homes, stage           string
	aliceDoc, bobDoc, jobs string
	calls                  []string
	chowns                 []string
	cmds                   []*exec.Cmd
	// onExec, when set, runs as a command starts and may return a shell
	// snippet for it to run instead of true.
	onExec func(name string) string
}

func newRefreshBox(t *testing.T) *refreshBox {
	t.Helper()
	root := t.TempDir()
	b := &refreshBox{homes: filepath.Join(root, "home"), stage: filepath.Join(root, "stage")}
	b.aliceDoc = filepath.Join(b.homes, "alice", "domains", "a.test", "public_html")
	b.bobDoc = filepath.Join(b.homes, "bob", "domains", "b.test", "public_html")
	b.jobs = filepath.Join(b.stage, "job1")
	for _, d := range []string{b.aliceDoc, b.bobDoc, filepath.Join(b.jobs, "files")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(b.bobDoc, "index.html"):            "bob",
		filepath.Join(b.jobs, "files", "index.html"):     "staged",
		filepath.Join(b.jobs, "db.sql"):                  "CREATE TABLE t (i int);\n",
		filepath.Join(b.homes, "bob", "secret.sql"):      "bob's own",
		filepath.Join(b.homes, "alice", "notes.txt"):     "alice",
		filepath.Join(root, "outside", "x.sql"):          "outside",
		filepath.Join(root, "outside", "files", "x.txt"): "outside",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	accounts := map[string]*user.User{
		"alice":    {Username: "alice", Uid: "1001", Gid: "1001", HomeDir: filepath.Join(b.homes, "alice")},
		"bob":      {Username: "bob", Uid: "1002", Gid: "1002", HomeDir: filepath.Join(b.homes, "bob")},
		"www-data": {Username: "www-data", Uid: "33", Gid: "33", HomeDir: "/var/www"},
		"carol":    {Username: "carol", Uid: "1003", Gid: "1003", HomeDir: "/srv/carol"},
	}
	prevLookup, prevHome, prevChown, prevRoots, prevExec := lookupRefreshUser, refreshHomeRoot, refreshChownTree, migrationStagingRoots, execCommandContext
	lookupRefreshUser = func(name string) (*user.User, error) {
		if u, ok := accounts[name]; ok {
			return u, nil
		}
		return nil, user.UnknownUserError(name)
	}
	refreshHomeRoot = b.homes
	migrationStagingRoots = []string{b.stage}
	refreshChownTree = func(anchor, root string, uid, gid int) error {
		b.chowns = append(b.chowns, fmt.Sprintf("%s %s %d", anchor, root, uid))
		return nil
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		b.calls = append(b.calls, name+" "+strings.Join(args, " "))
		cmd := exec.CommandContext(ctx, "true")
		if name == "mysqldump" {
			cmd = exec.CommandContext(ctx, "echo", "-- dump")
		}
		if b.onExec != nil {
			if run := b.onExec(name); run != "" {
				cmd = exec.CommandContext(ctx, "sh", "-c", run)
			}
		}
		b.cmds = append(b.cmds, cmd)
		return cmd
	}
	t.Cleanup(func() {
		lookupRefreshUser, refreshHomeRoot, refreshChownTree, migrationStagingRoots, execCommandContext = prevLookup, prevHome, prevChown, prevRoots, prevExec
	})
	return b
}

func (b *refreshBox) call(t *testing.T, h func(context.Context, json.RawMessage) (any, error), params map[string]string) error {
	t.Helper()
	raw, _ := json.Marshal(params)
	_, err := h(context.Background(), raw)
	return err
}

func wantRefreshRefused(t *testing.T, b *refreshBox, what string, err error) {
	t.Helper()
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Errorf("%s: want invalid_argument, got %v", what, err)
	}
	if len(b.calls) != 0 || len(b.chowns) != 0 {
		t.Errorf("%s: a refused call must run nothing, ran %q, chowned %q", what, b.calls, b.chowns)
	}
	b.calls, b.chowns, b.cmds = nil, nil, nil
}

// wantPinned checks the i-th command ran in a held directory, not a path.
func (b *refreshBox) wantPinned(t *testing.T, i int) {
	t.Helper()
	if i >= len(b.cmds) || !strings.HasPrefix(b.cmds[i].Dir, "/proc/self/fd/") {
		t.Fatalf("command %d must run in a held directory, cmds %q", i, b.calls)
	}
}

// The refresh writes only inside the dest user's own home, and reads only
// from the migration staging roots.
func TestMigrationRefreshFiles_StaysInTheTenantsHomeAndTheStaging(t *testing.T) {
	b := newRefreshBox(t)
	files := filepath.Join(b.jobs, "files")
	link := filepath.Join(b.homes, "alice", "domains", "link.test")
	if err := os.Symlink(filepath.Dir(b.bobDoc), link); err != nil {
		t.Fatal(err)
	}
	stageLink := filepath.Join(b.jobs, "out")
	if err := os.Symlink(filepath.Join(filepath.Dir(b.stage), "outside", "files"), stageLink); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ what, user, docroot, source string }{
		{"another tenant's docroot", "alice", b.bobDoc, files},
		{"the home itself", "alice", filepath.Join(b.homes, "alice"), files},
		{"a link to another tenant's folder", "alice", filepath.Join(link, "public_html"), files},
		{"dot-segments", "alice", b.aliceDoc + "/../../../../bob/domains/b.test/public_html", files},
		{"a sibling home sharing the name's prefix", "alice", filepath.Join(b.homes, "alicex", "public_html"), files},
		{"a system account", "www-data", b.aliceDoc, files},
		{"a home outside /home", "carol", b.aliceDoc, files},
		{"an unknown account", "dave", b.aliceDoc, files},
		{"source outside the staging", "alice", b.aliceDoc, filepath.Dir(b.bobDoc)},
		{"source through a link out of the staging", "alice", b.aliceDoc, stageLink},
	} {
		err := b.call(t, migrationRefreshFilesHandler, map[string]string{"source": tc.source, "docroot": tc.docroot, "os_user": tc.user})
		wantRefreshRefused(t, b, tc.what, err)
	}
	if body, _ := os.ReadFile(filepath.Join(b.bobDoc, "index.html")); string(body) != "bob" {
		t.Fatalf("bob's site changed: %q", body)
	}

	if err := b.call(t, migrationRefreshFilesHandler, map[string]string{"source": files, "docroot": b.aliceDoc, "os_user": "alice"}); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 1 || !strings.HasPrefix(b.calls[0], "rsync ") || !strings.HasSuffix(b.calls[0], " "+files+"/ ./") {
		t.Fatalf("calls %q", b.calls)
	}
	b.wantPinned(t, 0)
	if want := filepath.Join(b.homes, "alice") + " " + b.aliceDoc + " 1001"; len(b.chowns) != 1 || b.chowns[0] != want {
		t.Fatalf("chowns %q, want %q", b.chowns, want)
	}
}

func TestMigrationRefreshDB_ReadsOnlyAStagedDump(t *testing.T) {
	b := newRefreshBox(t)
	stageLink := filepath.Join(b.jobs, "link.sql")
	if err := os.Symlink(filepath.Join(b.homes, "bob", "secret.sql"), stageLink); err != nil {
		t.Fatal(err)
	}
	for what, sql := range map[string]string{
		"a dump outside the staging":  filepath.Join(b.homes, "bob", "secret.sql"),
		"a link out of the staging":   stageLink,
		"a folder":                    filepath.Join(b.jobs, "files"),
		"a missing dump":              filepath.Join(b.jobs, "none.sql"),
		"dot-segments out of staging": filepath.Join(b.jobs) + "/../../home/bob/secret.sql",
	} {
		err := b.call(t, migrationRefreshDBHandler, map[string]string{"db_name": "alice_wp", "sql_path": sql})
		wantRefreshRefused(t, b, what, err)
	}
	if err := b.call(t, migrationRefreshDBHandler, map[string]string{"db_name": "alice_wp", "sql_path": filepath.Join(b.jobs, "db.sql")}); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 3 {
		t.Fatalf("calls %q", b.calls)
	}
}

// The pre-refresh dump is created new: a file or link the tenant left at its
// name fails the backup, and nothing is written through the link.
func TestMigrationRefreshBackup_NeverWritesThroughALink(t *testing.T) {
	b := newRefreshBox(t)
	const stamp = "20261106-120000"
	target := filepath.Join(filepath.Dir(b.stage), "outside", "x.sql")
	dump := filepath.Join(filepath.Dir(b.aliceDoc), "pre-refresh-"+stamp+".sql")

	t.Run("control: os.Create writes through the link", func(t *testing.T) {
		ctl := filepath.Join(t.TempDir(), "ctl.sql")
		if err := os.Symlink(target, ctl); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(target)
		f, err := os.Create(ctl)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if after, _ := os.ReadFile(target); len(after) != 0 || len(before) == 0 {
			t.Fatalf("expected os.Create to truncate the link target, before %q after %q", before, after)
		}
		if err := os.WriteFile(target, before, 0o644); err != nil {
			t.Fatal(err)
		}
	})

	if err := os.Symlink(target, dump); err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"docroot": b.aliceDoc, "os_user": "alice", "db_name": "alice_wp", "stamp": stamp}
	wantRefreshRefused(t, b, "a link at the dump's name", b.call(t, migrationRefreshBackupHandler, params))
	if body, _ := os.ReadFile(target); string(body) != "outside" {
		t.Fatalf("the link target changed: %q", body)
	}
	if err := os.Remove(dump); err != nil {
		t.Fatal(err)
	}

	params["docroot"] = b.bobDoc
	wantRefreshRefused(t, b, "another tenant's docroot", b.call(t, migrationRefreshBackupHandler, params))

	params["docroot"] = b.aliceDoc
	if err := b.call(t, migrationRefreshBackupHandler, params); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(dump)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dump %v %v", fi, err)
	}
	if body, _ := os.ReadFile(dump); string(body) != "-- dump\n" {
		t.Fatalf("dump body %q", body)
	}
	if len(b.calls) != 2 || b.calls[0] != "cp -al -- public_html public_html.pre-refresh-"+stamp {
		t.Fatalf("calls %q", b.calls)
	}
	b.wantPinned(t, 0)
}

// The docroot's folder swapped for a link to another tenant's after the
// check: the mirror still runs in the folder that was checked.
func TestMigrationRefreshFiles_ASwapAfterTheCheckIsNotFollowed(t *testing.T) {
	b := newRefreshBox(t)
	aDir := filepath.Dir(b.aliceDoc)
	b.onExec = func(name string) string {
		if name != "rsync" {
			return ""
		}
		if err := os.Rename(aDir, aDir+".real"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Dir(b.bobDoc), aDir); err != nil {
			t.Fatal(err)
		}
		return "echo mirrored > marker"
	}
	err := b.call(t, migrationRefreshFilesHandler, map[string]string{"source": filepath.Join(b.jobs, "files"), "docroot": b.aliceDoc, "os_user": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(b.bobDoc, "marker")); err == nil {
		t.Fatal("the mirror followed the link into bob's docroot")
	}
	if body, _ := os.ReadFile(filepath.Join(aDir+".real", "public_html", "marker")); string(body) != "mirrored\n" {
		t.Fatalf("the mirror did not run in alice's checked docroot: %q", body)
	}
}

func TestMigrationRefreshReconcile_StaysInTheTenantsHome(t *testing.T) {
	b := newRefreshBox(t)
	for what, tc := range map[string][2]string{
		"another tenant's install": {"alice", b.bobDoc},
		"a system account":         {"www-data", b.aliceDoc},
		"the home itself":          {"alice", filepath.Join(b.homes, "alice")},
	} {
		err := b.call(t, migrationRefreshReconcileHandler, map[string]string{"os_user": tc[0], "install_path": tc[1]})
		wantRefreshRefused(t, b, what, err)
	}
}
