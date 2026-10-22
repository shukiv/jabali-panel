package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// GH #2156: kind=source scaffolds unpack a full upstream app (sic) from a
// pinned GitHub commit tarball, and system_libs installs the shared library
// an app loads (libMagickWand for Wand) on demand.

// sourceArchive builds a tar.gz with one top-level dir, like a codeload
// tarball, and returns it with its sha256.
func sourceArchive(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "app-0123/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "app-0123/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// sourceServer serves archive at /a.tar.gz (and a redirect at /moved) over
// TLS, and points the fetch at it for the test.
func sourceServer(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.tar.gz":
			_, _ = w.Write(archive)
		case "/moved":
			http.Redirect(w, r, "/a.tar.gz", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	prevTransport, prevRe := pythonSourceTransport, pythonSourceURLRe
	pythonSourceTransport = srv.Client().Transport
	pythonSourceURLRe = regexp.MustCompile(`^` + regexp.QuoteMeta(srv.URL) + `/[a-z.]+$`)
	t.Cleanup(func() { pythonSourceTransport, pythonSourceURLRe = prevTransport, prevRe })
	return srv
}

func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "jabali-pysrc-*"))
	return m
}

func TestFetchPythonSource_VerifiesTheArchive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	archive, sum := sourceArchive(t, map[string]string{"manage.py": "print(1)\n"})
	srv := sourceServer(t, archive)

	path, err := fetchPythonSource(context.Background(), srv.URL+"/a.tar.gz", sum)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, archive) {
		t.Fatal("the fetched file isn't the archive")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode %v, want 0600", fi.Mode().Perm())
	}
	os.Remove(path)

	other := strings.Repeat("0", 64)
	if _, err := fetchPythonSource(context.Background(), srv.URL+"/a.tar.gz", other); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a wrong pin: err %v, want a mismatch", err)
	}
	if left := tempFiles(t, tmp); len(left) != 0 {
		t.Fatalf("a rejected archive was left on disk: %v", left)
	}
}

func TestFetchPythonSource_RefusesRedirectsErrorsAndOversize(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	archive, sum := sourceArchive(t, map[string]string{"manage.py": strings.Repeat("x", 4096)})
	srv := sourceServer(t, archive)

	if _, err := fetchPythonSource(context.Background(), srv.URL+"/moved", sum); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("a redirect: err %v, want it refused", err)
	}
	if _, err := fetchPythonSource(context.Background(), srv.URL+"/gone", sum); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("a 404: err %v, want it named", err)
	}
	prev := pythonSourceMaxBytes
	pythonSourceMaxBytes = int64(len(archive) - 1)
	t.Cleanup(func() { pythonSourceMaxBytes = prev })
	if _, err := fetchPythonSource(context.Background(), srv.URL+"/a.tar.gz", sum); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("an oversize archive: err %v", err)
	}
	if left := tempFiles(t, tmp); len(left) != 0 {
		t.Fatalf("a rejected archive was left on disk: %v", left)
	}
}

