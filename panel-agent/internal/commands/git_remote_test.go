package commands

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// testGitToken is a made-up token for the tests' own Git server.
const testGitToken = "jabali-test-token-123"

// newGitHost points homesRoot at a temp dir holding the current user's home.
func newGitHost(t *testing.T) (osUser, home string) {
	t.Helper()
	me, err := user.Current()
	if err != nil || !osUserSafeRE.MatchString(me.Username) {
		t.Skipf("needs a current user with a POSIX name (%v)", err)
	}
	if uid, _ := strconv.Atoi(me.Uid); uid < minTenantUID || uid >= 60000 {
		t.Skipf("needs a current user in the hosting uid range, not %s", me.Uid)
	}
	prevLookup := gitLookupIP
	gitLookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("140.82.112.3")}, nil
	}
	t.Cleanup(func() { gitLookupIP = prevLookup })
	homes := t.TempDir()
	prev := homesRoot
	homesRoot = homes
	t.Cleanup(func() { homesRoot = prev })
	home = filepath.Join(homes, me.Username)
	if err := os.Mkdir(home, 0o751); err != nil {
		t.Fatal(err)
	}
	return me.Username, home
}

func TestGitRepoURL(t *testing.T) {
	for raw, want := range map[string][2]string{
		"https://github.com/acme/shop.git":          {"github.com", "https://github.com/acme/shop.git"},
		"https://GitLab.com/acme/sub/shop":          {"gitlab.com", "https://gitlab.com/acme/sub/shop"},
		"https://git.example.com:8443/team/app.git": {"git.example.com:8443", "https://git.example.com:8443/team/app.git"},
		"https://Git.Example.com:08443/team/app":    {"git.example.com:8443", "https://git.example.com:8443/team/app"},
		"https://bitbucket.org/acme/shop.git/":      {"bitbucket.org", "https://bitbucket.org/acme/shop.git/"},
		"https://203.0.113.7/srv/~me/app.git":       {"203.0.113.7", "https://203.0.113.7/srv/~me/app.git"},
	} {
		host, gitURL, err := gitRepoURL(raw)
		if err != nil || host != want[0] || gitURL != want[1] {
			t.Errorf("gitRepoURL(%q) = %q, %q, %v; want %q", raw, host, gitURL, err, want)
		}
	}
	for _, raw := range []string{
		"",
		"http://github.com/acme/shop.git",
		"ssh://git@github.com/acme/shop.git",
		"git@github.com:acme/shop.git",
		"git://github.com/acme/shop.git",
		"file:///home/x/repo.git",
		"ext::sh -c touch% /tmp/x",
		"-uhttps://github.com/acme/shop.git",
		"https://token@github.com/acme/shop.git",
		"https://user:token@github.com/acme/shop.git",
		"https://github.com/acme/shop.git?x=1",
		"https://github.com/acme/shop.git#main",
		"https://github.com/acme/sh%6fp.git",
		"https://github.com/acme/$HOME.git",
		"https://github.com/acme/my shop.git",
		"https://github.com",
		"https://github.com/",
		"https://github.com/acme/../shop.git",
		"https://github.com/acme/./shop.git",
		"https://github.com//shop.git",
		"https://[::1]/acme/shop.git",
		"https://github.com:0/acme/shop.git",
		"https://github.com:/acme/shop.git",
		"https://github.com:99999/acme/shop.git",
		"https://-bad.example.com/acme/shop.git",
		"https://github.com/" + strings.Repeat("a", 512),
	} {
		if host, _, err := gitRepoURL(raw); err == nil {
			t.Errorf("gitRepoURL(%q) = %q, want a refusal", raw, host)
		}
	}
}

