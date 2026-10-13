package commands

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// moodle_data_owner_only_test.go — a Moodle install's data root is the
// tenant's alone. The domain directory is setgid www-data, so .moodledata and
// the data root came out group www-data, and Moodle's default directory mode
// (2777, with umask 0) made every stored file and directory writable by the
// web server's group, and by everyone who could reach it. Seen on the test
// box: data root 0777 group www-data, 101 directories 0777, 18 files 0666.

func TestPrepareMoodleDataRoot_TenantGroupOwnerOnly(t *testing.T) {
	var ran []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	const parent = "/home/bob/domains/example.com/.moodledata"
	const dataRoot = parent + "/mdrill"
	if err := prepareMoodleDataRoot(context.Background(), "bob", dataRoot); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"mkdir -p -m 0770 " + dataRoot,
		"chgrp --no-dereference bob " + parent + " " + dataRoot,
		"chmod 0750 " + parent,
	}
	if len(ran) != len(want) {
		t.Fatalf("ran %q, want %q", ran, want)
	}
	for i, w := range want {
		// As the tenant, so a link the tenant planted can't redirect it.
		if !strings.Contains(ran[i], "--uid=bob") || !strings.HasSuffix(ran[i], w) {
			t.Errorf("step %d ran %q, want %q as the tenant", i, ran[i], w)
		}
	}
}

// Moodle sets the umask for everything it writes from the directory mode, so
// 2770 also makes its files 0660: nothing for anyone outside the tenant.
func TestMoodleInstallRun_NewDataDirsOwnerOnly(t *testing.T) {
	run := moodleInstallRun(moodleInstallReq{OSUser: "bob", SiteURL: "https://example.com", SiteTitle: "Site"}, "/home/bob/domains/example.com/public_html", "/home/bob/domains/example.com/.moodledata/root")
	if !slices.Contains(run.Args, "--chmod=2770") {
		t.Fatalf("installer args %q lack --chmod=2770 (Moodle's default is 2777)", run.Args)
	}
}

// The handler has to use it: the installer can't run here (it downloads
// Moodle), so check the call in the source.
func TestMoodleInstallHandler_PreparesTheDataRoot(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "moodle_install.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "moodleInstallHandler" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "prepareMoodleDataRoot" {
					calls++
				}
			}
			return true
		})
	}
	if calls != 1 {
		t.Fatalf("moodleInstallHandler calls prepareMoodleDataRoot %d times, want 1", calls)
	}
}
