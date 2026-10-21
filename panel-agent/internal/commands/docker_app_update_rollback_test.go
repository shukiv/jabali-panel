package commands

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GH #1956: an installed Nextcloud answers "/" with a redirect to
// https://<its domain>/login. The probe followed it to TLS on the plain-HTTP
// port, so an update never passed its health wait and "rolled back".
func TestHttpServing_ARedirectIsServingNotFollowed(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("https://127.0.0.1:%d/login", serverPort(t, srv.URL)), http.StatusFound)
	}))
	defer srv.Close()
	if !httpServing(context.Background(), []int{serverPort(t, srv.URL)}) {
		t.Fatal("a redirect to https should count as serving")
	}
}

// A rollback brings the install up on the compose and .env it ran before the
// update, so the previous (digest-pinned) image runs again. It re-upped the
// new compose, leaving the new image running behind a "rolled_back" outcome.
func TestRollback_RestoresThePreviousCompose(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yml", "image: app:34@sha256:old\n")
	write(".env", "A=old\n")
	prev := readPriorComposeFiles(dir, true, true)
	write("compose.yml", "image: app:35@sha256:new\n")
	write(".env", "A=new\n")

	var upWith []string
	prevExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && strings.Join(args, " ") == "compose up -d" {
			b, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
			e, _ := os.ReadFile(filepath.Join(dir, ".env"))
			upWith = append(upWith, string(b)+string(e))
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prevExec })

	if err := rollback(context.Background(), dir, "sha256:oldimage", prev); err != nil {
		t.Fatal(err)
	}
	if len(upWith) != 1 || upWith[0] != "image: app:34@sha256:old\nA=old\n" {
		t.Fatalf("compose up ran with %q, want the previous compose and .env", upWith)
	}
	if fi, err := os.Stat(filepath.Join(dir, ".env")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode %v, %v; want 0600", fi.Mode().Perm(), err)
	}
}

// An update that kept the on-disk files (an auto-update) has nothing to put
// back, and still re-ups as before.
func TestRollback_KeptFilesStayAsTheyAre(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("image: app:1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	prev := readPriorComposeFiles(dir, false, false)
	ups := 0
	prevExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && strings.Join(args, " ") == "compose up -d" {
			ups++
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prevExec })
	if err := rollback(context.Background(), dir, "sha256:old", prev); err != nil || ups != 1 {
		t.Fatalf("err %v, ups %d", err, ups)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "compose.yml")); string(b) != "image: app:1\n" {
		t.Fatalf("compose changed: %q", b)
	}
}

// The update handler reads the files it replaces before it writes the new
// ones, and hands them to both rollbacks. Its data root is fixed, so this
// pins the wiring in source.
func TestDockerAppUpdate_KeepsThePriorFilesForRollback(t *testing.T) {
	b, err := os.ReadFile("docker_app_update.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	read := strings.Index(src, `prev := readPriorComposeFiles(dir, p.ComposeYML != "", p.EnvFile != "")`)
	write := strings.Index(src, `writeAtomicDockerApp(filepath.Join(dir, "compose.yml"), []byte(p.ComposeYML), 0o640)`)
	if read < 0 || write < 0 || read > write {
		t.Fatalf("the prior files must be read before the new compose is written (read at %d, write at %d)", read, write)
	}
	// The stop before a recreate, compose up and the health wait.
	if n := strings.Count(src, "_ = rollback(ctx, dir, oldImage, prev)"); n != 3 {
		t.Fatalf("%d rollbacks get the prior files, want 3", n)
	}
}

// An app whose new compose moves its data before its database starts
// (Rocket.Chat, MongoDB 6.0 → 8.0) has its old containers stopped after the
// pull, so the old database no longer holds the files and the old app no
// longer writes while the move runs. Without the flag nothing is stopped.
func TestRecreatePrep_StopsTheOldContainersAfterThePull(t *testing.T) {
	for _, tc := range []struct {
		stopFirst bool
		failOn    string
		want      []string
		wantStep  string
	}{
		{stopFirst: false, want: []string{"compose pull"}},
		{stopFirst: true, want: []string{"compose pull", "compose stop"}},
		{stopFirst: true, failOn: "compose pull", want: []string{"compose pull"}, wantStep: "pull"},
		{stopFirst: true, failOn: "compose stop", want: []string{"compose pull", "compose stop"}, wantStep: "stop"},
	} {
		var ran []string
		prevExec := execCommandContext
		execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			cmd := strings.Join(args, " ")
			if name == "docker" {
				ran = append(ran, cmd)
			}
			if cmd == tc.failOn {
				return exec.CommandContext(ctx, "false")
			}
			return exec.CommandContext(ctx, "true")
		}
		step, _, err := recreatePrep(context.Background(), t.TempDir(), tc.stopFirst)
		execCommandContext = prevExec
		if strings.Join(ran, ",") != strings.Join(tc.want, ",") || step != tc.wantStep || (err != nil) != (tc.wantStep != "") {
			t.Errorf("stopFirst=%v failOn=%q: ran %v, step %q, err %v; want %v, step %q", tc.stopFirst, tc.failOn, ran, step, err, tc.want, tc.wantStep)
		}
	}
}
