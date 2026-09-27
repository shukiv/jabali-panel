package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// GH #1790 follow-up: the tenant-docker retrofit runs compose in every app
// dir, and compose must not parse the app's .env there either (a password
// starting with a quote would abort the retrofit mid-way).
func TestDockerComposeAt_DisablesDotEnv(t *testing.T) {
	cmd := dockerComposeAt("/var/lib/jabali/docker-apps/x", "up", "-d")
	if !slices.Contains(cmd.Env, "COMPOSE_DISABLE_ENV_FILE=true") {
		t.Errorf("compose runs without COMPOSE_DISABLE_ENV_FILE=true")
	}
	want := []string{"docker", "compose", "-f", "/var/lib/jabali/docker-apps/x/compose.yml", "up", "-d"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("args = %q, want %q", cmd.Args, want)
	}
}

// Every `docker compose` exec in this package goes through dockerComposeAt.
func TestDockerCompose_OnlyThroughHelper(t *testing.T) {
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
		n := strings.Count(string(b), `"docker", "compose"`) + strings.Count(string(b), `[]string{"compose"`)
		if f == "docker_cmd.go" {
			n-- // dockerComposeAt itself
		}
		if n != 0 {
			t.Errorf("%s runs docker compose outside dockerComposeAt (%d site(s))", f, n)
		}
	}
}