func TestCheckPythonSourceRoot(t *testing.T) {
	sum := strings.Repeat("a", 64)
	mk := func(files ...string) string {
		dir := t.TempDir()
		for _, f := range files {
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	if err := checkPythonSourceRoot(mk(), sum); err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if err := checkPythonSourceRoot(mk("venv/bin/python"), sum); err != nil {
		t.Fatalf("only the venv: %v", err)
	}
	if err := checkPythonSourceRoot(mk("index.html"), sum); err == nil {
		t.Fatal("a directory holding the tenant's files was accepted")
	}
	own := mk("manage.py")
	os.WriteFile(filepath.Join(own, pythonSourceMarker), []byte(sum+"\n"), 0o644)
	if err := checkPythonSourceRoot(own, sum); err != nil {
		t.Fatalf("an earlier attempt of the same scaffold: %v", err)
	}
	if err := checkPythonSourceRoot(own, strings.Repeat("b", 64)); err == nil {
		t.Fatal("files from another pin were accepted")
	}
	linked := mk("manage.py")
	target := filepath.Join(t.TempDir(), "pin")
	os.WriteFile(target, []byte(sum+"\n"), 0o644)
	if err := os.Symlink(target, filepath.Join(linked, pythonSourceMarker)); err != nil {
		t.Fatal(err)
	}
	if err := checkPythonSourceRoot(linked, sum); err == nil {
		t.Fatal("a marker that is a symlink was followed")
	}
}

// asTenantExec runs `sudo -u <user> -H tar|tee …` for real (minus sudo), so
// the source steps run end to end in a temp dir, and records every command.
// Anything else stays the seam's no-op.
func asTenantExec(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "sudo" && len(args) > 3 && args[0] == "-u" && args[2] == "-H" {
			if args[3] == "tar" || args[3] == "tee" {
				return exec.CommandContext(ctx, args[3], args[4:]...)
			}
		}
		return prev(ctx, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &calls
}

func sourceScaffoldParams(appRoot, url, sum string) pythonScaffoldParams {
	return pythonScaffoldParams{
		AppID: "01APP", Username: "u", AppRoot: appRoot, PythonVersion: "3.12", Server: "gunicorn",
		Requirements: []string{"Django==6.0.3"}, ScaffoldKind: "source", Project: "sic",
		SourceURL: url, SourceSHA256: sum,
	}
}

func TestPythonScaffold_SourceUnpacksAsTheTenant(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	archive, sum := sourceArchive(t, map[string]string{"manage.py": "print('sic')\n", "sic/settings.py": "DEBUG = True\n"})
	srv := sourceServer(t, archive)
	calls := asTenantExec(t)
	appRoot := t.TempDir()

	raw, _ := json.Marshal(sourceScaffoldParams(appRoot, srv.URL+"/a.tar.gz", sum))
	if _, err := pythonAppScaffoldHandler(context.Background(), raw); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(appRoot, "sic", "settings.py")); string(b) != "DEBUG = True\n" {
		t.Fatalf("sic/settings.py = %q; the archive wasn't unpacked under app_root", b)
	}
	if b, _ := os.ReadFile(filepath.Join(appRoot, pythonSourceMarker)); strings.TrimSpace(string(b)) != sum {
		t.Fatalf("marker = %q, want the pin", b)
	}
	var untar string
	for _, c := range *calls {
		if strings.HasPrefix(c, "sudo -u u -H tar ") {
			untar = c
		}
	}
	if untar == "" || !strings.Contains(untar, "--strip-components=1") || !strings.Contains(untar, "-C "+appRoot) {
		t.Fatalf("the archive wasn't unpacked by tar as the tenant into app_root: %q", untar)
	}
	if !strings.Contains(untar, "--no-same-owner") || !strings.Contains(untar, "--no-same-permissions") {
		t.Fatalf("tar must not take owners or modes from the archive: %q", untar)
	}
}

func TestPythonScaffold_SourceInstallsItsSystemLibs(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	archive, sum := sourceArchive(t, map[string]string{"manage.py": "print('sic')\n"})
	srv := sourceServer(t, archive)
	asTenantExec(t)
	l := &libExec{pkgnames: []string{"libmagickwand-6.q16-7t64"}}
	l.install(t)
	p := sourceScaffoldParams(t.TempDir(), srv.URL+"/a.tar.gz", sum)
	p.SystemLibs = []string{"magickwand"}
	raw, _ := json.Marshal(p)
	if _, err := pythonAppScaffoldHandler(context.Background(), raw); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if len(l.installs) != 1 || !strings.HasSuffix(strings.Join(l.installs[0], " "), " libmagickwand-6.q16-7t64") {
		t.Fatalf("installs = %v, want libmagickwand-6.q16-7t64 once", l.installs)
	}
}

// An absent app directory is an empty one: on a fresh install the venv step
// creates it.
func TestCheckPythonSourceRoot_MissingDirIsEmpty(t *testing.T) {
	if err := checkPythonSourceRoot(filepath.Join(t.TempDir(), "new"), strings.Repeat("a", 64)); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
}

func TestPythonScaffold_SourceRefusesATenantsDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	archive, sum := sourceArchive(t, map[string]string{"manage.py": "print('sic')\n"})
	srv := sourceServer(t, archive)
	calls := asTenantExec(t)
	l := &libExec{pkgnames: []string{"libmagickwand-6.q16-7t64"}}
	l.install(t)
	appRoot := t.TempDir()
	os.WriteFile(filepath.Join(appRoot, "index.php"), []byte("<?php"), 0o644)

	p := sourceScaffoldParams(appRoot, srv.URL+"/a.tar.gz", sum)
	p.SystemLibs = []string{"magickwand"}
	raw, _ := json.Marshal(p)
	if _, err := pythonAppScaffoldHandler(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err %v, want the non-empty directory refused", err)
	}
	// Refused before anything is set up for it: no venv in the tenant's
	// directory, no pip, no system package.
	for _, c := range *calls {
		if strings.Contains(c, " tar ") || strings.Contains(c, " venv ") || strings.Contains(c, "pip") {
			t.Fatalf("a refused directory still ran %q", c)
		}
	}
	if len(l.installs) != 0 {
		t.Fatalf("a refused directory still installed %v", l.installs)
	}
	if _, err := os.Stat(filepath.Join(appRoot, "manage.py")); err == nil {
		t.Fatal("the archive was unpacked over the tenant's files")
	}
}

func TestPythonScaffold_RejectsABadSourceOrLib(t *testing.T) {
	calls := asTenantExec(t)
	sum := strings.Repeat("a", 64)
	commit := "https://codeload.github.com/epilys/sic/tar.gz/9bef6edad2f78e93265739969e1974236127ee7d"
	for name, p := range map[string]pythonScaffoldParams{
		"other host": sourceScaffoldParams("/home/u/app", "https://example.com/epilys/sic/tar.gz/9bef6edad2f78e93265739969e1974236127ee7d", sum),
		"plain http": sourceScaffoldParams("/home/u/app", strings.Replace(commit, "https", "http", 1), sum),
		"branch":     sourceScaffoldParams("/home/u/app", "https://codeload.github.com/epilys/sic/tar.gz/main", sum),
		"bad sha256": sourceScaffoldParams("/home/u/app", commit, "abc"),
		"unknown lib": func() pythonScaffoldParams {
			p := sourceScaffoldParams("/home/u/app", commit, sum)
			p.SystemLibs = []string{"openssl"}
			return p
		}(),
	} {
		raw, _ := json.Marshal(p)
		if _, err := pythonAppScaffoldHandler(context.Background(), raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a rejected scaffold ran commands: %v", *calls)
	}
}

// libExec fakes ldconfig, apt-cache and systemd-run for the library install.
type libExec struct {
	present  bool     // ldconfig lists libMagickWand
	pkgnames []string // apt-cache pkgnames output
	installs [][]string
	// staysMissing: the install "succeeds" but the library never appears.
	staysMissing bool
}

func (l *libExec) install(t *testing.T) {
	t.Helper()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "ldconfig":
			out := "\tlibc.so.6 (libc6,x86-64) => /lib/x86_64-linux-gnu/libc.so.6\n"
			if l.present {
				out += "\tlibMagickWand-6.Q16.so.7 (libc6,x86-64) => /lib/x86_64-linux-gnu/libMagickWand-6.Q16.so.7\n"
			}
			return exec.CommandContext(ctx, "printf", "%s", out)
		case "apt-cache":
			return exec.CommandContext(ctx, "printf", "%s", strings.Join(l.pkgnames, "\n")+"\n")
		case "systemd-run":
			if len(args) > 0 && args[0] == "--scope" {
				return prev(ctx, name, args...) // a build command (runAsUserInSlice), not apt
			}
			l.installs = append(l.installs, args)
			l.present = !l.staysMissing
			return exec.CommandContext(ctx, "true")
		}
		return prev(ctx, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })
}

