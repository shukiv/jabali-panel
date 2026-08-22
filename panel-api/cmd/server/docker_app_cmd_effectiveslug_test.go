package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestDockerAppCLI_AgentOpsUseEffectiveSlug pins the JAB-315 fix. Docker agent
// ops must address the container/data path by EffectiveSlug (instance_slug when
// set), never the base catalog Slug — otherwise a second install of the same
// catalog app (EffectiveSlug slug-2, slug-3, …) is addressed by its base slug and
// the op (status/start/stop/restart/delete/logs) hits the WRONG instance.
// cmd/server has no DB/agent fixture, so this source-pins the invariant.
func TestDockerAppCLI_AgentOpsUseEffectiveSlug(t *testing.T) {
	src, err := os.ReadFile("docker_app_cmd.go")
	if err != nil {
		t.Fatalf("read docker_app_cmd.go: %v", err)
	}
	s := string(src)
	// An agent slug param built from the raw catalog Slug is the bug.
	if regexp.MustCompile(`"slug":\s*app\.Slug\b`).MatchString(s) {
		t.Fatal(`a docker agent op still passes "slug": app.Slug — must be app.EffectiveSlug() so it targets the right instance (JAB-315)`)
	}
	// And the fixed ops must use EffectiveSlug.
	if !strings.Contains(s, "app.EffectiveSlug()") {
		t.Fatal("docker CLI agent ops must target app.EffectiveSlug() (JAB-315)")
	}
}
