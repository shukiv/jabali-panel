package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// testULID is a valid ULID; a larger n sorts later.
func testULID(n int) string { return fmt.Sprintf("01JBX%021d", n) }

// gitDeployServer is a Git server over HTTPS for the deployment tests, with
// one repository, shop.git, that wants Bitbucket's x-token-auth and
// testGitToken. Its branches:
//
//	main      public/index.php, artisan, storage/app/.gitignore
//	composer  main + composer.json and composer.lock
//	vendored  composer + vendor/autoload.php
//	nopublic  main without public/
//	linkpublic main with public/ a link to the repository's root
//	filepublic main with public a file
type gitDeployServer struct {
	gitBin  string
	repoURL string
	host    string
	resolve string
	commits map[string]string
}

func newGitDeployServer(t *testing.T) *gitDeployServer {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("needs git")
	}
	execPath, err := exec.Command(gitBin, "--exec-path").Output()
	if err != nil {
		t.Skip("git --exec-path failed")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("needs git-http-backend")
	}
	repos := t.TempDir()
	work := filepath.Join(t.TempDir(), "work")
	git := func(args ...string) string {
		t.Helper()
		full := append([]string{"-C", work, "-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)
		out, err := exec.Command(gitBin, full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(work, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(gitBin, "init", "-q", "-b", "main", work).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	write("public/index.php", "<?php echo 'shop';\n")
	write("artisan", "<?php\n")
	write("storage/app/.gitignore", "*\n!.gitignore\n")
	git("add", "-A")
	git("commit", "-q", "-m", "main")
	git("checkout", "-q", "-b", "composer")
	write("composer.json", "{}\n")
	write("composer.lock", "{}\n")
	git("add", "-A")
	git("commit", "-q", "-m", "composer")
	git("checkout", "-q", "-b", "vendored")
	write("vendor/autoload.php", "<?php\n")
	git("add", "-A")
	git("commit", "-q", "-m", "vendored")
	git("checkout", "-q", "main")
	git("checkout", "-q", "-b", "nopublic")
	git("rm", "-q", "-r", "public")
	git("commit", "-q", "-m", "nopublic")
	git("checkout", "-q", "-b", "linkpublic")
	if err := os.Symlink("..", filepath.Join(work, "public")); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "linkpublic")
	git("checkout", "-q", "nopublic")
	git("checkout", "-q", "-b", "filepublic")
	write("public", "not a directory\n")
	git("add", "-A")
	git("commit", "-q", "-m", "filepublic")
	commits := map[string]string{}
	for _, b := range []string{"main", "composer", "vendored", "nopublic", "linkpublic", "filepublic"} {
		commits[b] = git("rev-parse", b)
	}
	if out, err := exec.Command(gitBin, "clone", "-q", "--bare", work, filepath.Join(repos, "shop.git")).CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v: %s", err, out)
	}

	backendH := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "x-token-auth" || p != testGitToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backendH.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := gitDeployGitEnvExtra
	gitDeployGitEnvExtra = []string{"GIT_SSL_CAINFO=" + ca}
	t.Cleanup(func() { gitDeployGitEnvExtra = prev })

	port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	// example.com is in the test certificate; the pin, not DNS, takes git
	// to the test server.
	s := &gitDeployServer{
		gitBin:  gitBin,
		repoURL: "https://example.com:" + port + "/shop.git",
		resolve: "example.com:" + port + ":127.0.0.1",
		commits: commits,
	}
	s.host, _, err = gitRepoURL(s.repoURL)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// gitDeployTest is one domain on a test host, deploying from s.
type gitDeployTest struct {
	t         *testing.T
	srv       *gitDeployServer
	osUser    string
	home      string
	domainDir string
	// record is where the fake php writes what each run of it saw.
	record string
	php    string
}

func newGitDeployTest(t *testing.T) *gitDeployTest {
	t.Helper()
	srv := newGitDeployServer(t)
	// The run's git and the fake php only touch the test's own dirs.
	withRealExec(t)
	osUser, home := newGitHost(t)
	dt := &gitDeployTest{t: t, srv: srv, osUser: osUser, home: home,
		domainDir: filepath.Join(home, "domains", "example.com")}
	if err := os.MkdirAll(dt.domainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	dt.record = filepath.Join(dir, "record")
	dt.php = filepath.Join(dir, "php")
	// The fake PHP notes its working directory, its arguments, whether a
	// credential file is still there, and PHP_INI_SCAN_DIR; it prints a
	// PHP Defense drop line, or exits non-zero, when told to.
	script := `#!/bin/sh
{
  printf 'cwd=%s\n' "$(pwd -P)"
  printf 'args=%s\n' "$*"
  if [ -n "$(ls -A "$JABALI_TEST_CREDS" 2>/dev/null)" ]; then echo cred=present; else echo cred=gone; fi
  printf 'scan=%s\n' "${PHP_INI_SCAN_DIR-unset}"
  echo --
} >> "$JABALI_TEST_RECORD"
if [ -n "$JABALI_TEST_DROP" ]; then echo '[snuffleupagus][0.0.0.0][disabled_function][drop] Aborted execution on call of the function proc_open' >&2; fi
exit "${JABALI_TEST_EXIT:-0}"
`
	if err := os.WriteFile(dt.php, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JABALI_TEST_RECORD", dt.record)
	t.Setenv("JABALI_TEST_CREDS", filepath.Join(home, gitCredRootName))
	t.Setenv("JABALI_TEST_EXIT", "")
	t.Setenv("JABALI_TEST_DROP", "")
	return dt
}

// manifest is a deployment of branch as run, with the token in a credential
// file, as git.deploy_start writes it.
func (dt *gitDeployTest) manifest(run, branch string) *gitDeployManifest {
	dt.t.Helper()
	cf, err := writeGitCredFileIn(dt.osUser, os.Getuid(), os.Getgid(), gitDeployCredName(run), dt.srv.host, "x-token-auth", testGitToken)
	if err != nil {
		dt.t.Fatal(err)
	}
	cf.Close()
	return &gitDeployManifest{
		Mode: gitDeployModeDeploy, RunID: run, Release: run, Home: dt.home, DomainDir: dt.domainDir,
		RepoURL: dt.srv.repoURL, Branch: branch, GitBin: dt.srv.gitBin, CredFile: cf.Path, Resolve: dt.srv.resolve,
		Keep: gitDeployKeep, PHPBin: dt.php,
		ComposerUnavailable: "the hosting package does not include Composer runs",
	}
}

func (dt *gitDeployTest) run(m *gitDeployManifest) (string, error) {
	dt.t.Helper()
	if err := m.check(); err != nil {
		dt.t.Fatalf("the test's manifest is invalid: %v", err)
	}
	out := &gitDeployOutput{}
	d := &gitDeployer{m: m, stdout: out, stderr: out}
	err := d.run(context.Background())
	return out.String(), err
}

// gitDeployOutput is one log for a run's stdout and stderr. exec copies a
// child's two streams on two goroutines, so it takes a lock; the agent's own
// are files.
type gitDeployOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *gitDeployOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *gitDeployOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (dt *gitDeployTest) live() string {
	target, err := os.Readlink(filepath.Join(dt.domainDir, "current"))
	if err != nil {
		return ""
	}
	return target
}

// records are the fake PHP's runs, in order.
func (dt *gitDeployTest) records() []map[string]string {
	b, _ := os.ReadFile(dt.record)
	var recs []map[string]string
	for _, block := range strings.Split(string(b), "--\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		rec := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
			k, v, _ := strings.Cut(line, "=")
			rec[k] = v
		}
		recs = append(recs, rec)
	}
	return recs
}

func TestGitDeploy_Deploys(t *testing.T) {
	dt := newGitDeployTest(t)
	shared := filepath.Join(dt.domainDir, "shared")
	if err := os.MkdirAll(shared, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, ".env"), []byte("APP_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := testULID(10)
	m := dt.manifest(run, "main")
	m.WebDir = "public"
	m.SharedPaths = []string{".env", "storage"}
	m.Commands = "php artisan migrate --force\nphp artisan config:cache"
	out, err := dt.run(m)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	rel := filepath.Join(dt.domainDir, "releases", run)
	if got := dt.live(); got != "releases/"+run {
		t.Errorf("current -> %q, want releases/%s", got, run)
	}
	if !gitDeployIsRegular(filepath.Join(rel, "public", "index.php")) {
		t.Error("the release has no public/index.php")
	}
	if gitDeployExists(filepath.Join(rel, ".git")) {
		t.Error("the release kept .git")
	}
	if link, _ := os.Readlink(filepath.Join(rel, ".env")); link != "../../shared/.env" {
		t.Errorf(".env -> %q", link)
	}
	if b, _ := os.ReadFile(filepath.Join(rel, ".env")); string(b) != "APP_KEY=x\n" {
		t.Errorf(".env through the link = %q", b)
	}
	if link, _ := os.Readlink(filepath.Join(rel, "storage")); link != "../../shared/storage" {
		t.Errorf("storage -> %q", link)
	}
	if !gitDeployIsRegular(filepath.Join(shared, "storage", "app", ".gitignore")) {
		t.Error("shared/storage was not taken from the release")
	}
	if !strings.Contains(out, gitDeployCommitMarker+dt.srv.commits["main"]+"\n") {
		t.Errorf("the log has no commit line for %s:\n%s", dt.srv.commits["main"], out)
	}
	if !strings.Contains(out, gitDeployLiveMarker+run+"\n") {
		t.Errorf("the log has no live line:\n%s", out)
	}
	if strings.Contains(out, testGitToken) {
		t.Error("the log carries the token")
	}
	if gitDeployExists(filepath.Join(dt.home, gitCredRootName, gitDeployCredName(run))) {
		t.Error("the credential directory outlived the deployment")
	}
	recs := dt.records()
	if len(recs) != 2 {
		t.Fatalf("the fake php ran %d times, want 2", len(recs))
	}
	realRel, _ := filepath.EvalSymlinks(rel)
	for i, want := range []string{"artisan migrate --force", "artisan config:cache"} {
		if recs[i]["args"] != want || recs[i]["cwd"] != realRel {
			t.Errorf("command %d ran %q in %q, want %q in %q", i+1, recs[i]["args"], recs[i]["cwd"], want, realRel)
		}
	}
}

// The repository's code runs only once the token is gone.
func TestGitDeploy_TokenIsGoneBeforeTheRepositoryRuns(t *testing.T) {
	dt := newGitDeployTest(t)
	m := dt.manifest(testULID(10), "main")
	m.Commands = "php artisan about"
	if out, err := dt.run(m); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	recs := dt.records()
	if len(recs) != 1 || recs[0]["cred"] != "gone" {
		t.Fatalf("a deploy command saw the credential file: %v", recs)
	}
}

// wp-cli runs on the domain's PHP, as php does, not on the account's
// command-line PHP its #! line would pick.
func TestGitDeploy_WPRunsOnTheDomainsPHP(t *testing.T) {
	dt := newGitDeployTest(t)
	bin := t.TempDir()
	wp := filepath.Join(bin, "wp")
	if err := os.WriteFile(wp, []byte("#!/usr/bin/env php\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := dt.manifest(testULID(10), "main")
	m.Commands = "wp cache flush --path=public"
	if out, err := dt.run(m); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	recs := dt.records()
	if len(recs) != 1 || recs[0]["args"] != wp+" cache flush --path=public" {
		t.Errorf("the fake php ran %v, want %s cache flush --path=public", recs, wp)
	}
}

func TestGitDeploy_AFailedCommandLeavesTheSiteAlone(t *testing.T) {
	dt := newGitDeployTest(t)
	first := testULID(10)
	if out, err := dt.run(dt.manifest(first, "main")); err != nil {
		t.Fatalf("first deploy: %v\n%s", err, out)
	}
	t.Setenv("JABALI_TEST_EXIT", "3")
	second := testULID(11)
	m := dt.manifest(second, "main")
	m.Commands = "php artisan migrate --force"
	out, err := dt.run(m)
	if err == nil || !strings.Contains(err.Error(), "deploy command 1 (php artisan migrate --force) failed") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if got := dt.live(); got != "releases/"+first {
		t.Errorf("current -> %q, want the first release", got)
	}
	if gitDeployExists(filepath.Join(dt.domainDir, "releases", second)) {
		t.Error("the failed release was left behind")
	}
	if gitDeployExists(filepath.Join(dt.home, gitCredRootName, gitDeployCredName(second))) {
		t.Error("the credential directory outlived the failed deployment")
	}
	if !strings.Contains(out, "removed the unfinished release") {
		t.Errorf("the log doesn't say the release was removed:\n%s", out)
	}
}

func TestGitDeploy_AMissingSharedFileFailsFirst(t *testing.T) {
	dt := newGitDeployTest(t)
	run := testULID(10)
	m := dt.manifest(run, "main")
	m.WebDir = "public"
	m.SharedPaths = []string{".env"}
	_, err := dt.run(m)
	if err == nil || !strings.Contains(err.Error(), "create shared/.env") {
		t.Fatalf("err = %v", err)
	}
	if dt.live() != "" || gitDeployExists(filepath.Join(dt.domainDir, "releases", run)) {
		t.Error("a failed first deployment left a release or a current link")
	}
}

func TestGitDeploy_ComposerNeedsThePackage(t *testing.T) {
	dt := newGitDeployTest(t)
	run := testULID(10)
	_, err := dt.run(dt.manifest(run, "composer"))
	if err == nil || !strings.Contains(err.Error(), "does not include Composer runs") {
		t.Fatalf("err = %v", err)
	}
	if dt.live() != "" || len(dt.records()) != 0 {
		t.Error("the deployment went on without Composer")
	}
}

// Composer gets the Composer PHP Defense rules; the deploy commands don't.
func TestGitDeploy_ComposerRulesAreComposersOnly(t *testing.T) {
	dt := newGitDeployTest(t)
	run := testULID(10)
	m := dt.manifest(run, "composer")
	m.Composer = &gitDeployComposer{Phar: "/opt/composer.phar", ScanDir: "/etc/test/composer.d", Env: []string{"COMPOSER_HOME=/x"}}
	m.ComposerUnavailable = ""
	m.Commands = "php artisan about"
	if out, err := dt.run(m); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	recs := dt.records()
	if len(recs) != 2 {
		t.Fatalf("the fake php ran %d times, want 2", len(recs))
	}
	rel := filepath.Join(dt.domainDir, "releases", run)
	if args := recs[0]["args"]; !strings.HasPrefix(args, "/opt/composer.phar install --no-dev") || !strings.HasSuffix(args, "--working-dir="+rel) {
		t.Errorf("composer ran %q", args)
	}
	if recs[0]["scan"] != ":/etc/test/composer.d" {
		t.Errorf("composer's PHP_INI_SCAN_DIR = %q", recs[0]["scan"])
	}
	if strings.Contains(recs[1]["scan"], "composer.d") {
		t.Errorf("a deploy command ran with the Composer rules: PHP_INI_SCAN_DIR=%q", recs[1]["scan"])
	}
}

func TestGitDeploy_PHPDefenseStoppingComposerFails(t *testing.T) {
	dt := newGitDeployTest(t)
	t.Setenv("JABALI_TEST_DROP", "1")
	m := dt.manifest(testULID(10), "composer")
	m.Composer = &gitDeployComposer{Phar: "/opt/composer.phar"}
	_, err := dt.run(m)
	if err == nil || !strings.Contains(err.Error(), "PHP Defense stopped a call") {
		t.Fatalf("err = %v", err)
	}
	if dt.live() != "" {
		t.Error("the release went live")
	}
}

func TestGitDeploy_VendorInTheRepositorySkipsComposer(t *testing.T) {
	dt := newGitDeployTest(t)
	out, err := dt.run(dt.manifest(testULID(10), "vendored"))
	if err != nil || !strings.Contains(out, "vendor/ is in the repository") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// GH #2277: a web directory the repository doesn't have is created, empty, in
// the new release, and the deployment goes on.
func TestGitDeploy_CreatesAMissingWebDir(t *testing.T) {
	for _, webDir := range []string{"public", "web/public"} {
		dt := newGitDeployTest(t)
		run := testULID(10)
		m := dt.manifest(run, "nopublic")
		m.WebDir = webDir
		out, err := dt.run(m)
		if err != nil {
			t.Fatalf("%s: err = %v\n%s", webDir, err, out)
		}
		if !gitDeployIsDir(filepath.Join(dt.domainDir, "releases", run, webDir)) {
			t.Errorf("%s: not created in the release", webDir)
		}
		if got := dt.live(); got != "releases/"+run {
			t.Errorf("%s: current -> %q, want the new release", webDir, got)
		}
		if !strings.Contains(out, "created") {
			t.Errorf("%s: the log doesn't say the directory was created:\n%s", webDir, out)
		}
	}
}

// A link or a file where the web directory should be is never served or
// replaced: the deployment fails, before any of the repository's code runs,
// and the site stays as it was.
func TestGitDeploy_WebDirThatIsNotADirectory(t *testing.T) {
	for _, branch := range []string{"linkpublic", "filepublic"} {
		dt := newGitDeployTest(t)
		run := testULID(10)
		m := dt.manifest(run, branch)
		m.WebDir = "public"
		m.Commands = "php artisan about"
		if _, err := dt.run(m); err == nil || !strings.Contains(err.Error(), "is not a directory") {
			t.Fatalf("%s: err = %v", branch, err)
		}
		if recs := dt.records(); len(recs) != 0 {
			t.Errorf("%s: the deploy commands ran before the web directory was refused: %v", branch, recs)
		}
		if gitDeployExists(filepath.Join(dt.domainDir, "releases", run)) {
			t.Errorf("%s: the failed release was left behind", branch)
		}
		if gitDeployExists(filepath.Join(dt.domainDir, "current")) {
			t.Errorf("%s: current was set by a failed deployment", branch)
		}
	}
}

func TestGitDeploy_RefusesARealCurrentDirectory(t *testing.T) {
	dt := newGitDeployTest(t)
	if err := os.Mkdir(filepath.Join(dt.domainDir, "current"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := dt.run(dt.manifest(testULID(10), "main")); err == nil || !strings.Contains(err.Error(), "is not a link") {
		t.Fatalf("err = %v", err)
	}
	if !gitDeployIsDir(filepath.Join(dt.domainDir, "current")) {
		t.Error("the directory named current was replaced")
	}
}

func TestGitDeploy_AWrongTokenFails(t *testing.T) {
	dt := newGitDeployTest(t)
	run := testULID(10)
	cf, err := writeGitCredFileIn(dt.osUser, os.Getuid(), os.Getgid(), gitDeployCredName(run), dt.srv.host, "x-token-auth", "not-the-token")
	if err != nil {
		t.Fatal(err)
	}
	cf.Close()
	m := dt.manifest(testULID(11), "main")
	m.RunID, m.Release, m.CredFile = run, run, cf.Path
	out, err := dt.run(m)
	if err == nil || !strings.Contains(err.Error(), "username and token") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if strings.Contains(out, "not-the-token") || strings.Contains(err.Error(), "not-the-token") {
		t.Error("the token is in the log or the error")
	}
	if gitDeployExists(filepath.Join(dt.domainDir, "releases", run)) || gitDeployExists(filepath.Dir(cf.Path)) {
		t.Error("a failed fetch left the release or the credential directory")
	}
}

func TestGitDeploy_Prune(t *testing.T) {
	dt := newGitDeployTest(t)
	releases := filepath.Join(dt.domainDir, "releases")
	var names []string
	for i := 1; i <= 8; i++ {
		names = append(names, testULID(i))
		if err := os.MkdirAll(filepath.Join(releases, testULID(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A rollback left the oldest release live.
	if err := os.Symlink("releases/"+names[0], filepath.Join(dt.domainDir, "current")); err != nil {
		t.Fatal(err)
	}
	d := &gitDeployer{m: &gitDeployManifest{DomainDir: dt.domainDir, Keep: 5}, stdout: &bytes.Buffer{}}
	d.prune()
	for i, n := range names {
		kept := i == 0 || i >= 3
		if gitDeployIsDir(filepath.Join(releases, n)) != kept {
			t.Errorf("release %d: kept = %v, want %v", i+1, !kept, kept)
		}
	}
}

func TestGitDeploy_Activate(t *testing.T) {
	dt := newGitDeployTest(t)
	first, second := testULID(10), testULID(11)
	for _, run := range []string{first, second} {
		if out, err := dt.run(dt.manifest(run, "main")); err != nil {
			t.Fatalf("deploy: %v\n%s", err, out)
		}
	}
	m := &gitDeployManifest{Mode: gitDeployModeActivate, RunID: testULID(12), Release: first,
		Home: dt.home, DomainDir: dt.domainDir, WebDir: "public"}
	if out, err := dt.run(m); err != nil {
		t.Fatalf("activate: %v\n%s", err, out)
	}
	if got := dt.live(); got != "releases/"+first {
		t.Errorf("current -> %q, want the first release", got)
	}
	m.Release = testULID(1)
	if _, err := dt.run(m); err == nil || !strings.Contains(err.Error(), "no longer on the server") {
		t.Fatalf("activating a missing release: err = %v", err)
	}
	if got := dt.live(); got != "releases/"+first {
		t.Errorf("a failed rollback moved current to %q", got)
	}
}

func TestDecodeGitDeployManifest_Refuses(t *testing.T) {
	good := gitDeployManifest{
		Mode: gitDeployModeDeploy, RunID: testULID(1), Release: testULID(1), Home: "/home/u", DomainDir: "/home/u/domains/example.com",
		RepoURL: "https://github.com/acme/shop.git", Branch: "main", GitBin: "/usr/bin/git", Resolve: "github.com:443:140.82.112.3", Keep: 5,
	}
	enc := func(m gitDeployManifest) string {
		s, err := encodeGitDeployManifest(m)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if _, err := decodeGitDeployManifest(enc(good)); err != nil {
		t.Fatalf("the good manifest: %v", err)
	}
	for name, edit := range map[string]func(*gitDeployManifest){
		"mode":                func(m *gitDeployManifest) { m.Mode = "shell" },
		"run id":              func(m *gitDeployManifest) { m.RunID = "x" },
		"domain dir outside":  func(m *gitDeployManifest) { m.DomainDir = "/home/v/domains/example.com" },
		"domain dir deeper":   func(m *gitDeployManifest) { m.DomainDir = "/home/u/domains/example.com/x" },
		"unclean home":        func(m *gitDeployManifest) { m.Home = "/home/u/" },
		"unchecked url":       func(m *gitDeployManifest) { m.RepoURL = "https://GitHub.com/acme/shop.git" },
		"branch":              func(m *gitDeployManifest) { m.Branch = "-x" },
		"no pin":              func(m *gitDeployManifest) { m.Resolve = "" },
		"cred file elsewhere": func(m *gitDeployManifest) { m.CredFile = "/tmp/credentials" },
		"keep":                func(m *gitDeployManifest) { m.Keep = 0 },
		"commands":            func(m *gitDeployManifest) { m.Commands = "sh -c id" },
		"shared":              func(m *gitDeployManifest) { m.SharedPaths = []string{".env"} },
	} {
		m := good
		edit(&m)
		if _, err := decodeGitDeployManifest(enc(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// {"mode":"deploy","x":1}
	if _, err := decodeGitDeployManifest("eyJtb2RlIjoiZGVwbG95IiwieCI6MX0="); err == nil {
		t.Error("an unknown field was accepted")
	}
	if RunGitDeploy(nil) != 2 || RunGitDeploy([]string{"!"}) != 2 {
		t.Error("RunGitDeploy ran without valid settings")
	}
}

// --- the agent's verbs ---

func gitDeployStart(t *testing.T, p gitDeployStartParams) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	return gitDeployStartHandler(context.Background(), raw)
}

// settingsOf decodes the settings at the end of a run's systemd-run argv.
func settingsOf(t *testing.T, argv []string) *gitDeployManifest {
	t.Helper()
	m, err := decodeGitDeployManifest(argv[len(argv)-1])
	if err != nil {
		t.Fatalf("the run's settings: %v", err)
	}
	return m
}

func TestGitDeployStartHandler_StartsADeployment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git on PATH")
	}
	osUser, home := newGitHost(t)
	if err := os.MkdirAll(filepath.Join(home, "domains", "example.com"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &gitExecFake{}
	f.install(t)
	run := testULID(5)
	res, err := gitDeployStart(t, gitDeployStartParams{
		RunID: run, Kind: "deploy", OSUser: osUser, Domain: "example.com",
		RepoURL: "https://GitHub.com/acme/shop.git", Branch: "main", Token: testGitToken,
		WebDir: "public", SharedPaths: []string{".env"}, Commands: "php artisan migrate --force",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.(gitDeployStartResponse).Unit != "jabali-gitdeploy-"+run+".service" {
		t.Errorf("response = %+v", res)
	}
	argv := f.argv
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"systemd-run --unit=jabali-gitdeploy-" + run + ".service --no-block --uid=" + osUser + " --gid=" + osUser,
		"--slice=jabali-user-" + osUser + ".slice --service-type=oneshot --remain-after-exit",
		"--property=TimeoutStartSec=1800",
		"-- " + composerSandboxShell + " --exec " + gitDeployAgentBin + " git-deploy ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, testGitToken) {
		t.Fatal("the token is in the argv")
	}
	m := settingsOf(t, argv)
	credFile := filepath.Join(home, gitCredRootName, gitDeployCredName(run), "credentials")
	want := gitDeployManifest{
		Mode: gitDeployModeDeploy, RunID: run, Release: run, Home: home, DomainDir: filepath.Join(home, "domains", "example.com"),
		RepoURL: "https://github.com/acme/shop.git", Branch: "main", GitBin: m.GitBin, CredFile: credFile,
		Resolve: "github.com:443:140.82.112.3", WebDir: "public", SharedPaths: []string{".env"},
		Commands: "php artisan migrate --force", Keep: gitDeployKeep, PHPError: m.PHPError,
		ComposerUnavailable: "the hosting package does not include Composer runs",
	}
	if !reflect.DeepEqual(*m, want) {
		t.Errorf("settings =\n%+v\nwant\n%+v", *m, want)
	}
	// The run reads the credential file: it outlives the call.
	if b, err := os.ReadFile(credFile); err != nil || string(b) != "https://x-access-token:"+testGitToken+"@github.com\n" {
		t.Errorf("credential file = %q, %v", b, err)
	}
	// Stopping the run removes it, whether or not the run did.
	raw, _ := json.Marshal(gitDeployStopParams{RunID: run, OSUser: osUser})
	if _, err := gitDeployStopHandler(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if gitDeployExists(filepath.Dir(credFile)) {
		t.Error("git.deploy_stop left the credential directory")
	}
}

func TestGitDeployStartHandler_StartsARollback(t *testing.T) {
	osUser, home := newGitHost(t)
	release := testULID(3)
	if err := os.MkdirAll(filepath.Join(home, "domains", "example.com", "releases", release), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &gitExecFake{}
	f.install(t)
	run := testULID(5)
	if _, err := gitDeployStart(t, gitDeployStartParams{RunID: run, Kind: "rollback", OSUser: osUser, Domain: "example.com", Release: release, WebDir: "public"}); err != nil {
		t.Fatal(err)
	}
	m := settingsOf(t, f.argv)
	if m.Mode != gitDeployModeActivate || m.Release != release || m.RepoURL != "" || m.CredFile != "" {
		t.Errorf("settings = %+v", *m)
	}
}

func TestGitDeployStartHandler_Refusals(t *testing.T) {
	osUser, home := newGitHost(t)
	if err := os.MkdirAll(filepath.Join(home, "domains", "example.com"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &gitExecFake{}
	f.install(t)
	deploy := func(edit func(*gitDeployStartParams)) gitDeployStartParams {
		p := gitDeployStartParams{RunID: testULID(5), Kind: "deploy", OSUser: osUser, Domain: "example.com",
			RepoURL: "https://github.com/acme/shop.git", Branch: "main"}
		edit(&p)
		return p
	}
	for name, c := range map[string]struct {
		p    gitDeployStartParams
		code string
	}{
		"kind":             {deploy(func(p *gitDeployStartParams) { p.Kind = "shell" }), agentwire.CodeInvalidArgument},
		"run id":           {deploy(func(p *gitDeployStartParams) { p.RunID = "1" }), agentwire.CodeInvalidArgument},
		"root":             {deploy(func(p *gitDeployStartParams) { p.OSUser = "root" }), agentwire.CodeInvalidArgument},
		"http":             {deploy(func(p *gitDeployStartParams) { p.RepoURL = "http://github.com/a/b.git" }), agentwire.CodeInvalidArgument},
		"branch":           {deploy(func(p *gitDeployStartParams) { p.Branch = "a..b" }), agentwire.CodeInvalidArgument},
		"commands":         {deploy(func(p *gitDeployStartParams) { p.Commands = "php -n artisan" }), agentwire.CodeInvalidArgument},
		"shared dotfile":   {deploy(func(p *gitDeployStartParams) { p.SharedPaths = []string{".env"} }), agentwire.CodeInvalidArgument},
		"no domain dir":    {deploy(func(p *gitDeployStartParams) { p.Domain = "other.example" }), agentwire.CodeFailedPrecondition},
		"missing release":  {deploy(func(p *gitDeployStartParams) { p.Kind, p.Release = "rollback", testULID(1) }), agentwire.CodeFailedPrecondition},
		"bad release name": {deploy(func(p *gitDeployStartParams) { p.Kind, p.Release = "rollback", "../x" }), agentwire.CodeInvalidArgument},
	} {
		if _, err := gitDeployStart(t, c.p); agentCode(t, err) != c.code {
			t.Errorf("%s: err = %v, want %s", name, err, c.code)
		}
	}
	if f.argv != nil {
		t.Errorf("a refused run ran %q", f.argv)
	}
}

func TestGitDeployStartHandler_RefusesAPrivateHost(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git on PATH")
	}
	osUser, home := newGitHost(t)
	if err := os.MkdirAll(filepath.Join(home, "domains", "example.com"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitLookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.1.2.3")}, nil
	}
	f := &gitExecFake{}
	f.install(t)
	_, err := gitDeployStart(t, gitDeployStartParams{RunID: testULID(5), Kind: "deploy", OSUser: osUser, Domain: "example.com",
		RepoURL: "https://git.internal.example/a/b.git", Branch: "main", Token: testGitToken})
	if agentCode(t, err) != agentwire.CodeFailedPrecondition || !strings.Contains(err.Error(), "local or private address") {
		t.Fatalf("err = %v", err)
	}
	if f.argv != nil || gitDeployExists(filepath.Join(home, gitCredRootName)) {
		t.Error("a refused host got a unit or a credential directory")
	}
}

func TestGitDeployReadReleases(t *testing.T) {
	osUser, home := newGitHost(t)
	dir := filepath.Join(home, "domains", "example.com")
	releases := filepath.Join(dir, "releases")
	for _, n := range []string{testULID(1), testULID(2), "notes"} {
		if err := os.MkdirAll(filepath.Join(releases, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A link among the releases is not one.
	if err := os.Symlink("/etc", filepath.Join(releases, testULID(3))); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("releases/"+testULID(1), filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	got, err := gitDeployReadReleases(osUser, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if want := (gitReleasesResponse{Releases: []string{testULID(2), testULID(1)}, Live: testULID(1)}); !reflect.DeepEqual(got, want) {
		t.Errorf("releases = %+v, want %+v", got, want)
	}

	// current pointing anywhere else is no live release.
	_ = os.Remove(filepath.Join(dir, "current"))
	_ = os.Symlink("/etc", filepath.Join(dir, "current"))
	if got, _ := gitDeployReadReleases(osUser, "example.com"); got.Live != "" {
		t.Errorf("live = %q for current -> /etc", got.Live)
	}

	// A domain directory that is a link is not read.
	other := filepath.Join(home, "domains", "link.example")
	if err := os.Symlink(dir, other); err != nil {
		t.Fatal(err)
	}
	if _, err := gitDeployReadReleases(osUser, "link.example"); err == nil {
		t.Error("read the releases through a linked domain directory")
	}
}

func TestGitDeployCommit_TakesTheFirstLine(t *testing.T) {
	first, later := strings.Repeat("a", 40), strings.Repeat("b", 40)
	f := &gitExecFake{stdout: "==> fetching\n" + gitDeployCommitMarker + first + "\n==> php artisan x\n" + gitDeployCommitMarker + later + "\n"}
	f.install(t)
	if got := gitDeployCommit(context.Background(), "u.service"); got != first {
		t.Fatalf("commit = %q, want the first line's %q", got, first)
	}
	f.stdout = "==> fetching\n" + gitDeployCommitMarker + "not-a-commit\n" + gitDeployCommitMarker + later + "\n"
	if got := gitDeployCommit(context.Background(), "u.service"); got != "" {
		t.Fatalf("commit = %q, want none for a malformed first line", got)
	}
}

func TestGitDeploy_PanelContract(t *testing.T) {
	dir := "../../../panel-api/internal/agent/testdata/"
	for _, c := range []struct {
		file string
		into any
	}{
		{"git_deploy_start_request.json", &gitDeployStartParams{}},
		{"git_deploy_start_response.json", &gitDeployStartResponse{}},
		{"git_deploy_status_response.json", &gitDeployStatusResponse{}},
		{"git_deploy_stop_request.json", &gitDeployStopParams{}},
		{"git_releases_request.json", &gitReleasesParams{}},
		{"git_releases_response.json", &gitReleasesResponse{}},
	} {
		raw, err := os.ReadFile(dir + c.file)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c.into); err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		again, _ := json.Marshal(c.into)
		var got, want any
		_ = json.Unmarshal(again, &got)
		_ = json.Unmarshal(raw, &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %T drops or renames fields:\nwant %s\ngot  %s", c.file, c.into, raw, again)
		}
	}
}