func TestEnsurePythonSystemLibs_InstallsTheDistrosRuntimePackage(t *testing.T) {
	for distro, tc := range map[string]struct {
		names []string
		want  string
	}{
		"ubuntu 24.04": {[]string{"libmagickwand-6-headers", "libmagickwand-6.q16-7t64", "libmagickwand-6.q16-dev", "libmagickwand-6.q16hdri-7t64", "libmagickwand-dev"}, "libmagickwand-6.q16-7t64"},
		"debian 13":    {[]string{"libmagickwand-7-headers", "libmagickwand-7.q16-10", "libmagickwand-7.q16-dev", "libmagickwand-7.q16hdri-10", "libmagickwand-dev"}, "libmagickwand-7.q16-10"},
		"debian 12":    {[]string{"libmagickwand-6.q16-6", "libmagickwand-6.q16-dev", "libmagickwand-6.q16hdri-6"}, "libmagickwand-6.q16-6"},
	} {
		l := &libExec{pkgnames: tc.names}
		l.install(t)
		if err := ensurePythonSystemLibs(context.Background(), []string{"magickwand"}); err != nil {
			t.Fatalf("%s: %v", distro, err)
		}
		if len(l.installs) != 1 {
			t.Fatalf("%s: %d installs, want 1", distro, len(l.installs))
		}
		args := strings.Join(l.installs[0], " ")
		if !strings.HasSuffix(args, " -- apt-get install -y -q --no-install-recommends "+tc.want) {
			t.Fatalf("%s: systemd-run %q, want an apt-get install of %s", distro, args, tc.want)
		}
		if !strings.Contains(args, "--setenv=DEBIAN_FRONTEND=noninteractive") || strings.Contains(args, "bash") {
			t.Fatalf("%s: systemd-run %q: want --setenv and no shell", distro, args)
		}
	}
}