// The token never appears in the unit's argv: git reads it from the
// credential file, after the empty helper clears any other.
func TestGitLsRemoteArgv(t *testing.T) {
	got := gitLsRemoteArgv("alice", "/usr/bin/git", "/home/alice/.jabali-git/ab/credentials", "github.com:443:140.82.112.3", "https://github.com/acme/shop.git")
	want := []string{
		"--quiet", "--uid=alice", "--gid=alice", "--slice=jabali-user-alice.slice",
		"--pipe", "--wait", "--collect", "--description=jabali git ls-remote",
		"--property=RuntimeMaxSec=20", "--property=MemoryMax=512M", "--property=TasksMax=64",
		"--", "/usr/local/bin/jabali-ssh-shell", "--exec", "/usr/bin/env",
		"HOME=" + filepath.Join(homesRoot, "alice"), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https",
		"/usr/bin/git", "-C", "/",
		"-c", "credential.helper=",
		"-c", "credential.helper=store --file=/home/alice/.jabali-git/ab/credentials",
		"-c", "http.curloptResolve=github.com:443:140.82.112.3", "-c", "http.followRedirects=false",
		"-c", "http.proxy=",
		"-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=10",
		"ls-remote", "--symref", "--", "https://github.com/acme/shop.git", "HEAD", "refs/heads/*",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	pub := strings.Join(gitLsRemoteArgv("alice", "/usr/bin/git", "", "github.com:443:140.82.112.3", "https://github.com/acme/shop.git"), " ")
	if strings.Contains(pub, "store") {
		t.Errorf("a call without a token names a credential file: %s", pub)
	}
}

func TestWriteGitCredFile(t *testing.T) {
	osUser, home := newGitHost(t)
	uid, gid := os.Getuid(), os.Getgid()
	cf, err := writeGitCredFile(osUser, uid, gid, "git.example.com:8443", "gitlab+deploy-token-7", "t0k/en:@x")
	if err != nil {
		t.Fatal(err)
	}
	if dir := filepath.Dir(filepath.Dir(cf.Path)); dir != filepath.Join(home, ".jabali-git") {
		t.Fatalf("credential file at %s", cf.Path)
	}
	b, err := os.ReadFile(cf.Path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://gitlab+deploy-token-7:t0k%2Fen%3A%40x@git.example.com:8443\n"; string(b) != want {
		t.Errorf("credential file = %q, want %q", b, want)
	}
	for path, mode := range map[string]os.FileMode{
		cf.Path:                            0o600,
		filepath.Dir(cf.Path):              0o700 | os.ModeDir,
		filepath.Join(home, ".jabali-git"): 0o700 | os.ModeDir,
	} {
		if fi, err := os.Lstat(path); err != nil || fi.Mode() != mode {
			t.Errorf("%s: %v, want mode %v", path, err, mode)
		}
	}
	cf.Remove()
	if _, err := os.Lstat(filepath.Dir(cf.Path)); !os.IsNotExist(err) {
		t.Errorf("Remove left %s (%v)", filepath.Dir(cf.Path), err)
	}
}

// A ~/.jabali-git that is a link is refused, and nothing lands where it
// points.
func TestWriteGitCredFile_RefusesALinkedDirectory(t *testing.T) {
	osUser, home := newGitHost(t)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(home, ".jabali-git")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeGitCredFile(osUser, os.Getuid(), os.Getgid(), "github.com", "x", "y"); err == nil {
		t.Fatal("writeGitCredFile followed a linked ~/.jabali-git")
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Errorf("wrote %d entries through the link", len(ents))
	}
}

func TestParseGitLsRemote(t *testing.T) {
	out := "ref: refs/heads/main\tHEAD\n" +
		"1111111111111111111111111111111111111111\tHEAD\n" +
		"2222222222222222222222222222222222222222\trefs/heads/release/1.x\n" +
		"1111111111111111111111111111111111111111\trefs/heads/main\n" +
		"3333333333333333333333333333333333333333\trefs/heads/dev\n"
	got := parseGitLsRemote(out)
	want := gitLsRemoteResult{DefaultBranch: "main", Branches: []string{"dev", "main", "release/1.x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %+v, want %+v", got, want)
	}
	if empty := parseGitLsRemote(""); empty.Branches == nil || len(empty.Branches) != 0 || empty.DefaultBranch != "" {
		t.Errorf("empty repository = %+v", empty)
	}
}

func TestGitRemoteError(t *testing.T) {
	for stderr, want := range map[string]string{
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled\n":                    "wants a username and token",
		"remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/a/b.git/'\n": "refused the ones given",
		"remote: Repository not found.\nfatal: repository 'https://github.com/a/b.git/' not found\n":              "no such repository",
		"fatal: unable to access 'https://nope.example/a.git/': Could not resolve host: nope.example\n":           "does not resolve",
		"fatal: unable to access 'https://x/a.git/': SSL certificate problem: self-signed certificate\n":          "not trusted",
		"fatal: unable to access 'https://x/a.git/': Failed to connect to x port 443\n":                           "could not connect",
	} {
		if got := gitRemoteError(stderr); !strings.Contains(got, want) {
			t.Errorf("gitRemoteError(%q) = %q, want it to say %q", stderr, got, want)
		}
	}
}

// What the remote server says ("remote: ...") never reaches the user; git's
// own fatal line does.
func TestGitRemoteError_LeavesOutTheServersWords(t *testing.T) {
	got := gitRemoteError("remote: internal admin panel v1.2 at 10.0.0.5\nfatal: unable to access 'https://x/a.git/': The requested URL returned error: 403\n")
	if strings.Contains(got, "10.0.0.5") || !strings.Contains(got, "returned error: 403") {
		t.Errorf("gitRemoteError = %q", got)
	}
	if got := gitRemoteError("remote: hello\n"); got != "git could not read the repository" {
		t.Errorf("no fatal line: %q", got)
	}
}

func TestGitPinHost(t *testing.T) {
	answers := map[string][]string{
		"github.com":     {"140.82.112.3"},
		"dual.example":   {"2001:db8::7", "203.0.113.9"},
		"v6.example":     {"2606:4700::1111"},
		"rebind.example": {"203.0.113.9", "10.0.0.5"},
		"meta.example":   {"169.254.169.254"},
		"cgnat.example":  {"100.64.1.1"},
		"local.example":  {"127.0.0.1"},
		"ula.example":    {"fd00::1"},
		"zero.example":   {"0.1.2.3"},
		"bench.example":  {"198.18.0.1"},
		"res.example":    {"240.0.0.1"},
		"site.example":   {"fec0::1"},
		"compat.example": {"::7f00:1"},
		"nat64p.example": {"64:ff9b::a00:5"},
		"6to4p.example":  {"2002:a00:5::1"},
		"nat64.example":  {"64:ff9b::8c52:7003"},
	}
	prev := gitLookupIP
	gitLookupIP = func(_ context.Context, _, host string) ([]net.IP, error) {
		ips := []net.IP{}
		for _, s := range answers[host] {
			ips = append(ips, net.ParseIP(s))
		}
		if len(ips) == 0 {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return ips, nil
	}
	t.Cleanup(func() { gitLookupIP = prev })

	for host, want := range map[string]string{
		"github.com":      "github.com:443:140.82.112.3",
		"github.com:8443": "github.com:8443:140.82.112.3",
		"GitHub.com":      "github.com:443:140.82.112.3",
		"github.com:":     "github.com:443:140.82.112.3",
		"nat64.example":   "nat64.example:443:[64:ff9b::8c52:7003]",
		"dual.example":    "dual.example:443:203.0.113.9",
		"v6.example":      "v6.example:443:[2606:4700::1111]",
	} {
		if got, err := gitPinHost(context.Background(), host); err != nil || got != want {
			t.Errorf("gitPinHost(%q) = %q, %v; want %q", host, got, err, want)
		}
	}
	for _, host := range []string{"rebind.example", "meta.example", "cgnat.example", "local.example", "ula.example", "nope.example", "zero.example", "bench.example", "res.example", "site.example", "compat.example", "nat64p.example", "6to4p.example"} {
		if got, err := gitPinHost(context.Background(), host); err == nil {
			t.Errorf("gitPinHost(%q) = %q, want a refusal", host, got)
		}
	}
}

// A host that resolves to a private address is refused before any
// credential file or unit exists.
func TestGitLsRemoteHandler_RefusesAPrivateHost(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git on PATH")
	}
	osUser, home := newGitHost(t)
	gitLookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.1.2.3")}, nil
	}
	f := &gitExecFake{}
	f.install(t)
	_, err := lsRemote(t, gitLsRemoteParams{OSUser: osUser, RepoURL: "https://git.internal.example/a/b.git", Token: testGitToken})
	if agentCode(t, err) != agentwire.CodeFailedPrecondition || !strings.Contains(err.Error(), "local or private address") {
		t.Fatalf("err = %v", err)
	}
	if f.argv != nil {
		t.Errorf("ran %q", f.argv)
	}
	if _, serr := os.Lstat(filepath.Join(home, ".jabali-git")); !os.IsNotExist(serr) {
		t.Errorf("wrote a credential directory for a refused host (%v)", serr)
	}
}

