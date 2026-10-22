package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GH #357: fileSHA256 backs the requirements-hash marker that stops
// app.python.apply re-running `pip install -r requirements.txt` on every ~60s
// reconcile tick. It must be stable for identical content and differ when the
// file changes — that difference is exactly what re-triggers pip.
func TestFileSHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "requirements.txt")

	if err := os.WriteFile(p, []byte("Django==5.0\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, err := fileSHA256(p)
	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}
	if a == "" {
		t.Fatal("empty hash")
	}

	// Same content → same hash (a converged app skips pip).
	b, err := fileSHA256(p)
	if err != nil {
		t.Fatalf("fileSHA256 (2): %v", err)
	}
	if a != b {
		t.Fatalf("hash not stable for identical content: %s != %s", a, b)
	}

	// Changed content → different hash (tenant edited requirements → re-pip).
	if err := os.WriteFile(p, []byte("Django==5.0\nwagtail==6.0\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	c, err := fileSHA256(p)
	if err != nil {
		t.Fatalf("fileSHA256 (3): %v", err)
	}
	if c == a {
		t.Fatal("hash unchanged after content change — marker would wrongly skip pip")
	}

	// Missing file → error (caller stats first; this guards the contract).
	if _, err := fileSHA256(filepath.Join(dir, "nope.txt")); err == nil {
		t.Fatal("expected error hashing a missing file")
	}
}

// runAsUserInDir leaves the change of directory to the user's own process
// (env -C), so the path is resolved with the user's permissions: the agent's
// own working directory never moves into a tenant path.
func TestRunAsUserInDir_UserChangesDirectory(t *testing.T) {
	var argv []string
	var cmds []*exec.Cmd
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		argv = append([]string{name}, args...)
		c := exec.CommandContext(ctx, "true")
		cmds = append(cmds, c)
		return c
	}
	t.Cleanup(func() { execCommandContext = prev })

	if _, err := runAsUserInDir(context.Background(), "alice", "/home/alice/app", "django-admin", "startproject", "config", "."); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(argv, " "), "sudo -u alice -H /usr/bin/env -C /home/alice/app -- django-admin startproject config ."; got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if cmds[0].Dir != "" {
		t.Fatalf("the agent changed into %q itself", cmds[0].Dir)
	}

	// No directory: the command runs as before, without env.
	if _, err := runAsUser(context.Background(), "alice", "mkdir", "-p", "/home/alice/app"); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(argv, " "), "sudo -u alice -H mkdir -p /home/alice/app"; got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// The env -C form runAsUserInDir builds works with the host's coreutils.
func TestEnvBinChangesDirectory(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(envBin, "-C", dir, "--", "pwd", "-P").CombinedOutput()
	if err != nil {
		t.Skipf("%s -C unavailable here: %v: %s", envBin, err, out)
	}
	if got := strings.TrimSpace(string(out)); got != dir {
		t.Fatalf("pwd = %q, want %q", got, dir)
	}
}

// A changed env file restarts the app (GH #357), so the same variables must
// render to the same bytes on every apply, in key order, whatever order the
// map hands them out in.
func TestRenderEnvFile_SameBytesEveryApply(t *testing.T) {
	p := pythonAppApplyParams{AppType: "wsgi", BaseURI: "/app", Env: map[string]string{
		"SECRET_KEY": "s", "DJANGO_ALLOWED_HOSTS": "example.com", "DEBUG": "0",
		"DATABASE_URL": "mysql://x", "EMAIL_HOST": "localhost", "B_FLAG": "1", "bad key": "dropped",
	}}
	want := "SCRIPT_NAME=/app\nB_FLAG=1\nDATABASE_URL=mysql://x\nDEBUG=0\nDJANGO_ALLOWED_HOSTS=example.com\nEMAIL_HOST=localhost\nSECRET_KEY=s\n"
	for i := 0; i < 50; i++ {
		if got := renderEnvFile(p); got != want {
			t.Fatalf("render %d:\n%s\nwant:\n%s", i, got, want)
		}
	}
}
