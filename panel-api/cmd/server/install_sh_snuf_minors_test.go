package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// installShFunc returns the source of one top-level function in install.sh.
func installShFunc(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../../install.sh")
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	m := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(name) + `\(\) \{\n.*?^\}\n`).Find(data)
	if m == nil {
		t.Fatalf("%s() not found in install.sh", name)
	}
	return string(m)
}

// install_snuffleupagus builds the extension for every PHP minor with an FPM
// binary. Debian and Sury name it php-fpm8.4; the old glob only knew
// php8.4-fpm, found nothing on a real box, and `jabali update` built 8.4
// alone, leaving 8.3 with no PHP Defense and 8.5 on an old build.
func TestSnufDetectFPMMinors(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	fn := installShFunc(t, "_snuf_detect_fpm_minors")

	dir := t.TempDir()
	for _, name := range []string{"php-fpm8.3", "php-fpm8.5", "php-fpm8.4", "php8.2-fpm"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Not an executable FPM binary: ignored.
	if err := os.WriteFile(filepath.Join(dir, "php-fpm8.1"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(arg string) string {
		t.Helper()
		// set -Eeuo pipefail as in install.sh: the function must not fail.
		out, err := exec.Command("bash", "-c", "set -Eeuo pipefail\n"+fn+"\n_snuf_detect_fpm_minors \"$1\"", "bash", arg).CombinedOutput()
		if err != nil {
			t.Fatalf("_snuf_detect_fpm_minors %s: %v\n%s", arg, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := run(dir); got != "8.2 8.3 8.4 8.5" {
		t.Errorf("minors = %q, want %q", got, "8.2 8.3 8.4 8.5")
	}
	if got := run(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("minors in a missing dir = %q, want empty", got)
	}

	body := installShFunc(t, "install_snuffleupagus")
	if !strings.Contains(body, `_detected_minors="$(_snuf_detect_fpm_minors)"`) {
		t.Error("install_snuffleupagus does not take its PHP minors from _snuf_detect_fpm_minors")
	}
}

// The reconciler renders every *.rules in /usr/share/jabali/snuffleupagus/
// rules, so the update-time sync must drop a file the repo no longer ships
// (the CMS overlays moved to pending/). A source without 00-base.rules is a
// broken checkout: the mirror stays as it is instead of rendering nothing.
func TestEnsureSnuffleupagusBundleSynced_Prunes(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	fn := installShFunc(t, "ensure_snuffleupagus_bundle_synced")
	sync := func(src, dst string) {
		t.Helper()
		script := "set -Eeuo pipefail\n_log(){ :; }\n" + fn + "\nensure_snuffleupagus_bundle_synced \"$1\" \"$2\""
		if out, err := exec.Command("bash", "-c", script, "bash", src, dst).CombinedOutput(); err != nil {
			t.Fatalf("sync: %v\n%s", err, out)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	src, dst := t.TempDir(), t.TempDir()
	write(filepath.Join(src, "00-base.rules"), "new\n")
	write(filepath.Join(dst, "00-base.rules"), "old\n")
	write(filepath.Join(dst, "10-wordpress.rules"), "stale overlay\n")
	write(filepath.Join(dst, "README.md"), "kept\n")
	sync(src, dst)
	if b, _ := os.ReadFile(filepath.Join(dst, "00-base.rules")); string(b) != "new\n" {
		t.Errorf("00-base.rules = %q, want the source copy", b)
	}
	if _, err := os.Stat(filepath.Join(dst, "10-wordpress.rules")); !os.IsNotExist(err) {
		t.Errorf("10-wordpress.rules still in the mirror (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "README.md")); err != nil {
		t.Errorf("README.md removed: %v", err)
	}

	broken := t.TempDir()
	sync(broken, dst)
	if _, err := os.Stat(filepath.Join(dst, "00-base.rules")); err != nil {
		t.Errorf("a source without 00-base.rules emptied the mirror: %v", err)
	}
}
