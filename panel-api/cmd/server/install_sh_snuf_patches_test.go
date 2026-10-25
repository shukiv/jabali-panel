package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const snufBuildDir = "../../../install/snuffleupagus/build"

func needPatchTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"bash", "patch", "sha256sum"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
}

// runApplyPatches runs apply-patches.sh from dir (a build dir holding the
// script and its patches/) and returns its stdout.
func runApplyPatches(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(dir, "apply-patches.sh")}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("apply-patches.sh %v: %v\n%s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// GH #2262: under PHP-FPM, Snuffleupagus read REMOTE_ADDR with getenv(),
// which never holds a FastCGI parameter, so every web incident logged
// 0.0.0.0. The shipped patch must apply to the pinned upstream source.
// The fixture is the first lines of src/sp_utils.c from Snuffleupagus
// v0.14.0 (LGPL-3.0), up to the end of get_ipaddr().
func TestSnufApplyPatches_PinnedSourceReadsTheClientIPFromTheSAPI(t *testing.T) {
	needPatchTools(t)
	head, err := os.ReadFile("testdata/snuffleupagus-0.14.0-sp_utils-head.c")
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "src", "sp_utils.c"), string(head))

	id := runApplyPatches(t, snufBuildDir, "--id")
	if len(id) != 16 {
		t.Fatalf("patch-set id = %q, want 16 hex characters", id)
	}
	if got := runApplyPatches(t, snufBuildDir, src); got != "+patch-"+id {
		t.Fatalf("suffix = %q, want %q", got, "+patch-"+id)
	}
	patched, err := os.ReadFile(filepath.Join(src, "src", "sp_utils.c"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(patched)
	sapi := strings.Index(body, `sapi_getenv("REMOTE_ADDR"`)
	env := strings.Index(body, `getenv("REMOTE_ADDR")`+";")
	if sapi < 0 || env < 0 || sapi > env {
		t.Fatalf("get_ipaddr does not ask the SAPI before getenv():\n%s", body)
	}
	if strings.Contains(body, `sapi_getenv("HTTP_X_FORWARDED_FOR"`) {
		t.Error("the client-chosen X-Forwarded-For must not go through the SAPI path")
	}
}

// A patch set that does not apply leaves the source as upstream shipped it,
// so the build still produces a working extension.
func TestSnufApplyPatches_AllOrNone(t *testing.T) {
	needPatchTools(t)
	build := t.TempDir()
	script, err := os.ReadFile(filepath.Join(snufBuildDir, "apply-patches.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(build, "apply-patches.sh"), string(script))
	writeFile(t, filepath.Join(build, "patches", "0001-good.patch"),
		"--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n")
	writeFile(t, filepath.Join(build, "patches", "0002-stale.patch"),
		"--- a/b.txt\n+++ b/b.txt\n@@ -1,2 +1,2 @@\n alpha\n-beta\n+BETA\n")

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "one\ntwo\n")
	writeFile(t, filepath.Join(src, "b.txt"), "alpha\ngamma\n") // upstream moved on

	id := runApplyPatches(t, build, "--id")
	if got := runApplyPatches(t, build, src); got != "+patch-failed-"+id {
		t.Fatalf("suffix = %q, want %q", got, "+patch-failed-"+id)
	}
	for name, want := range map[string]string{"a.txt": "one\ntwo\n", "b.txt": "alpha\ngamma\n"} {
		got, _ := os.ReadFile(filepath.Join(src, name))
		if string(got) != want {
			t.Errorf("%s = %q, want it untouched %q", name, got, want)
		}
	}
	entries, _ := os.ReadDir(src)
	if len(entries) != 2 {
		t.Errorf("source tree has %d entries, want the 2 it started with", len(entries))
	}
}

func TestSnufApplyPatches_NoPatches(t *testing.T) {
	needPatchTools(t)
	build := t.TempDir()
	script, err := os.ReadFile(filepath.Join(snufBuildDir, "apply-patches.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(build, "apply-patches.sh"), string(script))
	if got := runApplyPatches(t, build, "--id"); got != "" {
		t.Errorf("id with no patches = %q, want empty", got)
	}
	if got := runApplyPatches(t, build, t.TempDir()); got != "" {
		t.Errorf("suffix with no patches = %q, want empty", got)
	}
}

// The patch set is part of the build stamp, so an existing 0.14.0 build is
// rebuilt once, and the running FPM masters reload to pick it up.
func TestInstallSnuffleupagus_RebuildsOnANewPatchSetAndReloadsFPM(t *testing.T) {
	build, err := os.ReadFile(filepath.Join(snufBuildDir, "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`PATCH_ID="$(bash "$HERE/apply-patches.sh" --id)"`,
		`PATCH_SUFFIX="$(bash "$HERE/apply-patches.sh" "snuffleupagus-${SNUF_VERSION}")"`,
		`echo "${STAMP_BASE}${PATCH_SUFFIX}" > "$STAMP_FILE"`,
	} {
		if !strings.Contains(string(build), want) {
			t.Errorf("build.sh lacks %s", want)
		}
	}
	body := installShFunc(t, "install_snuffleupagus")
	for _, want := range []string{
		`|| _snuf_rebuilt=1`,
		`systemctl reload 'jabali-fpm@*.service'`,
		`build-essential libpcre2-dev patch`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("install_snuffleupagus lacks %s", want)
		}
	}
}
