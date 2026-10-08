package execstub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// userDir is where NoOpBinary keeps the stub for name under tmp.
func userDir(tmp, name string) string {
	return filepath.Join(tmp, fmt.Sprintf("jabali-%s-stub-%d", name, os.Getuid()))
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

// Every test binary a user runs shares one stub directory: running the tests
// again leaves nothing new behind.
func TestNoOpBinary_OneDirectoryPerUser(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	p1, err := NoOpBinary("tool")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := NoOpBinary("tool")
	if err != nil {
		t.Fatal(err)
	}

	if p1 != p2 || p1 != filepath.Join(userDir(tmp, "tool"), "tool") {
		t.Fatalf("stubs at %s and %s, want both at %s", p1, p2, filepath.Join(userDir(tmp, "tool"), "tool"))
	}
	if got := strings.Join(entries(t, tmp), ","); got != filepath.Base(userDir(tmp, "tool")) {
		t.Errorf("temp dir holds %s, want only the user's stub directory", got)
	}
	if got := strings.Join(entries(t, userDir(tmp, "tool")), ","); got != "tool" {
		t.Errorf("stub directory holds %s, want only the stub", got)
	}
	fi, err := os.Stat(userDir(tmp, "tool"))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("stub directory mode %v (%v), want 0700", fi.Mode().Perm(), err)
	}
	cmd := exec.Command(p1, "secure-zone", "example.com")
	cmd.Stdin = strings.NewReader("input the stub must drain\n")
	out, err := cmd.CombinedOutput()
	if err != nil || len(out) != 0 {
		t.Errorf("stub ran with %v and printed %q, want a silent exit 0", err, out)
	}
}

// A stub that was changed since it was written is written again.
func TestNoOpBinary_ReplacesAChangedStub(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	p, err := NoOpBinary("tool")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho changed\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := NoOpBinary("tool"); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(p); string(b) != noOpScript {
		t.Errorf("stub holds %q, want the no-op script", b)
	}
}

// The directory's name is known, so another user can make it first. A stub
// directory that others can write, or that is a link, is never used: the
// stub goes in a new private directory instead.
func TestNoOpBinary_NeverUsesADirectoryItCanNotTrust(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, dir string){
		"open to others": func(t *testing.T, dir string) {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o777); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "tool"), []byte("#!/bin/sh\necho planted\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"a link": func(t *testing.T, dir string) {
			// A directory that would pass every other check.
			target := t.TempDir()
			if err := os.Chmod(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "tool"), []byte("#!/bin/sh\necho planted\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, dir); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			dir := userDir(tmp, "tool")
			plant(t, dir)

			p, err := NoOpBinary("tool")
			if err != nil {
				t.Fatal(err)
			}

			if filepath.Dir(p) == dir {
				t.Fatalf("stub %s is in the untrusted directory", p)
			}
			if b, _ := os.ReadFile(p); string(b) != noOpScript {
				t.Errorf("stub holds %q, want the no-op script", b)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "tool")); !strings.Contains(string(b), "planted") {
				t.Errorf("the untrusted directory's file was changed: %q", b)
			}
		})
	}
}