// A host whose apt knows two runtime generations gets the newest.
func TestEnsurePythonSystemLibs_PicksTheNewestRuntime(t *testing.T) {
	l := &libExec{pkgnames: []string{"libmagickwand-7.q16-10", "libmagickwand-6.q16-7t64", "libmagickwand-6.q16-6"}}
	l.install(t)
	if err := ensurePythonSystemLibs(context.Background(), []string{"magickwand"}); err != nil {
		t.Fatal(err)
	}
	if len(l.installs) != 1 || !strings.HasSuffix(strings.Join(l.installs[0], " "), " libmagickwand-7.q16-10") {
		t.Fatalf("installs = %v, want libmagickwand-7.q16-10", l.installs)
	}
	for _, c := range [][2]string{{"libmagickwand-6.q16-6", "libmagickwand-6.q16-7t64"}, {"libmagickwand-6.q16-7t64", "libmagickwand-7.q16-10"}} {
		if !versionLess(c[0], c[1]) || versionLess(c[1], c[0]) {
			t.Errorf("versionLess(%s, %s) wrong", c[0], c[1])
		}
	}
}

// apt saying it installed the package isn't enough: the library must show up.
func TestEnsurePythonSystemLibs_FailsWhenTheLibStaysMissing(t *testing.T) {
	l := &libExec{pkgnames: []string{"libmagickwand-6.q16-7t64"}, staysMissing: true}
	l.install(t)
	if err := ensurePythonSystemLibs(context.Background(), []string{"magickwand"}); err == nil || !strings.Contains(err.Error(), "ldconfig lists no") {
		t.Fatalf("err %v, want the missing library reported", err)
	}
}

func TestEnsurePythonSystemLibs_LeavesAnInstalledLibAlone(t *testing.T) {
	l := &libExec{present: true, pkgnames: []string{"libmagickwand-6.q16-7t64"}}
	l.install(t)
	if err := ensurePythonSystemLibs(context.Background(), []string{"magickwand"}); err != nil {
		t.Fatal(err)
	}
	if len(l.installs) != 0 {
		t.Fatalf("installed %v although the library is there", l.installs)
	}
}

func TestEnsurePythonSystemLibs_FailsWithoutAPackage(t *testing.T) {
	l := &libExec{pkgnames: []string{"libmagickwand-dev", "libmagickwand-6.q16hdri-7t64"}}
	l.install(t)
	if err := ensurePythonSystemLibs(context.Background(), []string{"magickwand"}); err == nil {
		t.Fatal("no runtime package, yet no error")
	}
	if len(l.installs) != 0 {
		t.Fatalf("installed %v with no runtime package", l.installs)
	}
	if err := ensurePythonSystemLibs(context.Background(), []string{"openssl"}); err == nil {
		t.Fatal("an unknown library was accepted")
	}
}

func TestPythonApply_RejectsAnUnknownSystemLib(t *testing.T) {
	calls := asTenantExec(t)
	raw, _ := json.Marshal(pythonAppApplyParams{
		AppID: "01APP", Username: "u", UserID: "1", AppRoot: "/home/u/app", PythonVersion: "3.12",
		AppType: "wsgi", Entrypoint: "sic.wsgi:application", BaseURI: "/", Port: 8001,
		SystemLibs: []string{"openssl"},
	})
	if _, err := pythonAppApplyHandler(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "system_libs") {
		t.Fatalf("err %v, want the unknown library refused", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("a rejected apply ran commands: %v", *calls)
	}
}

// The apply handler can't be driven past its home-scope check in a unit test
// (it resolves the real account), so pin the order in source: the library is
// installed before the build starts, so a restored or migrated app gets it.
func TestPythonApply_EnsuresSystemLibsBeforeTheBuild(t *testing.T) {
	b, err := os.ReadFile("python_app_apply.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	ensure := strings.Index(src, "ensurePythonSystemLibs(ctx, p.SystemLibs)")
	build := strings.Index(src, "pythonAppBuildStep(p, appRoot, venv, server, pythonAppEnvDir)")
	if ensure < 0 || build < 0 || ensure > build {
		t.Fatalf("apply must install system_libs before the build: ensure at %d, build at %d", ensure, build)
	}
}
