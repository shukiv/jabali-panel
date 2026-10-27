package commands

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

// git_deploy_run.go — a Git deployment itself (GH #2277, ADR-0178):
// `jabali-agent git-deploy <settings>`, the command git.deploy_start's unit
// runs as the domain's user, inside their SSH sandbox. Nothing here runs as
// root, and nothing goes through a shell.
//
// A deployment builds a new release beside the live one, and only switches
// to it when every step has passed:
//
//  1. clone the branch, one commit deep, into domains/<d>/releases/<run id>,
//     pinned to the address git.deploy_start checked; then remove the
//     credential file and .git, before any of the repository's code runs
//  2. link each shared path to domains/<d>/shared/<path>
//  3. composer install, when the project has a composer.lock and no vendor/
//     (only when the hosting package allows Composer runs)
//  4. the deploy commands, in the release (cronvalidate.ValidateDeployCommands)
//  5. point domains/<d>/current at the release (a relative link, replaced
//     with a rename, so a request sees the old release or the new one)
//  6. remove releases beyond the newest ones kept, never the live one
//
// A failure before step 5 removes the unfinished release and leaves the site
// as it was. A rollback ("activate") only does step 5, for a kept release.
//
// The settings come in argv, which root wrote into the unit, not from a file
// in the tenant's home, which the tenant could rewrite: they say whether the
// Composer step may run. They hold no secret; the token is in the
// credential file.

const (
	gitDeployModeDeploy   = "deploy"
	gitDeployModeActivate = "activate"
	// gitDeployCommitMarker and gitDeployLiveMarker start the lines the
	// deployment prints for the panel. The commit line comes right after the
	// clone, before any of the repository's code runs, so the panel takes the
	// first one.
	gitDeployCommitMarker = "jabali-git-deploy: commit "
	gitDeployLiveMarker   = "jabali-git-deploy: live "
	// gitDeployMaxSettings bounds the encoded settings.
	gitDeployMaxSettings = 64 << 10
	gitDeployMaxKeep     = 20
)

// gitDeployManifest is what one run does.
type gitDeployManifest struct {
	Mode  string `json:"mode"`
	RunID string `json:"run_id"`
	// Home is the user's home; DomainDir is <home>/domains/<domain>.
	Home      string `json:"home"`
	DomainDir string `json:"domain_dir"`
	// Release is the release to build (the run's id) or to switch to.
	Release string `json:"release"`

	RepoURL  string `json:"repo_url,omitempty"`
	Branch   string `json:"branch,omitempty"`
	GitBin   string `json:"git_bin,omitempty"`
	CredFile string `json:"cred_file,omitempty"`
	Resolve  string `json:"resolve,omitempty"`

	WebDir      string   `json:"web_dir,omitempty"`
	SharedPaths []string `json:"shared_paths,omitempty"`
	Commands    string   `json:"commands,omitempty"`
	Keep        int      `json:"keep,omitempty"`

	// PHPBin is the domain's PHP CLI, for Composer and a bare `php`;
	// PHPError says why there is none.
	PHPBin   string `json:"php_bin,omitempty"`
	PHPError string `json:"php_error,omitempty"`
	// Composer is how to run Composer; nil when the run may not, and
	// ComposerUnavailable says why.
	Composer            *gitDeployComposer `json:"composer,omitempty"`
	ComposerUnavailable string             `json:"composer_unavailable,omitempty"`
}

type gitDeployComposer struct {
	Phar string `json:"phar"`
	// ScanDir holds the Composer PHP Defense ini; "" when PHP Defense is off.
	ScanDir string `json:"scan_dir,omitempty"`
	// Env is Composer's environment (composerEnv).
	Env []string `json:"env"`
}

var (
	gitBranchRE = regexp.MustCompile(`^[A-Za-z0-9._/+-]{1,255}$`)
	gitCommitRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

func encodeGitDeployManifest(m gitDeployManifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	s := base64.StdEncoding.EncodeToString(b)
	if len(s) > gitDeployMaxSettings {
		return "", errors.New("the deployment's settings are too large")
	}
	return s, nil
}

func decodeGitDeployManifest(s string) (*gitDeployManifest, error) {
	if len(s) > gitDeployMaxSettings {
		return nil, errors.New("the deployment's settings are too large")
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode the deployment's settings: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var m gitDeployManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode the deployment's settings: %w", err)
	}
	if err := m.check(); err != nil {
		return nil, err
	}
	return &m, nil
}

// validGitBranch applies git's branch name rules, as the panel's
// gitdeploy.ValidateBranch does.
func validGitBranch(s string) bool {
	return gitBranchRE.MatchString(s) &&
		!strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, ".") &&
		!strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, ".lock") &&
		!strings.Contains(s, "..") && !strings.Contains(s, "//") && !strings.Contains(s, "/.")
}