// gitExecFake stands in for systemd-run: it records the argv, copies the
// credential file it names while the call runs, and answers with stdout or
// fails with stderr.
type gitExecFake struct {
	argv     []string
	credFile string
	cred     string
	stdout   string
	stderr   string
}

func (f *gitExecFake) install(t *testing.T) {
	t.Helper()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		f.argv = append([]string{name}, args...)
		for _, a := range args {
			if p, ok := strings.CutPrefix(a, "credential.helper=store --file="); ok {
				f.credFile = p
				b, _ := os.ReadFile(p)
				f.cred = string(b)
			}
		}
		dir := t.TempDir()
		out, errf := filepath.Join(dir, "out"), filepath.Join(dir, "err")
		_ = os.WriteFile(out, []byte(f.stdout), 0o600)
		_ = os.WriteFile(errf, []byte(f.stderr), 0o600)
		code := "0"
		if f.stderr != "" {
			code = "128"
		}
		return exec.CommandContext(ctx, "sh", "-c", `cat "$1"; cat "$2" >&2; exit "$3"`, "sh", out, errf, code)
	}
	t.Cleanup(func() { execCommandContext = prev })
}

func lsRemote(t *testing.T, p gitLsRemoteParams) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	return gitLsRemoteHandler(context.Background(), raw)
}

