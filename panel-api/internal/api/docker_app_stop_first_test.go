package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// stopFirstCatalog is a one-app admin catalog whose entry sets
// stop_before_recreate as asked.
func stopFirstCatalog(t *testing.T, stop bool) *dockerapp.Catalog {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "sdemo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	app := `slug: sdemo
name: SDemo
version: "1.0.0"
description: an app whose data moves before its database starts
image_channel: docker.io/library/busybox:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
volumes:
  - name: data
    container_path: /data
ports:
  - name: http
    container_port: 8080
    protocol: tcp
    default_enabled: true
    default_bind: loopback
`
	if stop {
		app += "stop_before_recreate: true\n"
	}
	for name, body := range map[string]string{
		"app.yaml":         app,
		"compose.yml.tmpl": "services:\n  sdemo:\n    image: {{ .ImageChannel }}\n",
		"icon.svg":         `<svg/>`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cat, errs := dockerapp.LoadDir(root)
	if len(errs) > 0 {
		t.Fatalf("temp catalog load errors: %v", errs)
	}
	return cat
}

// An env edit re-renders and recreates the app through docker_app.update. For
// an entry with stop_before_recreate it asks the agent to stop the old
// containers first; for any other entry it does not.
func TestApplyEnv_StopFirstOnlyWhenTheCatalogAsks(t *testing.T) {
	for _, stop := range []bool{true, false} {
		mock := agent.NewMockClient()
		mock.On("docker_app.update", map[string]any{"outcome": "updated"})
		h := &dockerAppHandler{cfg: DockerAppHandlerConfig{
			Catalog: stopFirstCatalog(t, stop),
			Repo:    &fakeDockerRepo{},
			Agent:   mock,
		}}
		if err := h.applyEnv(context.Background(), &models.DockerApp{ID: "a1", Slug: "sdemo"}, map[string]string{}); err != nil {
			t.Fatalf("applyEnv: %v", err)
		}
		p := lastAgentParams(t, mock, "docker_app.update")
		if got := p["stop_first"] == true; got != stop {
			t.Fatalf("stop_before_recreate=%v: stop_first sent = %v (%v)", stop, got, p["stop_first"])
		}
	}
}

// The update and domain/port edit doors run through gin and a background
// goroutine, so pin the wiring in source.
func TestDockerAppDoors_SendStopFirst(t *testing.T) {
	b, err := os.ReadFile("docker_apps.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"h.setStopFirst(app, updateParams)",
		"h.setStopFirst(app, installParams)",
	} {
		if !strings.Contains(string(b), s) {
			t.Errorf("docker_apps.go no longer calls %s", s)
		}
	}
}

// The CLI's update and env doors recreate the app through docker_app.update
// too.
func TestDockerAppCLIDoors_SendStopFirst(t *testing.T) {
	for file, call := range map[string]string{
		"../../cmd/server/docker_app_cmd.go":        "cliSetStopFirst(app, updateParams)",
		"../../cmd/server/docker_app_parity_cmd.go": "cliSetStopFirst(app, params)",
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), call) {
			t.Errorf("%s no longer calls %s", file, call)
		}
	}
}