func (m *gitDeployManifest) check() error {
	if m.Mode != gitDeployModeDeploy && m.Mode != gitDeployModeActivate {
		return fmt.Errorf("unknown mode %q", m.Mode)
	}
	if !ulidRE.MatchString(m.RunID) || !ulidRE.MatchString(m.Release) {
		return errors.New("run_id and release must be ULIDs")
	}
	for _, p := range []string{m.Home, m.DomainDir} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return fmt.Errorf("%q is not a clean absolute path", p)
		}
	}
	if name, ok := strings.CutPrefix(m.DomainDir, m.Home+"/domains/"); !ok || !domainRegex.MatchString(name) {
		return errors.New("domain_dir must be <home>/domains/<domain>")
	}
	if err := cronvalidate.ValidateDeployLayout(m.WebDir, m.SharedPaths); err != nil {
		return err
	}
	if m.Mode == gitDeployModeActivate {
		return nil
	}
	if _, gitURL, err := gitRepoURL(m.RepoURL); err != nil || gitURL != m.RepoURL {
		return errors.New("repo_url is not a checked repository URL")
	}
	if !validGitBranch(m.Branch) {
		return fmt.Errorf("%q is not a branch name", m.Branch)
	}
	if m.Resolve == "" || !filepath.IsAbs(m.GitBin) {
		return errors.New("resolve and git_bin are required")
	}
	if m.CredFile != "" && !strings.HasPrefix(m.CredFile, m.Home+"/"+gitCredRootName+"/") {
		return errors.New("cred_file must be in ~/" + gitCredRootName)
	}
	if m.Keep < 1 || m.Keep > gitDeployMaxKeep {
		return fmt.Errorf("keep must be 1..%d", gitDeployMaxKeep)
	}
	if m.PHPBin != "" && !filepath.IsAbs(m.PHPBin) {
		return errors.New("php_bin must be absolute")
	}
	if c := m.Composer; c != nil && (!filepath.IsAbs(c.Phar) || (c.ScanDir != "" && !filepath.IsAbs(c.ScanDir))) {
		return errors.New("composer paths must be absolute")
	}
	if _, err := cronvalidate.ValidateDeployCommands(m.Commands); err != nil {
		return fmt.Errorf("deploy commands: %w", err)
	}
	return nil
}

// RunGitDeploy is `jabali-agent git-deploy <settings>`. It returns the exit
// code: 0 when the release went live.
func RunGitDeploy(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: jabali-agent git-deploy <settings>")
		return 2
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "jabali-git-deploy: runs as the site's user, never as root")
		return 2
	}
	m, err := decodeGitDeployManifest(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "jabali-git-deploy: "+err.Error())
		return 2
	}
	// systemctl stop (a cancel, or the run's time limit) sends SIGTERM: the
	// step in progress is ended and the unfinished release removed.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	d := &gitDeployer{m: m, stdout: os.Stdout, stderr: os.Stderr}
	if err := d.run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "jabali-git-deploy: failed: "+err.Error())
		return 1
	}
	return 0
}

type gitDeployer struct {
	m              *gitDeployManifest
	stdout, stderr io.Writer
}

func (d *gitDeployer) say(format string, a ...any) {
	fmt.Fprintf(d.stdout, "==> "+format+"\n", a...)
}

func (d *gitDeployer) releasesDir() string { return filepath.Join(d.m.DomainDir, "releases") }

func (d *gitDeployer) releaseDir(name string) string { return filepath.Join(d.releasesDir(), name) }

func (d *gitDeployer) run(ctx context.Context) error {
	if d.m.Mode == gitDeployModeActivate {
		return d.activate()
	}
	return d.deploy(ctx)
}

// removeCreds deletes the credential file and its directory. Called right
// after the clone, and again on the way out.
func (d *gitDeployer) removeCreds() {
	if d.m.CredFile == "" {
		return
	}
	_ = os.Remove(d.m.CredFile)
	_ = os.Remove(d.m.CredFile + ".lock")
	_ = os.Remove(filepath.Dir(d.m.CredFile))
}