func TestGitLsRemoteHandler_TokenOnlyInTheCredentialFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git on PATH")
	}
	osUser, _ := newGitHost(t)
	f := &gitExecFake{stdout: "ref: refs/heads/main\tHEAD\nabc\tHEAD\nabc\trefs/heads/main\n"}
	f.install(t)

	res, err := lsRemote(t, gitLsRemoteParams{OSUser: osUser, RepoURL: "https://bitbucket.org/acme/shop.git", Username: "x-token-auth", Token: testGitToken})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(gitLsRemoteResult); r.DefaultBranch != "main" || !reflect.DeepEqual(r.Branches, []string{"main"}) {
		t.Errorf("result = %+v", r)
	}
	if f.argv[0] != "systemd-run" || strings.Contains(strings.Join(f.argv, " "), testGitToken) {
		t.Fatalf("the token is in the argv: %q", f.argv)
	}
	if f.cred != "https://x-token-auth:"+testGitToken+"@bitbucket.org\n" {
		t.Errorf("credential file during the call = %q", f.cred)
	}
	if _, err := os.Lstat(f.credFile); !os.IsNotExist(err) {
		t.Errorf("the credential file outlived the call (%v)", err)
	}

	// No username: the default one goes with the token.
	if _, err := lsRemote(t, gitLsRemoteParams{OSUser: osUser, RepoURL: "https://github.com/acme/shop.git", Token: testGitToken}); err != nil {
		t.Fatal(err)
	}
	if f.cred != "https://x-access-token:"+testGitToken+"@github.com\n" {
		t.Errorf("credential file = %q", f.cred)
	}

	// No token: no credential file at all.
	f.credFile, f.cred = "", ""
	if _, err := lsRemote(t, gitLsRemoteParams{OSUser: osUser, RepoURL: "https://github.com/acme/shop.git"}); err != nil {
		t.Fatal(err)
	}
	if f.credFile != "" {
		t.Errorf("a public read wrote %s", f.credFile)
	}
}

