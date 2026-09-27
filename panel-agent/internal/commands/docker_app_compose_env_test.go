package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// GH #1790 follow-up: compose must never parse the project's .env. The file
// holds the app's env values verbatim, and compose's dotenv parser fails every
// command on some values that start with a quote (an unclosed ' or ", or a
// closing quote followed by more text). Each compose call carries
// COMPOSE_DISABLE_ENV_FILE=true.
func TestComposeCalls_DisableDotEnv(t *testing.T) {
	var cmds []*exec.Cmd
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, "true")
		cmds = append(cmds, c)
		return c
	}
	t.Cleanup(func() { execCommandContext = prev })

	ctx := context.Background()
	dir := t.TempDir()
	if _, err := runDockerCompose(ctx, dir, "ps", "--services"); err != nil {
		t.Fatalf("runDockerCompose: %v", err)
	}
	// Empty output fails JSON validation; only the command's env matters here.
	_ = runTenantComposeValidation(ctx, dir, nil, "jabali-user-t.slice", nil)

	if len(cmds) != 2 {
		t.Fatalf("expected 2 compose commands, got %d", len(cmds))
	}
	for i, c := range cmds {
		if !slices.Contains(c.Env, "COMPOSE_DISABLE_ENV_FILE=true") {
			t.Errorf("compose command %d runs without COMPOSE_DISABLE_ENV_FILE=true", i)
		}
		if c.Dir != dir {
			t.Errorf("compose command %d runs in %q, want %q", i, c.Dir, dir)
		}
	}
}

// Every `docker compose` exec in this package goes through composeCommand, so
// a new call site (or the exec handler, whose data root is a const) cannot
// skip the .env guard.
func TestComposeCalls_OnlyThroughComposeCommand(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		n := strings.Count(src, `"docker", "compose"`) + strings.Count(src, `[]string{"compose"}`)
		if f == "docker_app.go" {
			n-- // composeCommand itself
		}
		if n != 0 {
			t.Errorf("%s runs docker compose outside composeCommand (%d site(s))", f, n)
		}
	}
}