func (d *gitDeployer) deploy(ctx context.Context) (err error) {
	defer d.removeCreds()
	m := d.m
	shared := filepath.Join(m.DomainDir, "shared")
	if err := gitDeployEnsureDir(d.releasesDir(), 0o755); err != nil {
		return err
	}
	if len(m.SharedPaths) > 0 {
		if err := gitDeployEnsureDir(shared, 0o750); err != nil {
			return err
		}
	}
	rel := d.releaseDir(m.Release)
	if gitDeployExists(rel) {
		return fmt.Errorf("releases/%s already exists", m.Release)
	}
	// From here an error removes what this run built.
	defer func() {
		if err != nil {
			if rerr := os.RemoveAll(rel); rerr == nil {
				d.say("removed the unfinished release; the site is as it was")
			}
		}
	}()

	d.say("fetching %s, branch %s", m.RepoURL, m.Branch)
	if err := d.clone(ctx, rel); err != nil {
		return err
	}
	commit, err := d.headCommit(ctx, rel)
	if err != nil {
		return err
	}
	fmt.Fprintln(d.stdout, gitDeployCommitMarker+commit)
	// The token is no longer needed, and the repository's own code runs from
	// here on.
	d.removeCreds()
	if gitDeployIsRegular(filepath.Join(rel, ".gitmodules")) {
		d.say("the repository has submodules: they are not fetched")
	}
	if err := os.RemoveAll(filepath.Join(rel, ".git")); err != nil {
		return fmt.Errorf("remove .git from the release: %w", err)
	}
	if m.WebDir != "" && !gitDeployIsDir(filepath.Join(rel, m.WebDir)) {
		return fmt.Errorf("the repository has no directory %s to serve the site from: fix the web directory in the settings", m.WebDir)
	}
	if err := d.linkShared(rel, shared); err != nil {
		return err
	}
	if err := d.composer(ctx, rel); err != nil {
		return err
	}
	if err := d.commands(ctx, rel); err != nil {
		return err
	}
	if err := d.switchTo(m.Release); err != nil {
		return err
	}
	d.prune()
	return nil
}

func (d *gitDeployer) activate() error {
	rel := d.releaseDir(d.m.Release)
	if !gitDeployIsDir(rel) {
		return fmt.Errorf("release %s is no longer on the server", d.m.Release)
	}
	if d.m.WebDir != "" && !gitDeployIsDir(filepath.Join(rel, d.m.WebDir)) {
		return fmt.Errorf("release %s has no directory %s to serve the site from", d.m.Release, d.m.WebDir)
	}
	return d.switchTo(d.m.Release)
}

// gitDeployEnsureDir makes dir a directory, refusing a link or a file there.
func gitDeployEnsureDir(dir string, mode os.FileMode) error {
	if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if !gitDeployIsDir(dir) {
		return fmt.Errorf("%s exists and is not a directory", dir)
	}
	return nil
}

// gitDeployIsDir reports whether p is a directory, not a link to one.
func gitDeployIsDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

func gitDeployIsRegular(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

func gitDeployExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// gitDeployGitEnvExtra is added to git's environment: tests trust their own
// Git server's certificate with it. Empty in the agent.
var gitDeployGitEnvExtra []string

func (d *gitDeployer) gitEnv() []string {
	env := append(gitEnvHome(d.m.Home), "PATH="+os.Getenv("PATH"))
	return append(env, gitDeployGitEnvExtra...)
}

// clone fetches the branch, one commit deep, into rel. Lines the Git server
// sent ("remote: ...") are left out of the log, as git.ls_remote leaves them
// out of its errors.
func (d *gitDeployer) clone(ctx context.Context, rel string) error {
	m := d.m
	args := []string{"-C", "/"}
	args = append(args, gitAuthArgs(m.CredFile)...)
	args = append(args, gitNetArgs(m.Resolve)...)
	args = append(args, "-c", "advice.detachedHead=false",
		"clone", "--quiet", "--depth=1", "--single-branch", "--no-tags", "--branch="+m.Branch,
		"--", m.RepoURL, rel)
	cmd := execCommandContext(ctx, m.GitBin, args...)
	cmd.Env = d.gitEnv()
	tail := &tailWriter{max: 16 << 10}
	errOut := &lineFilter{dst: io.MultiWriter(d.stderr, tail), drop: func(l string) bool {
		return strings.HasPrefix(strings.TrimSpace(l), "remote:")
	}}
	cmd.Stdout, cmd.Stderr = d.stdout, errOut
	err := cmd.Run()
	errOut.Flush()
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("stopped while fetching the repository")
		}
		return errors.New(gitRemoteError(string(tail.buf)))
	}
	return nil
}