func TestGitLsRemoteHandler_GitFailureIsAPrecondition(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git on PATH")
	}
	osUser, _ := newGitHost(t)
	f := &gitExecFake{stderr: "remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/acme/shop.git/'\n"}
	f.install(t)
	_, err := lsRemote(t, gitLsRemoteParams{OSUser: osUser, RepoURL: "https://github.com/acme/shop.git", Token: testGitToken})
	if agentCode(t, err) != agentwire.CodeFailedPrecondition || !strings.Contains(err.Error(), "refused the ones given") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testGitToken) {
		t.Errorf("the error carries the token: %v", err)
	}
	if _, serr := os.Lstat(f.credFile); !os.IsNotExist(serr) {
		t.Errorf("a failed call left the credential file (%v)", serr)
	}
}

func TestGitLsRemoteHandler_Refusals(t *testing.T) {
	osUser, _ := newGitHost(t)
	f := &gitExecFake{}
	f.install(t)
	for name, p := range map[string]gitLsRemoteParams{
		"bad user":         {OSUser: "Bad User", RepoURL: "https://github.com/a/b.git"},
		"system account":   {OSUser: "root", RepoURL: "https://github.com/a/b.git"},
		"token in the URL": {OSUser: osUser, RepoURL: "https://tok@github.com/a/b.git"},
		"http":             {OSUser: osUser, RepoURL: "http://github.com/a/b.git"},
		"bad username":     {OSUser: osUser, RepoURL: "https://github.com/a/b.git", Username: "a b", Token: "t"},
		"token with space": {OSUser: osUser, RepoURL: "https://github.com/a/b.git", Token: "a b"},
	} {
		if _, err := lsRemote(t, p); agentCode(t, err) != agentwire.CodeInvalidArgument {
			t.Errorf("%s: err = %v, want invalid_argument", name, err)
		}
	}
	if f.argv != nil {
		t.Errorf("a refused call ran %q", f.argv)
	}
}

