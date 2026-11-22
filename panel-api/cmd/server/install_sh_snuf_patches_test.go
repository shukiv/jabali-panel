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

// writePinnedSnufSource lays out, under a temp dir, the parts of the pinned
// Snuffleupagus v0.14.0 source (LGPL-3.0) that the shipped patches touch:
// the first lines of src/sp_utils.c up to the end of get_ipaddr(), and an
// excerpt around each hunk of the other files. The patch set is all or
// none, so every file it touches has to be here. Returns the source root.
func writePinnedSnufSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	fixtures := map[string]string{
		"sp_utils.c":              "snuffleupagus-0.14.0-sp_utils-head.c",
		"sp_cookie_encryption.c":  "snuffleupagus-0.14.0-sp_cookie_encryption-excerpt.c",
		"sp_disabled_functions.c": "snuffleupagus-0.14.0-sp_disabled_functions-excerpt.c",
		"sp_execute.c":            "snuffleupagus-0.14.0-sp_execute-excerpt.c",
		"sp_unserialize.c":        "snuffleupagus-0.14.0-sp_unserialize-excerpt.c",
		"sp_var_value.c":          "snuffleupagus-0.14.0-sp_var_value-excerpt.c",
	}
	for name, fixture := range fixtures {
		body, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(src, "src", name), string(body))
	}
	return src
}

// GH #2262: under PHP-FPM, Snuffleupagus read REMOTE_ADDR with getenv(),
// which never holds a FastCGI parameter, so every web incident logged
// 0.0.0.0. The shipped patch must apply to the pinned upstream source.
func TestSnufApplyPatches_PinnedSourceReadsTheClientIPFromTheSAPI(t *testing.T) {
	needPatchTools(t)
	src := writePinnedSnufSource(t)

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

// Snuffleupagus 0.14.0 does not compile against PHP 8.6 (php_hash_bin2hex()
// is gone, internal arg_info[].name became a zend_string), which would leave
// 8.6 pools with no PHP Defense. The shipped patches must give each of
// those spots an 8.6 branch and keep the old code for 8.5 and earlier.
func TestSnufApplyPatches_PinnedSourceBuildsOnPHP86(t *testing.T) {
	needPatchTools(t)
	src := writePinnedSnufSource(t)
	id := runApplyPatches(t, snufBuildDir, "--id")
	if got := runApplyPatches(t, snufBuildDir, src); got != "+patch-"+id {
		t.Fatalf("suffix = %q, want %q", got, "+patch-"+id)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(src, "src", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	want := map[string][]string{
		"sp_unserialize.c": {"#if PHP_VERSION_ID < 80600", "php_hash_bin2hex(", "zend_bin2hex("},
		"sp_var_value.c": {"#if PHP_VERSION_ID < 80600",
			"arg_name = ZSTR_VAL(ed->func->internal_function.arg_info[i].name);"},
		"sp_disabled_functions.c": {"#if PHP_VERSION_ID < 80600",
			"return ZSTR_VAL(fn->internal_function.arg_info[i].name);"},
		"sp_execute.c":           {"ZEND_UNREACHABLE();"},
		"sp_cookie_encryption.c": {"*secure = zend_is_true(value);"},
	}
	for name, needles := range want {
		body := read(name)
		for _, n := range needles {
			if !strings.Contains(body, n) {
				t.Errorf("%s: missing %q after patching", name, n)
			}
		}
	}
	if strings.Contains(read("sp_cookie_encryption.c"), "zval_is_true(") {
		t.Error("sp_cookie_encryption.c still calls zval_is_true(), which PHP 8.6 dropped")
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