func (d *gitDeployer) headCommit(ctx context.Context, rel string) (string, error) {
	cmd := execCommandContext(ctx, d.m.GitBin, "-C", rel, "rev-parse", "HEAD")
	cmd.Env = d.gitEnv()
	out, err := cmd.Output()
	commit := strings.TrimSpace(string(out))
	if err != nil || !gitCommitRE.MatchString(commit) {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("could not read the fetched commit: %s", clipBytes(strings.TrimSpace(string(ee.Stderr)), 512))
		}
		return "", errors.New("could not read the fetched commit")
	}
	return commit, nil
}

// linkShared points each shared path of the release at domains/<d>/shared.
// A shared path not there yet is taken from the release; one the repository
// doesn't have either fails the deployment, before the site changes.
func (d *gitDeployer) linkShared(rel, shared string) error {
	for _, p := range d.m.SharedPaths {
		target := filepath.Join(shared, p)
		inRel := filepath.Join(rel, p)
		if !gitDeployExists(target) {
			if !gitDeployExists(inRel) {
				return fmt.Errorf("shared/%s does not exist and the repository has no %s: create shared/%s, then deploy again", p, p, p)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return fmt.Errorf("create shared/%s: %w", filepath.Dir(p), err)
			}
			if err := os.Rename(inRel, target); err != nil {
				return fmt.Errorf("move %s to shared/: %w", p, err)
			}
			d.say("shared/%s did not exist: took it from this release", p)
		}
		if err := os.RemoveAll(inRel); err != nil {
			return fmt.Errorf("replace %s with a link: %w", p, err)
		}
		if err := os.MkdirAll(filepath.Dir(inRel), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
		}
		link, err := filepath.Rel(filepath.Dir(inRel), target)
		if err != nil {
			return err
		}
		if err := os.Symlink(link, inRel); err != nil {
			return fmt.Errorf("link %s: %w", p, err)
		}
		d.say("%s -> shared/%s", p, p)
	}
	return nil
}

// composer runs `composer install` for a project with a composer.lock and no
// vendor/, with the Composer runs' PHP Defense rules (ADR-0176), which apply
// to Composer alone: the deploy commands run on the default ones.
func (d *gitDeployer) composer(ctx context.Context, rel string) error {
	m := d.m
	if !gitDeployIsRegular(filepath.Join(rel, "composer.json")) {
		return nil
	}
	if !gitDeployIsRegular(filepath.Join(rel, "composer.lock")) {
		d.say("composer.json has no composer.lock beside it: composer install skipped (commit composer.lock to have it run)")
		return nil
	}
	if gitDeployExists(filepath.Join(rel, "vendor")) {
		d.say("vendor/ is in the repository: composer install skipped")
		return nil
	}
	if m.Composer == nil {
		return fmt.Errorf("the project needs composer install (composer.lock and no vendor/), and %s", m.ComposerUnavailable)
	}
	if m.PHPBin == "" {
		return fmt.Errorf("composer install needs PHP: %s", m.PHPError)
	}
	d.say("composer install")
	cmd := execCommandContext(ctx, m.PHPBin, m.Composer.Phar, "install",
		"--no-dev", "--no-interaction", "--no-ansi", "--no-progress", "--prefer-dist", "--optimize-autoloader",
		"--working-dir="+rel)
	env := gitDeployEnvWithout(os.Environ(), "PHP_INI_SCAN_DIR", "COMPOSER_")
	env = append(env, m.Composer.Env...)
	if m.Composer.ScanDir != "" {
		// The leading ':' keeps PHP's own scan dir; the Composer ini comes
		// after it, so its sp.configuration_file wins.
		env = append(env, "PHP_INI_SCAN_DIR=:"+m.Composer.ScanDir)
	}
	cmd.Env = env
	cmd.Dir = rel
	blocked := &blockWatch{}
	cmd.Stdout = io.MultiWriter(d.stdout, blocked)
	cmd.Stderr = io.MultiWriter(d.stderr, blocked)
	err := cmd.Run()
	if blocked.seen() {
		// A blocked call can end in exit 0 (the caller swallowed it).
		return errors.New("PHP Defense stopped a call during composer install")
	}
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("stopped during composer install")
		}
		return fmt.Errorf("composer install failed: %v", err)
	}
	return nil
}