// The real git, given the argv the sandbox runs, reads the token from the
// credential file and nothing else: a server that wants Bitbucket's
// x-token-auth refuses the default username, refuses a call with no file,
// and the tenant's own ~/.gitconfig can't redirect the URL.
func TestGitLsRemote_RealGitReadsOnlyTheCredentialFile(t *testing.T) {
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
	osUser, home := newGitHost(t)

	repos := t.TempDir()
	work := filepath.Join(t.TempDir(), "work")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", work},
		{"-C", work, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "one"},
		{"-C", work, "branch", "dev"},
		{"clone", "-q", "--bare", work, filepath.Join(repos, "shop.git")},
	} {
		if out, err := exec.Command(gitBin, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %q: %v: %s", args, err, out)
		}
	}
	backendH := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/moved.git/") {
			http.Redirect(w, r, "https://"+r.Host+"/shop.git/"+strings.TrimPrefix(r.URL.Path, "/moved.git/")+"?"+r.URL.RawQuery, http.StatusFound)
			return
		}
		if u, p, ok := r.BasicAuth(); !ok || u != "x-token-auth" || p != testGitToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backendH.ServeHTTP(w, r)
	}))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	// example.com is in the test certificate; the pin, not DNS, takes git
	// to the test server.
	repoURL := "https://example.com:" + strconv.Itoa(port) + "/shop.git"
	resolve := "example.com:" + strconv.Itoa(port) + ":127.0.0.1"
	host, _, err := gitRepoURL(repoURL)
	if err != nil {
		t.Fatal(err)
	}

	// run runs what the sandbox runs after --exec, from an empty
	// environment as the sandbox does, trusting the test server's
	// certificate.
	runURL := func(credFile, url string) (gitLsRemoteResult, string, error) {
		argv := gitLsRemoteArgv(osUser, gitBin, credFile, resolve, url)
		i := 0
		for i < len(argv) && argv[i] != "--exec" {
			i++
		}
		cmd := exec.Command(argv[i+1], append([]string{"GIT_SSL_CAINFO=" + ca}, argv[i+2:]...)...)
		// The sandbox starts in the home directory (jabali-ssh-shell --chdir).
		cmd.Dir = home
		cmd.Env = []string{}
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return parseGitLsRemote(stdout.String()), stderr.String(), err
	}
	run := func(credFile string) (gitLsRemoteResult, string, error) { return runURL(credFile, repoURL) }
	withCred := func(username string) (gitLsRemoteResult, string, error) {
		cf, err := writeGitCredFile(osUser, os.Getuid(), os.Getgid(), host, username, testGitToken)
		if err != nil {
			t.Fatal(err)
		}
		defer cf.Remove()
		return run(cf.Path)
	}

	res, stderr, err := withCred("x-token-auth")
	if err != nil {
		t.Fatalf("ls-remote with the credential file: %v: %s", err, stderr)
	}
	if want := (gitLsRemoteResult{DefaultBranch: "main", Branches: []string{"dev", "main"}}); !reflect.DeepEqual(res, want) {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	if _, stderr, err := withCred(gitDefaultUsername); err == nil {
		t.Error("the server took the default username; it wants x-token-auth")
	} else if !strings.Contains(gitRemoteError(stderr), "refused the ones given") {
		t.Errorf("wrong username: %s", gitRemoteError(stderr))
	}
	if _, stderr, err := run(""); err == nil {
		t.Error("ls-remote with no credential file read a private repository")
	} else if !strings.Contains(gitRemoteError(stderr), "wants a username and token") {
		t.Errorf("no credentials: %s", gitRemoteError(stderr))
	}

	// git follows no redirect, not even to the same server: a redirect
	// could lead to a host nobody checked.
	cf, err := writeGitCredFile(osUser, os.Getuid(), os.Getgid(), host, "x-token-auth", testGitToken)
	if err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runURL(cf.Path, "https://example.com:"+strconv.Itoa(port)+"/moved.git")
	cf.Remove()
	if err == nil {
		t.Errorf("git followed a redirect: %s", stderr)
	}

	// A tenant's ~/.gitconfig that points the URL elsewhere is not read.
	gitconfig := "[url \"https://nope.invalid/\"]\n\tinsteadOf = https://example.com:" + strconv.Itoa(port) + "/\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := withCred("x-token-auth"); err != nil {
		t.Errorf("the tenant's ~/.gitconfig changed the call: %v: %s", err, stderr)
	}
	if err := os.Remove(filepath.Join(home, ".gitconfig")); err != nil {
		t.Fatal(err)
	}

	// Nor is the config of a repository in the directory git starts in: the
	// tenant's home may be one.
	if out, err := exec.Command(gitBin, "init", "-q", home).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	repoConfig, err := os.OpenFile(filepath.Join(home, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = repoConfig.WriteString(gitconfig)
	repoConfig.Close()
	if _, stderr, err := withCred("x-token-auth"); err != nil {
		t.Errorf("a repository's config in the home directory changed the call: %v: %s", err, stderr)
	}
}

// The panel's side of the git.* contract (GH #2277): what the panel sends
// decodes into these params with every field kept, and what the handler
// answers decodes into what the panel expects. The panel decodes the same
// fixtures (TestContract in panel-api/internal/gitdeploy).
func TestGitRemote_PanelContract(t *testing.T) {
	dir := "../../../panel-api/internal/agent/testdata/"
	for _, c := range []struct {
		file string
		into any
	}{
		{"git_ls_remote_request.json", &gitLsRemoteParams{}},
		{"git_ls_remote_response.json", &gitLsRemoteResult{}},
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
