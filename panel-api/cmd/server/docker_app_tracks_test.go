package main

import (
	"os"
	"strings"
	"testing"
)

// GH #1956: `jabali docker-app update` refuses an install the catalog has no
// image for before it calls the agent, as the admin API does; its on-disk
// fallback would otherwise "update" it and relabel it with the catalog's
// version. Every CLI re-render takes the install's target image, and the
// label follows the target's version. cmd/server has no DB/agent fixture, so
// this pins the wiring in source (the rules are tested in dockerapp).
func TestCLIDockerAppUpdate_FollowsTheReleaseTrack(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		return string(b)
	}
	src := read("docker_app_cmd.go")
	start := strings.Index(src, "func newDockerAppUpdateCmd() *cobra.Command {")
	end := strings.Index(src, "func newDockerAppBackupsCmd() *cobra.Command {")
	if start < 0 || end < start {
		t.Fatal("update command not found")
	}
	update := src[start:end]
	guard := strings.Index(update, `if target, terr = entry.TargetFor(app.CatalogVersion, true); terr != nil {
						return terr
					}`)
	call := strings.Index(update, `sharedAgent.Call(ctx, "docker_app.update", updateParams)`)
	if guard < 0 || call < 0 || guard > call {
		t.Fatalf("the update command must refuse an install without an update path before the agent call (guard at %d, call at %d)", guard, call)
	}
	if !strings.Contains(update, `_ = repo.UpdateCatalogVersion(ctx, app.ID, target.Version)`) || strings.Contains(update, "entry.Version") {
		t.Fatal("the update command must label the install with the target's version, not the catalog's")
	}
	// no_change heals the label too, but only after a re-render.
	if !strings.Contains(update, `(outc.Outcome == "updated" || outc.Outcome == "no_change") &&
				updateParams["compose_yml"] != nil && target.Version != ""`) {
		t.Fatal("the update command must label on updated or no_change, and only after a re-render")
	}
	for file, snippets := range map[string][]string{
		"docker_app_cmd.go": {
			`return renderInstallComposeCLI(ctx, repo, app, existingEnv, true)`,
			`target, err := entry.TargetFor(app.CatalogVersion, update)`,
			`ImageChannel: target.ImageChannel,`,
		},
		"docker_app_parity_cmd.go": {
			`renderInstallComposeCLI(ctx, repo, app, baseEnv, false)`,
			`entry.TargetFor(app.CatalogVersion, false)`,
			// The agent puts the previous env back on a rollback, so the
			// edit didn't take.
			`if outc.Outcome == "rolled_back" {
		return fmt.Errorf(`,
		},
	} {
		s := read(file)
		for _, want := range snippets {
			if !strings.Contains(s, want) {
				t.Errorf("%s: missing %q", file, want)
			}
		}
	}
}