// commands runs the deploy commands in the release, in order, stopping at
// the first that fails. A bare `php` is the domain's PHP.
func (d *gitDeployer) commands(ctx context.Context, rel string) error {
	cmds, err := cronvalidate.ValidateDeployCommands(d.m.Commands)
	if err != nil {
		return fmt.Errorf("deploy commands: %w", err)
	}
	for i, c := range cmds {
		argv := append([]string(nil), c.Argv...)
		switch argv[0] {
		case "php", "wp":
			if d.m.PHPBin == "" {
				return fmt.Errorf("deploy command %d needs PHP: %s", i+1, d.m.PHPError)
			}
			if argv[0] == "wp" {
				// wp-cli on the domain's PHP, as the site runs, not on the
				// account's command-line PHP its #! line would pick.
				wp, err := exec.LookPath("wp")
				if err != nil {
					return fmt.Errorf("deploy command %d needs wp-cli, which is not installed", i+1)
				}
				argv = append([]string{wp}, argv[1:]...)
				argv = append([]string{d.m.PHPBin}, argv...)
				break
			}
			argv[0] = d.m.PHPBin
		}
		line := strings.Join(c.Argv, " ")
		d.say("%s", line)
		cmd := execCommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = rel
		cmd.Env = os.Environ()
		cmd.Stdout, cmd.Stderr = d.stdout, d.stderr
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("stopped during deploy command %d (%s)", i+1, line)
			}
			return fmt.Errorf("deploy command %d (%s) failed: %v", i+1, line, err)
		}
	}
	return nil
}

// switchTo points domains/<d>/current at releases/<name>: a new relative
// link renamed over the old one, so the switch is atomic.
func (d *gitDeployer) switchTo(name string) error {
	current := filepath.Join(d.m.DomainDir, "current")
	if fi, err := os.Lstat(current); err == nil && fi.Mode()&fs.ModeSymlink == 0 {
		return errors.New("current exists in the domain's directory and is not a link: move it away, then deploy again")
	}
	tmp := filepath.Join(d.m.DomainDir, ".current-"+d.m.RunID)
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("releases", name), tmp); err != nil {
		return fmt.Errorf("link the new release: %w", err)
	}
	if err := os.Rename(tmp, current); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("switch to the new release: %w", err)
	}
	fmt.Fprintln(d.stdout, gitDeployLiveMarker+name)
	return nil
}

// prune removes releases beyond the newest Keep, never the live one. A
// release it can't remove is reported, and the deployment still succeeds.
func (d *gitDeployer) prune() {
	entries, err := os.ReadDir(d.releasesDir())
	if err != nil {
		d.say("could not list old releases: %v", err)
		return
	}
	live, _ := os.Readlink(filepath.Join(d.m.DomainDir, "current"))
	var names []string
	for _, e := range entries {
		if e.IsDir() && ulidRE.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	// A ULID sorts by the time it was made: newest first.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for i, n := range names {
		if i < d.m.Keep || live == filepath.Join("releases", n) {
			continue
		}
		if err := os.RemoveAll(d.releaseDir(n)); err != nil {
			d.say("could not remove old release %s: %v", n, err)
			continue
		}
		d.say("removed old release %s", n)
	}
}

// gitDeployEnvWithout is env without the variables named, or starting with
// a name ending in '_'.
func gitDeployEnvWithout(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
next:
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		for _, n := range names {
			if k == n || (strings.HasSuffix(n, "_") && strings.HasPrefix(k, n)) {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}

// lineFilter writes whole lines to dst, leaving out those drop matches.
type lineFilter struct {
	dst  io.Writer
	drop func(string) bool
	buf  []byte
}

func (f *lineFilter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	for {
		i := strings.IndexAny(string(f.buf), "\n\r")
		if i < 0 {
			break
		}
		line := string(f.buf[:i+1])
		f.buf = f.buf[i+1:]
		if !f.drop(line) {
			_, _ = f.dst.Write([]byte(line))
		}
	}
	return len(p), nil
}

// Flush writes what is left of an unfinished last line.
func (f *lineFilter) Flush() {
	if len(f.buf) > 0 && !f.drop(string(f.buf)) {
		_, _ = f.dst.Write(f.buf)
	}
	f.buf = nil
}

// blockWatch looks for the line PHP Defense prints when it stops a call.
type blockWatch struct {
	mu      sync.Mutex
	partial []byte
	hit     bool
}

func (w *blockWatch) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	sc := bufio.NewScanner(strings.NewReader(string(w.partial)))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		if composerRunBlockedRE.MatchString(sc.Text()) {
			w.hit = true
		}
	}
	if i := strings.LastIndexByte(string(w.partial), '\n'); i >= 0 {
		w.partial = w.partial[i+1:]
	}
	return len(p), nil
}

func (w *blockWatch) seen() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hit || composerRunBlockedRE.Match(w.partial)
}
