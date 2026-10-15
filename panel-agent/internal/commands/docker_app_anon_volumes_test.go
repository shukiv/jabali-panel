package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A deleted app takes the anonymous volumes of its containers with it.
// `docker compose down -v` removes an anonymous volume only with the
// container that made it; after a recreate (an update, an edit, a changed
// env) compose mounts the old container's anonymous volumes into the new one
// by name, and the delete left them on disk. Seen on a test server: one
// anonymous volume left per deleted app that had been edited once.

// fakeDocker answers the docker commands a delete runs and records every
// call. down fails when downFails is set.
type fakeDocker struct {
	calls     []string
	downFails bool
	outputs   map[string]string // joined args -> stdout
}

func (f *fakeDocker) install(t *testing.T) {
	t.Helper()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		call := strings.Join(args, " ")
		f.calls = append(f.calls, call)
		if call == "compose down -v" && f.downFails {
			return exec.CommandContext(ctx, "false")
		}
		if out, ok := f.outputs[call]; ok {
			return exec.CommandContext(ctx, "printf", "%s", out)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
}

func (f *fakeDocker) index(call string) int {
	for i, c := range f.calls {
		if c == call {
			return i
		}
	}
	return -1
}

func (f *fakeDocker) removed() []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "volume rm ") {
			out = append(out, strings.TrimPrefix(c, "volume rm "))
		}
	}
	return out
}

// A recreated app: two containers, an anonymous volume each, a named volume
// from the compose file and a bind mount.
func recreatedApp() *fakeDocker {
	return &fakeDocker{outputs: map[string]string{
		"compose ps -a -q": "c1\nc2\n",
		"inspect --type container c1 c2": `[
			{"Mounts":[{"Type":"volume","Name":"anon1"},{"Type":"volume","Name":"proj_data"},{"Type":"bind","Name":""}]},
			{"Mounts":[{"Type":"volume","Name":"anon2"}]}]`,
		"volume inspect anon1 proj_data anon2": `[
			{"Name":"anon1","Labels":{"com.docker.volume.anonymous":""}},
			{"Name":"proj_data","Labels":{"com.docker.compose.project":"proj","com.docker.compose.volume":"data"}},
			{"Name":"anon2","Labels":{"com.docker.volume.anonymous":""}}]`,
	}}
}

func appDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services: {}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDockerAppDelete_PurgeRemovesTheContainersAnonymousVolumes(t *testing.T) {
	f := recreatedApp()
	f.install(t)
	dir := appDir(t)

	if _, err := deleteDockerAppAt(context.Background(), dir, dockerAppLifecycleParams{Slug: "app", PurgeVolumes: true}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got := f.removed()
	if strings.Join(got, " ") != "anon1 anon2" {
		t.Fatalf("removed volumes %v, want the two anonymous ones (calls %v)", got, f.calls)
	}
	// Removed once down has removed the containers that mount them.
	if down, rm := f.index("compose down -v"), f.index("volume rm anon1"); down < 0 || rm < down {
		t.Fatalf("volume rm before compose down: %v", f.calls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("install dir still there: %v", err)
	}
}

// A delete that keeps the app's data keeps these volumes too.
func TestDockerAppDelete_KeepVolumesKeepsTheAnonymousVolumes(t *testing.T) {
	f := recreatedApp()
	f.install(t)

	if _, err := deleteDockerAppAt(context.Background(), appDir(t), dockerAppLifecycleParams{Slug: "app"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := f.removed(); len(got) != 0 {
		t.Fatalf("a keep-volumes delete removed %v", got)
	}
}

// A failed down may leave the containers running on those volumes.
func TestDockerAppDelete_AFailedDownRemovesNoVolume(t *testing.T) {
	f := recreatedApp()
	f.downFails = true
	f.install(t)

	if _, err := deleteDockerAppAt(context.Background(), appDir(t), dockerAppLifecycleParams{Slug: "app", PurgeVolumes: true}); err == nil {
		t.Fatal("a failed down returned no error")
	}
	if got := f.removed(); len(got) != 0 {
		t.Fatalf("volumes %v removed after a failed down", got)
	}
}

// Only volumes a container mounts and docker labels anonymous are listed; a
// name that could read as a flag never reaches `docker volume rm`.
func TestComposeAnonymousVolumes_OnlyAnonymousMountedNames(t *testing.T) {
	f := &fakeDocker{outputs: map[string]string{
		"compose ps -a -q":            `c1`,
		"inspect --type container c1": `[{"Mounts":[{"Type":"volume","Name":"--force"},{"Type":"volume","Name":"anon1"},{"Type":"volume","Name":"ext1"}]}]`,
		"volume inspect anon1 ext1":   `[{"Name":"anon1","Labels":{"com.docker.volume.anonymous":""}},{"Name":"ext1","Labels":null},{"Name":"other","Labels":{"com.docker.volume.anonymous":""}}]`,
	}}
	f.install(t)
	if got := composeAnonymousVolumes(context.Background(), t.TempDir()); strings.Join(got, " ") != "anon1" {
		t.Fatalf("listed %v, want [anon1] (calls %v)", got, f.calls)
	}
}

// Any docker error lists nothing, so nothing is removed.
func TestComposeAnonymousVolumes_ADockerErrorListsNothing(t *testing.T) {
	for _, failing := range []string{"compose ps -a -q", "inspect --type container c1", "volume inspect anon1"} {
		f := &fakeDocker{outputs: map[string]string{
			"compose ps -a -q":            "c1",
			"inspect --type container c1": `[{"Mounts":[{"Type":"volume","Name":"anon1"}]}]`,
			"volume inspect anon1":        `[{"Name":"anon1","Labels":{"com.docker.volume.anonymous":""}}]`,
		}}
		delete(f.outputs, failing)
		prev := execCommandContext
		execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			call := strings.Join(args, " ")
			if call == failing {
				return exec.CommandContext(ctx, "false")
			}
			if out, ok := f.outputs[call]; ok {
				return exec.CommandContext(ctx, "printf", "%s", out)
			}
			return exec.CommandContext(ctx, "true")
		}
		got := composeAnonymousVolumes(context.Background(), t.TempDir())
		execCommandContext = prev
		if len(got) != 0 {
			t.Fatalf("with %q failing, listed %v", failing, got)
		}
	}
}
