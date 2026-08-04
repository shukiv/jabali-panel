package commands

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// JAB-222 was an omission bug: one function staged its temp file in /tmp and
// renamed it into /etc, which works on a dev box and fails with EXDEV on any
// host where /tmp is a separate tmpfs. Nothing in the type system objects, the
// unit tests passed, and the failure only appeared on a production box — where
// it silently disabled a security control the operator believed was on.
//
// A point fix would not stop the next one, so this is a structural guard rather
// than a behavioural test: it re-derives the audit from the source on every run
// and fails on any NEW instance. The rule it enforces is narrow and mechanical —
// a function must not both create a temp file with an empty directory argument
// and call os.Rename. Those two together are the bug; either alone is fine.
func TestNoTempFileRenamedAcrossFilesystems(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	fset := token.NewFileSet()
	var offenders []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			var tempInDefaultDir, renames bool
			var tempPos token.Pos

			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "os" {
					return true
				}
				switch sel.Sel.Name {
				case "CreateTemp", "MkdirTemp":
					// Empty first argument means "use $TMPDIR / /tmp".
					if len(call.Args) > 0 {
						if lit, ok := call.Args[0].(*ast.BasicLit); ok &&
							(lit.Value == `""` || lit.Value == `"/tmp"`) {
							tempInDefaultDir = true
							tempPos = call.Pos()
						}
					}
				case "Rename":
					renames = true
				}
				return true
			})

			if tempInDefaultDir && renames {
				offenders = append(offenders, fset.Position(tempPos).String()+" in "+fn.Name.Name)
			}
			return true
		})
	}

	if len(offenders) > 0 {
		t.Fatalf("temp file created in the default temp dir and renamed in the same function — "+
			"rename(2) cannot cross filesystems and /tmp is often a separate tmpfs (JAB-222).\n"+
			"Stage the temp file in the target's directory instead, e.g. writeAtomic():\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// writeAtomic is the sanctioned helper. Pin the property that matters: the temp
// file must live on the same filesystem as the target, which for a same-dir
// staging path is true by construction.
func TestWriteAtomic_StagesInTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")

	if err := writeAtomic(target, []byte("SITE_KEY=abc\n"), 0o600); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "SITE_KEY=abc\n" {
		t.Errorf("content = %q", got)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	// No .tmp left behind.
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file survived: %v", err)
	}
}

// A leftover .tmp from an interrupted run must not donate its permissions to a
// file that carries a secret.
func TestWriteAtomic_TightensLeftoverTempPermissions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	if err := os.WriteFile(target+".tmp", []byte("stale"), 0o666); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := writeAtomic(target, []byte("SECRET_KEY=xyz\n"), 0o600); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret-bearing file left at %v — leftover temp permissions leaked through", fi.Mode().Perm())
	}
}
