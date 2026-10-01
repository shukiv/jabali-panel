package commands

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Go toolchain is pinned in three places that must move together:
// go.mod's toolchain line (GOTOOLCHAIN=auto builds with it), install.sh's
// GO_VERSION (what a box installs, and what `jabali update` builds with), and
// the CI workflows. A bump that misses one builds CI and the boxes with
// different compilers.
func TestGoToolchainPinsAgree(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}

	m := regexp.MustCompile(`(?m)^toolchain go([0-9]+\.[0-9]+\.[0-9]+)$`).FindStringSubmatch(read("go.mod"))
	if m == nil {
		t.Fatal("go.mod has no toolchain line")
	}
	want := m[1]

	m = regexp.MustCompile(`(?m)^go ([0-9]+\.[0-9]+)(\.[0-9]+)?$`).FindStringSubmatch(read("go.mod"))
	if m == nil || !strings.HasPrefix(want, m[1]+".") {
		t.Errorf("go.mod go line %v is not the toolchain's minor (%s)", m, want)
	}

	install := read("install.sh")
	m = regexp.MustCompile(`(?m)^GO_VERSION="\$\{JABALI_GO_VERSION:-([0-9.]+)\}"$`).FindStringSubmatch(install)
	if m == nil {
		t.Fatal("install.sh GO_VERSION pin not found")
	}
	if m[1] != want {
		t.Errorf("install.sh GO_VERSION = %s, go.mod toolchain = %s", m[1], want)
	}
	for _, v := range []string{"GO_SHA256_AMD64", "GO_SHA256_ARM64"} {
		if !regexp.MustCompile(`(?m)^` + v + `="\$\{JABALI_` + v + `:-[0-9a-f]{64}\}"$`).MatchString(install) {
			t.Errorf("install.sh %s is not a pinned sha256", v)
		}
	}

	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil || len(workflows) == 0 {
		t.Fatalf("no workflows found (err=%v)", err)
	}
	pinRe := regexp.MustCompile(`(?m)^\s*(GO_VERSION|go-version|default):\s*"([0-9]+\.[0-9]+\.[0-9]+)"`)
	seen := 0
	for _, f := range workflows {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pinRe.FindAllStringSubmatch(string(b), -1) {
			seen++
			if p[2] != want {
				t.Errorf("%s: %s %q, go.mod toolchain = %s", filepath.Base(f), p[1], p[2], want)
			}
		}
	}
	if seen == 0 {
		t.Error("no Go version pins found in .github/workflows; the check is no longer checking anything")
	}
}

// ensure_go_toolchain_current replaces $GO_ROOT during `jabali update`, before
// the update compiles anything with it. A failed download, a bad checksum or a
// broken tarball must leave the installed Go untouched.
func TestEnsureGoToolchainCurrent(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := installShFunctionBodies(string(raw))["ensure_go_toolchain_current"]
	if !ok {
		t.Fatal("ensure_go_toolchain_current() not found in install.sh")
	}
	fn := "ensure_go_toolchain_current() {" + body + "\n}\n"

	// fakeGo writes a go binary that reports version v.
	fakeGo := func(t *testing.T, dir, v string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\necho \"go version go" + v + " linux/amd64\"\n"
		if err := os.WriteFile(filepath.Join(dir, "bin", "go"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// tarball packs a go/ tree whose binary reports version v, like go.dev's.
	tarball := func(t *testing.T, v string) (path, sum string) {
		t.Helper()
		path = filepath.Join(t.TempDir(), "go.tar.gz")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		script := []byte("#!/bin/sh\necho \"go version go" + v + " linux/amd64\"\n")
		for _, h := range []*tar.Header{
			{Name: "go/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "go/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "go/bin/go", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(script))},
		} {
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tw.Write(script); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(b)
		return path, hex.EncodeToString(h[:])
	}
	// run calls the function with curl and uname stubbed out. curl copies
	// $FAKE_TARBALL to its -o path, or fails when FAKE_TARBALL is empty;
	// every call is logged to $CURL_LOG.
	run := func(t *testing.T, goRoot, fakeTarball, sha string) (curlCalled bool) {
		t.Helper()
		curlLog := filepath.Join(t.TempDir(), "curl.log")
		script := `set -Eeuo pipefail
_log(){ :; }; _warn(){ echo "WARN: $*" >&2; }; _ok(){ :; }
uname(){ echo x86_64; }
curl(){
  echo "$*" >> "$CURL_LOG"
  local out=""
  while [[ $# -gt 0 ]]; do [[ "$1" == "-o" ]] && out="$2"; shift; done
  [[ -n "$FAKE_TARBALL" ]] || return 22
  cp "$FAKE_TARBALL" "$out"
}
GO_VERSION=1.99.1
GO_SHA256_AMD64="$FAKE_SHA"
GO_SHA256_ARM64=""
GO_ROOT="$FAKE_GO_ROOT"
` + fn + `
ensure_go_toolchain_current
`
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(),
			"FAKE_TARBALL="+fakeTarball, "FAKE_SHA="+sha, "FAKE_GO_ROOT="+goRoot, "CURL_LOG="+curlLog)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ensure_go_toolchain_current failed: %v\n%s", err, out)
		}
		_, err := os.Stat(curlLog)
		return err == nil
	}
	version := func(t *testing.T, goRoot string) string {
		t.Helper()
		out, err := exec.Command(filepath.Join(goRoot, "bin", "go"), "version").Output()
		if err != nil {
			t.Fatalf("%s/bin/go: %v", goRoot, err)
		}
		return strings.Fields(string(out))[2]
	}
	leftovers := func(goRoot string) []string {
		m, _ := filepath.Glob(filepath.Join(filepath.Dir(goRoot), ".jabali-go-stage.*"))
		return m
	}
	good, goodSum := tarball(t, "1.99.1")

	t.Run("already current", func(t *testing.T) {
		goRoot := filepath.Join(t.TempDir(), "go")
		fakeGo(t, goRoot, "1.99.1")
		if run(t, goRoot, good, goodSum) {
			t.Error("downloaded Go although the pinned version is installed")
		}
	})
	t.Run("download fails", func(t *testing.T) {
		goRoot := filepath.Join(t.TempDir(), "go")
		fakeGo(t, goRoot, "1.25.1")
		run(t, goRoot, "", goodSum)
		if v := version(t, goRoot); v != "go1.25.1" {
			t.Errorf("installed Go = %s after a failed download, want go1.25.1 kept", v)
		}
	})
	t.Run("checksum mismatch", func(t *testing.T) {
		goRoot := filepath.Join(t.TempDir(), "go")
		fakeGo(t, goRoot, "1.25.1")
		run(t, goRoot, good, strings.Repeat("0", 64))
		if v := version(t, goRoot); v != "go1.25.1" {
			t.Errorf("installed Go = %s after a checksum mismatch, want go1.25.1 kept", v)
		}
		if l := leftovers(goRoot); len(l) > 0 {
			t.Errorf("stage left behind: %v", l)
		}
	})
	t.Run("tarball holds another version", func(t *testing.T) {
		goRoot := filepath.Join(t.TempDir(), "go")
		fakeGo(t, goRoot, "1.25.1")
		other, otherSum := tarball(t, "1.98.0")
		run(t, goRoot, other, otherSum)
		if v := version(t, goRoot); v != "go1.25.1" {
			t.Errorf("installed Go = %s, want go1.25.1 kept", v)
		}
		if l := leftovers(goRoot); len(l) > 0 {
			t.Errorf("stage left behind: %v", l)
		}
	})
	t.Run("upgrade", func(t *testing.T) {
		goRoot := filepath.Join(t.TempDir(), "go")
		fakeGo(t, goRoot, "1.25.1")
		if err := os.WriteFile(filepath.Join(goRoot, "stale-file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if !run(t, goRoot, good, goodSum) {
			t.Fatal("no download attempted")
		}
		if v := version(t, goRoot); v != "go1.99.1" {
			t.Errorf("installed Go = %s, want go1.99.1", v)
		}
		if _, err := os.Stat(filepath.Join(goRoot, "stale-file")); !os.IsNotExist(err) {
			t.Errorf("old tree survived the swap (stale-file err=%v)", err)
		}
		if l := leftovers(goRoot); len(l) > 0 {
			t.Errorf("stage left behind: %v", l)
		}
	})
	t.Run("no Go installed", func(t *testing.T) {
		goRoot := filepath.Join(t.TempDir(), "go")
		run(t, goRoot, good, goodSum)
		if v := version(t, goRoot); v != "go1.99.1" {
			t.Errorf("installed Go = %s, want go1.99.1", v)
		}
	})
}
