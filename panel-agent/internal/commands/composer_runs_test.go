package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const testRunID = "01JCXMPSRRVN00000000000AB1"

// composerExecFake answers systemctl show and journalctl with canned output
// and records every command.
type composerExecFake struct {
	mu    sync.Mutex
	calls []string
	show  string
	log   string
	dir   string
}

func (f *composerExecFake) install(t *testing.T) {
	t.Helper()
	f.dir = t.TempDir()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, name+" "+strings.Join(args, " "))
		out := ""
		switch {
		case name == "systemctl" && len(args) > 0 && args[0] == "show":
			out = f.show
		case name == "journalctl":
			out = f.log
		}
		p := filepath.Join(f.dir, fmt.Sprintf("out-%d", len(f.calls)))
		if err := os.WriteFile(p, []byte(out), 0o600); err != nil {
			t.Error(err)
		}
		return exec.CommandContext(ctx, "cat", p)
	}
	t.Cleanup(func() { execCommandContext = prev })
}

func (f *composerExecFake) called(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// composerRunHost is a temp host for one user: a home with
// domains/example.com/app, the domain's vhost on PHP 8.4, a php8.4 on PATH,
// a Composer phar, and PHP Defense rules.
type composerRunHost struct {
	user      string
	home      string
	domainDir string
	php       string
	phar      string
}

func newComposerRunHost(t *testing.T) composerRunHost {
	t.Helper()
	me, err := user.Current()
	if err != nil || !osUserSafeRE.MatchString(me.Username) {
		t.Skipf("needs a current user with a POSIX name (%v)", err)
	}
	h := composerRunHost{user: me.Username}

	homes := t.TempDir()
	prevHomes := homesRoot
	homesRoot = homes
	t.Cleanup(func() { homesRoot = prevHomes })
	h.home = filepath.Join(homes, h.user)
	h.domainDir = filepath.Join(h.home, "domains", "example.com")
	if err := os.MkdirAll(filepath.Join(h.domainDir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}

	sites := t.TempDir()
	prevSites := nginxSitesDir
	nginxSitesDir = sites
	t.Cleanup(func() { nginxSitesDir = prevSites })
	vhost := "server {\n  location ~ \\.php$ {\n    fastcgi_pass unix:/run/php/jabali-" + h.user + "-php8.4/fpm.sock;\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(sites, "example.com.conf"), []byte(vhost), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	h.php = filepath.Join(bin, "php8.4")
	if err := os.WriteFile(h.php, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	composerBin := t.TempDir()
	prevBin := composerBinDir
	composerBinDir = composerBin
	t.Cleanup(func() { composerBinDir = prevBin })
	h.phar = filepath.Join(composerBin, "composer-latest")
	if err := os.WriteFile(h.phar, []byte("#!/usr/bin/env php\n<?php\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JABALI_COMPOSER_CHOICE_ROOT", t.TempDir())

	sp := t.TempDir()
	prevActive, prevRules, prevIni := phpDefenseActiveRulesPath, composerPHPDefenseRulesPath, composerPHPDefenseIniDir
	phpDefenseActiveRulesPath = filepath.Join(sp, "active.rules")
	composerPHPDefenseRulesPath = filepath.Join(sp, "composer.rules")
	composerPHPDefenseIniDir = filepath.Join(sp, "composer.d")
	t.Cleanup(func() {
		phpDefenseActiveRulesPath, composerPHPDefenseRulesPath, composerPHPDefenseIniDir = prevActive, prevRules, prevIni
	})
	if err := os.WriteFile(phpDefenseActiveRulesPath, []byte(testActiveRules), 0o644); err != nil {
		t.Fatal(err)
	}
	return h
}

const testActiveRules = `# Jabali Snuffleupagus active rules -- RENDERED, do not edit.
# mode=enforce rendered_at=2026-10-26T00:00:00Z

# --- 00-base.rules ---
sp.log_media("syslog");
sp.harden_random.enable();
sp.disable_function.function("system").drop();
sp.disable_function.function("exec").drop();
sp.disable_function.function("shell_exec").drop();
sp.disable_function.function("passthru").drop();
sp.disable_function.function("popen").drop();
sp.disable_function.function("proc_open").drop();
sp.disable_function.function("pcntl_exec").drop();
sp.disable_function.function("assert").drop();
sp.disable_function.function("phpinfo").drop().simulation();
`

func startRun(t *testing.T, p composerRunStartParams) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	return composerRunStartHandler(context.Background(), raw)
}

// A run starts as the tenant in their slice, as a oneshot unit whose result
// stays readable, and runs Composer on the domain's PHP inside the SSH
// sandbox with the Composer PHP Defense rules.
func TestComposerRunStart_RunsInTheSandboxOnTheDomainPHP(t *testing.T) {
	h := newComposerRunHost(t)
	f := &composerExecFake{}
	f.install(t)

	res, err := startRun(t, composerRunStartParams{
		RunID: testRunID, OSUser: h.user, Domain: "example.com", AppDir: "app",
		Command: "install", NoDev: true,
	})
	if err != nil {
		t.Fatalf("run_start: %v", err)
	}
	if r := res.(composerRunStartResponse); r.Unit != "jabali-composer-"+testRunID+".service" || r.PHPVersion != "8.4" {
		t.Fatalf("response = %+v", r)
	}
	runs := f.called("systemd-run ")
	if len(runs) != 1 {
		t.Fatalf("systemd-run calls = %q", runs)
	}
	home := filepath.Join(h.home, ".composer")
	want := "systemd-run --unit=jabali-composer-" + testRunID + ".service --no-block" +
		" --uid=" + h.user + " --gid=" + h.user + " --slice=jabali-user-" + h.user + ".slice" +
		" --service-type=oneshot --remain-after-exit --description=jabali composer install example.com" +
		" " + strings.Join(composerRunProps, " ") +
		" -- /usr/local/bin/jabali-ssh-shell --exec /usr/bin/env" +
		" COMPOSER_HOME=" + home + " COMPOSER_CACHE_DIR=" + home + "/cache COMPOSER_NO_INTERACTION=1" +
		" PHP_INI_SCAN_DIR=:" + composerPHPDefenseIniDir +
		" " + h.php + " " + h.phar + " install --no-dev --no-progress --no-interaction --no-ansi" +
		" --working-dir=" + filepath.Join(h.domainDir, "app")
	if runs[0] != want {
		t.Fatalf("systemd-run argv\n got %s\nwant %s", runs[0], want)
	}
	for _, prop := range []string{"--property=TimeoutStartSec=1800", "--property=MemoryMax=2G", "--property=TasksMax=256"} {
		if !strings.Contains(runs[0], prop) {
			t.Errorf("run has no %s", prop)
		}
	}
	if strings.Contains(runs[0], "--collect") {
		t.Error("--collect erases the run's exit status")
	}
	if _, err := os.Stat(composerPHPDefenseRulesPath); err != nil {
		t.Errorf("the Composer rules were not written: %v", err)
	}
	if fi, err := os.Lstat(home); err != nil || !fi.IsDir() {
		t.Errorf("~/.composer was not made: %v", err)
	}
}

// Packages follow the options; require keeps a constraint.
func TestComposerRunArgv_PackagesLast(t *testing.T) {
	p := composerRunStartParams{RunID: testRunID, OSUser: "alice", Domain: "example.com", Command: "require",
		Packages: []string{"monolog/monolog:^3.0", "guzzlehttp/guzzle"}}
	argv := composerRunArgv(p, composerCommands["require"], "/usr/bin/php8.4", "/usr/local/bin/composer-latest", "")
	tail := argv[len(argv)-7:]
	want := []string{"require", "--no-progress", "--no-interaction", "--no-ansi",
		"--working-dir=" + filepath.Join(homesRoot, "alice", "domains", "example.com"),
		"monolog/monolog:^3.0", "guzzlehttp/guzzle"}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("argv tail = %q, want %q", tail, want)
	}
	for _, a := range argv {
		if strings.HasPrefix(a, "PHP_INI_SCAN_DIR=") {
			t.Errorf("no PHP Defense on the server, yet the run sets %s", a)
		}
	}
}

// validate skips the checks that only matter for publishing a package: a
// hosted app with no description or license is fine to install (GH #2276).
func TestComposerRunArgv_ValidateSkipsPublishChecks(t *testing.T) {
	p := composerRunStartParams{RunID: testRunID, OSUser: "alice", Domain: "example.com", Command: "validate"}
	argv := composerRunArgv(p, composerCommands["validate"], "/usr/bin/php8.4", "/usr/local/bin/composer-latest", "")
	tail := argv[len(argv)-5:]
	want := []string{"validate", "--no-check-publish", "--no-interaction", "--no-ansi",
		"--working-dir=" + filepath.Join(homesRoot, "alice", "domains", "example.com")}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("argv tail = %q, want %q", tail, want)
	}
}

func TestComposerRunStart_RefusesWhatItCannotRun(t *testing.T) {
	h := newComposerRunHost(t)
	f := &composerExecFake{}
	f.install(t)
	ok := composerRunStartParams{RunID: testRunID, OSUser: h.user, Domain: "example.com", AppDir: "app", Command: "install"}

	for _, c := range []struct {
		name string
		edit func(*composerRunStartParams)
	}{
		{"run id not a ULID", func(p *composerRunStartParams) { p.RunID = "../x" }},
		{"unknown command", func(p *composerRunStartParams) { p.Command = "exec" }},
		{"global option as command", func(p *composerRunStartParams) { p.Command = "--working-dir=/" }},
		{"no_dev on a command without it", func(p *composerRunStartParams) { p.Command = "validate"; p.NoDev = true }},
		{"require without packages", func(p *composerRunStartParams) { p.Command = "require" }},
		{"packages on install", func(p *composerRunStartParams) { p.Packages = []string{"a/b"} }},
		{"option as package", func(p *composerRunStartParams) { p.Command = "require"; p.Packages = []string{"--dev"} }},
		{"bare name", func(p *composerRunStartParams) { p.Command = "require"; p.Packages = []string{"monolog"} }},
		{"constraint with $", func(p *composerRunStartParams) { p.Command = "require"; p.Packages = []string{"a/b:$HOME"} }},
		{"constraint with %", func(p *composerRunStartParams) { p.Command = "require"; p.Packages = []string{"a/b:%h"} }},
		{"constraint on update", func(p *composerRunStartParams) { p.Command = "update"; p.Packages = []string{"a/b:^1"} }},
		{"too many packages", func(p *composerRunStartParams) {
			p.Command = "remove"
			for i := 0; i <= composerMaxPackages; i++ {
				p.Packages = append(p.Packages, fmt.Sprintf("a/b%d", i))
			}
		}},
		{"app dir with ..", func(p *composerRunStartParams) { p.AppDir = "app/../.." }},
		{"absolute app dir", func(p *composerRunStartParams) { p.AppDir = "/etc" }},
		{"bad domain", func(p *composerRunStartParams) { p.Domain = "../x" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := ok
			c.edit(&p)
			if _, err := startRun(t, p); agentCode(t, err) != agentwire.CodeInvalidArgument {
				t.Fatalf("run_start = %v, want invalid_argument", err)
			}
		})
	}
	if runs := f.called("systemd-run"); len(runs) != 0 {
		t.Fatalf("a refused run still started: %q", runs)
	}
}

// The agent is root and the tenant owns the domain's directory: a link as
// the app directory, or on the way to it, is refused rather than followed.
func TestComposerRunStart_RefusesALinkedAppDir(t *testing.T) {
	h := newComposerRunHost(t)
	f := &composerExecFake{}
	f.install(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(h.domainDir, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"linked", "linked/sub", "missing"} {
		_, err := startRun(t, composerRunStartParams{RunID: testRunID, OSUser: h.user, Domain: "example.com", AppDir: dir, Command: "install"})
		if agentCode(t, err) != agentwire.CodeFailedPrecondition {
			t.Errorf("app_dir %q: run_start = %v, want failed_precondition", dir, err)
		}
	}
	if runs := f.called("systemd-run"); len(runs) != 0 {
		t.Fatalf("started through a link: %q", runs)
	}
}

func TestComposerRunStart_NeedsTheDomainToRunPHP(t *testing.T) {
	h := newComposerRunHost(t)
	f := &composerExecFake{}
	f.install(t)
	if err := os.WriteFile(filepath.Join(nginxSitesDir, "example.com.conf"), []byte("server {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := startRun(t, composerRunStartParams{RunID: testRunID, OSUser: h.user, Domain: "example.com", Command: "install"})
	if agentCode(t, err) != agentwire.CodeFailedPrecondition || !strings.Contains(err.Error(), "does not run PHP") {
		t.Fatalf("run_start = %v, want failed_precondition: does not run PHP", err)
	}
}

// A unit of the same name still loaded is a run already started.
func TestComposerRunStart_RefusesARunAlreadyThere(t *testing.T) {
	h := newComposerRunHost(t)
	f := &composerExecFake{show: "LoadState=loaded\nActiveState=activating\n"}
	f.install(t)
	_, err := startRun(t, composerRunStartParams{RunID: testRunID, OSUser: h.user, Domain: "example.com", Command: "install"})
	if agentCode(t, err) != agentwire.CodeAlreadyExists {
		t.Fatalf("run_start = %v, want already_exists", err)
	}
	if runs := f.called("systemd-run"); len(runs) != 0 {
		t.Fatalf("started a second unit: %q", runs)
	}
}

// The unit states a run goes through, as systemd 255 reports them for a
// oneshot unit with RemainAfterExit (checked on a test server).
func TestComposerRunState(t *testing.T) {
	zero, one := 0, 1
	for _, c := range []struct {
		name   string
		props  string
		state  string
		result string
		exit   *int
	}{
		{"running", "LoadState=loaded\nActiveState=activating\nSubState=start\nResult=success\nExecMainCode=0\nExecMainStatus=0", "running", "", nil},
		{"succeeded", "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainCode=1\nExecMainStatus=0", "succeeded", "success", &zero},
		{"exit 1", "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=exit-code\nExecMainCode=1\nExecMainStatus=1", "failed", "exit-code", &one},
		{"timeout", "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=timeout\nExecMainCode=2\nExecMainStatus=15", "timed_out", "timeout", nil},
		{"killed", "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=oom-kill\nExecMainCode=2\nExecMainStatus=9", "failed", "oom-kill", nil},
		{"stopped", "LoadState=loaded\nActiveState=inactive\nSubState=dead\nResult=success\nExecMainCode=0\nExecMainStatus=0", "gone", "", nil},
		{"no such unit", "LoadState=not-found\nActiveState=inactive", "gone", "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			props := map[string]string{}
			for _, l := range strings.Split(c.props, "\n") {
				k, v, _ := strings.Cut(l, "=")
				props[k] = v
			}
			state, result, exit := composerRunState(props)
			if state != c.state || result != c.result || !reflect.DeepEqual(exit, c.exit) {
				t.Fatalf("got %s %q %v, want %s %q %v", state, result, exit, c.state, c.result, c.exit)
			}
		})
	}
}

func runStatus(t *testing.T) composerRunStatusResponse {
	t.Helper()
	raw, _ := json.Marshal(composerRunIDParams{RunID: testRunID})
	res, err := composerRunStatusHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("run_status: %v", err)
	}
	return res.(composerRunStatusResponse)
}

// A run PHP Defense stopped is never reported as succeeded, even when the
// call's failure was swallowed and the run exited 0.
func TestComposerRunStatus_ABlockedRunNeverSucceeds(t *testing.T) {
	f := &composerExecFake{
		show: "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainCode=1\nExecMainStatus=0\n",
		log:  "Installing dependencies from lock file\nPHP Fatal error:  [snuffleupagus][0.0.0.0][disabled_function][drop] Aborted execution on call of the function 'assert' in /x.php on line 3\n",
	}
	f.install(t)
	r := runStatus(t)
	if r.State != "failed" || !r.PHPDefenseBlocked {
		t.Fatalf("state=%s blocked=%v, want failed and blocked", r.State, r.PHPDefenseBlocked)
	}
	if !strings.Contains(r.Log, "Installing dependencies") {
		t.Errorf("log = %q", r.Log)
	}
	if j := f.called("journalctl"); len(j) != 1 || !strings.Contains(j[0], "_SYSTEMD_UNIT=jabali-composer-"+testRunID+".service") {
		t.Errorf("journal read = %q, want the run unit's own lines", j)
	}

	// Simulation only logs: the run's result stands.
	f.log = "PHP Warning:  [snuffleupagus][0.0.0.0][disabled_function][simulation] Aborted execution on call of the function 'assert'\n"
	if r := runStatus(t); r.State != "succeeded" || r.PHPDefenseBlocked {
		t.Fatalf("simulation line: state=%s blocked=%v, want succeeded", r.State, r.PHPDefenseBlocked)
	}
}

// The log keeps the run's last bytes, and says it was cut.
func TestComposerRunStatus_KeepsTheEndOfALongLog(t *testing.T) {
	long := strings.Repeat("x", 1023) + "\n"
	f := &composerExecFake{
		show: "LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainCode=1\nExecMainStatus=2\n",
		log:  strings.Repeat(long, 300) + "the last line\n",
	}
	f.install(t)
	r := runStatus(t)
	if len(r.Log) != composerRunLogMax || !strings.HasSuffix(r.Log, "the last line\n") || !r.LogTruncated {
		t.Fatalf("log len=%d truncated=%v", len(r.Log), r.LogTruncated)
	}
	if r.State != "failed" || r.ExitCode == nil || *r.ExitCode != 2 {
		t.Fatalf("state=%s exit=%v", r.State, r.ExitCode)
	}
	f.log = "short\n"
	if r := runStatus(t); r.LogTruncated || r.Log != "short\n" {
		t.Fatalf("short log: %q truncated=%v", r.Log, r.LogTruncated)
	}
}

func TestComposerRunStop(t *testing.T) {
	raw, _ := json.Marshal(composerRunIDParams{RunID: testRunID})
	unit := "jabali-composer-" + testRunID + ".service"

	f := &composerExecFake{show: "LoadState=loaded\nActiveState=failed\n"}
	f.install(t)
	if _, err := composerRunStopHandler(context.Background(), raw); err != nil {
		t.Fatalf("run_stop: %v", err)
	}
	if got := append(f.called("systemctl stop"), f.called("systemctl reset-failed")...); !reflect.DeepEqual(got, []string{"systemctl stop " + unit, "systemctl reset-failed " + unit}) {
		t.Fatalf("calls = %q", got)
	}

	gone := &composerExecFake{show: "LoadState=not-found\n"}
	gone.install(t)
	if _, err := composerRunStopHandler(context.Background(), raw); err != nil {
		t.Fatalf("run_stop of a gone unit: %v", err)
	}
	if got := gone.called("systemctl stop"); len(got) != 0 {
		t.Fatalf("stopped a unit that is not there: %q", got)
	}

	bad, _ := json.Marshal(composerRunIDParams{RunID: "*"})
	if _, err := composerRunStopHandler(context.Background(), bad); agentCode(t, err) != agentwire.CodeInvalidArgument {
		t.Fatalf("run_stop with a glob = %v, want invalid_argument", err)
	}
}

// The Composer copy lifts only the exec-family bans and logs where the run
// can see it; every other rule stays as active.rules has it.
func TestRenderComposerPHPDefenseRules(t *testing.T) {
	got := string(renderComposerPHPDefenseRules([]byte(testActiveRules)))
	for f := range phpDefenseExecFunctions {
		ban := `sp.disable_function.function("` + f + `").drop();`
		if !strings.Contains(got, "# allowed for Composer runs: "+ban) {
			t.Errorf("%s is still banned", f)
		}
	}
	// Symfony Process calls phpinfo() before it starts any process.
	if !strings.Contains(got, `# allowed for Composer runs: sp.disable_function.function("phpinfo").drop().simulation();`) {
		t.Error("phpinfo is still banned")
	}
	for _, keep := range []string{
		`sp.disable_function.function("assert").drop();`,
		`sp.harden_random.enable();`,
		`sp.log_media("php");`,
	} {
		if !strings.Contains(got, "\n"+keep+"\n") {
			t.Errorf("missing %s", keep)
		}
	}
	if strings.Contains(got, `log_media("syslog")`) {
		t.Error("still logs to syslog, which the sandbox can't reach")
	}
	// Simulation-mode bans are lifted too.
	sim := strings.ReplaceAll(testActiveRules, `("proc_open").drop();`, `("proc_open").drop().simulation();`)
	if !strings.Contains(string(renderComposerPHPDefenseRules([]byte(sim))), `# allowed for Composer runs: sp.disable_function.function("proc_open").drop().simulation();`) {
		t.Error("a simulation-mode proc_open ban is kept")
	}
}

func TestEnsureComposerPHPDefense(t *testing.T) {
	newComposerRunHost(t)
	dir, err := ensureComposerPHPDefense()
	if err != nil || dir != composerPHPDefenseIniDir {
		t.Fatalf("ensure = %q, %v", dir, err)
	}
	ini, err := os.ReadFile(filepath.Join(composerPHPDefenseIniDir, composerPHPDefenseIniName))
	if err != nil || !strings.Contains(string(ini), "sp.configuration_file="+composerPHPDefenseRulesPath+"\n") {
		t.Fatalf("ini = %q, %v", ini, err)
	}

	// Without PHP Defense there is nothing to select, and nothing left behind.
	if err := os.Remove(phpDefenseActiveRulesPath); err != nil {
		t.Fatal(err)
	}
	dir, err = ensureComposerPHPDefense()
	if err != nil || dir != "" {
		t.Fatalf("ensure without PHP Defense = %q, %v", dir, err)
	}
	for _, p := range []string{composerPHPDefenseRulesPath, filepath.Join(composerPHPDefenseIniDir, composerPHPDefenseIniName)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is left behind: %v", p, err)
		}
	}
}

func detect(t *testing.T, h composerRunHost, docroot string) (composerDetectResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(composerDetectParams{OSUser: h.user, Domain: "example.com", Docroot: docroot})
	res, err := composerDetectHandler(context.Background(), raw)
	if err != nil {
		return composerDetectResponse{}, err
	}
	return res.(composerDetectResponse), nil
}

func TestComposerDetect(t *testing.T) {
	h := newComposerRunHost(t)
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(h.domainDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app/composer.json")
	write("app/composer.lock")
	write("composer.json")
	if err := os.MkdirAll(filepath.Join(h.domainDir, "app", "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.domainDir, "app", "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A linked composer.json is not counted: the agent never follows it.
	if err := os.Symlink("/etc/hostname", filepath.Join(h.domainDir, "app", "public", "composer.json")); err != nil {
		t.Fatal(err)
	}

	r, err := detect(t, h, filepath.Join(h.domainDir, "app", "public"))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	want := []composerDetectDir{{Dir: "app", HasLock: true, HasVendor: true}, {Dir: ""}}
	if !reflect.DeepEqual(r.Dirs, want) {
		t.Fatalf("dirs = %+v, want %+v", r.Dirs, want)
	}
	if r.PHPVersion != "8.4" || r.PHPError != "" || r.ComposerError != "" {
		t.Fatalf("php=%q php_error=%q composer_error=%q", r.PHPVersion, r.PHPError, r.ComposerError)
	}

	// Nothing found is an empty list, not null.
	if err := os.Remove(filepath.Join(h.domainDir, "composer.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(h.domainDir, "app")); err != nil {
		t.Fatal(err)
	}
	r, err = detect(t, h, h.domainDir)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if b, _ := json.Marshal(r); !strings.Contains(string(b), `"dirs":[]`) {
		t.Fatalf("detect = %s, want an empty dirs list", b)
	}

	for _, docroot := range []string{"/etc", filepath.Join(h.home, "domains", "other.com"), "relative/path", h.domainDir + "/../other.com"} {
		if _, err := detect(t, h, docroot); agentCode(t, err) != agentwire.CodeInvalidArgument {
			t.Errorf("docroot %q: detect = %v, want invalid_argument", docroot, err)
		}
	}
}

// The SSH sandbox never jails root (GH #658), so a run as root or a system
// account would run a composer.json's scripts outside it.
func TestComposerRunStart_OnlyAsAHostingUser(t *testing.T) {
	newComposerRunHost(t)
	f := &composerExecFake{}
	f.install(t)
	for _, u := range []string{"root", "daemon", "nobody"} {
		if _, err := user.Lookup(u); err != nil {
			continue
		}
		_, err := startRun(t, composerRunStartParams{RunID: testRunID, OSUser: u, Domain: "example.com", Command: "install"})
		if agentCode(t, err) != agentwire.CodeInvalidArgument || !strings.Contains(err.Error(), "not a hosting user") {
			t.Errorf("run as %s = %v, want invalid_argument: not a hosting user", u, err)
		}
	}
	if runs := f.called("systemd-run"); len(runs) != 0 {
		t.Fatalf("started as a system account: %q", runs)
	}
}

// The panel's side of the composer.* contract (GH #2276): what the panel
// sends decodes into these handlers' params with every field kept, and what
// they answer decodes into what the panel expects. The panel decodes the
// same fixtures (TestContract in panel-api/internal/composerrun).
func TestComposerRuns_PanelContract(t *testing.T) {
	dir := "../../../panel-api/internal/agent/testdata/"
	for _, c := range []struct {
		file string
		into any
	}{
		{"composer_detect_request.json", &composerDetectParams{}},
		{"composer_detect_response.json", &composerDetectResponse{}},
		{"composer_run_start_request.json", &composerRunStartParams{}},
		{"composer_run_start_response.json", &composerRunStartResponse{}},
		{"composer_run_id_request.json", &composerRunIDParams{}},
		{"composer_run_status_response.json", &composerRunStatusResponse{}},
		{"composer_run_stop_response.json", &composerRunStopResponse{}},
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
			t.Errorf("%s: the agent drops or renames fields:\nwant %s\ngot  %s", c.file, raw, again)
		}
	}
}

// GH #2276: a domain's project folder can be outside its own directory, e.g.
// one composer.json and vendor/ in ~/domains/ shared by several domains. The
// run's working directory is that folder.
func TestComposerRunStart_RunsInTheProjectFolder(t *testing.T) {
	h := newComposerRunHost(t)
	if err := os.MkdirAll(filepath.Join(h.home, "domains", "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"domains", "domains/shared", "domains/example.com/app"} {
		f := &composerExecFake{}
		f.install(t)
		if _, err := startRun(t, composerRunStartParams{
			RunID: testRunID, OSUser: h.user, Domain: "example.com", WorkDir: dir, Command: "install",
		}); err != nil {
			t.Fatalf("work_dir %q: run_start: %v", dir, err)
		}
		runs := f.called("systemd-run ")
		if len(runs) != 1 || !strings.HasSuffix(runs[0], " --working-dir="+filepath.Join(h.home, dir)) {
			t.Fatalf("work_dir %q: systemd-run = %q", dir, runs)
		}
	}
}

// A project folder stays in domains/: the home holds .ssh and ~/.composer,
// and nothing outside it is the tenant's to run Composer in.
func TestComposerRunStart_RefusesAProjectFolderOutsideDomains(t *testing.T) {
	h := newComposerRunHost(t)
	f := &composerExecFake{}
	f.install(t)
	for _, dir := range []string{".", "..", ".ssh", "/etc", "/home/" + h.user + "/domains", "domainsx", "domains/", "domains//x", "domains/..", "domains/../.ssh", "domains/./x", "domains/a b", "domains/$HOME"} {
		_, err := startRun(t, composerRunStartParams{RunID: testRunID, OSUser: h.user, Domain: "example.com", WorkDir: dir, Command: "install"})
		if agentCode(t, err) != agentwire.CodeInvalidArgument {
			t.Errorf("work_dir %q: run_start = %v, want invalid_argument", dir, err)
		}
	}
	if runs := f.called("systemd-run"); len(runs) != 0 {
		t.Fatalf("a refused run still started: %q", runs)
	}
}

// The agent is root and the tenant owns domains/: a project folder that is a
// link, or missing, is refused rather than followed.
func TestComposerRunStart_RefusesALinkedProjectFolder(t *testing.T) {
	h := newComposerRunHost(t)
	f := &composerExecFake{}
	f.install(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(h.home, "domains", "linked")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"domains/linked", "domains/missing"} {
		_, err := startRun(t, composerRunStartParams{RunID: testRunID, OSUser: h.user, Domain: "example.com", WorkDir: dir, Command: "install"})
		if agentCode(t, err) != agentwire.CodeFailedPrecondition {
			t.Errorf("work_dir %q: run_start = %v, want failed_precondition", dir, err)
		}
	}
	if runs := f.called("systemd-run"); len(runs) != 0 {
		t.Fatalf("started through a link: %q", runs)
	}
}

func detectIn(t *testing.T, h composerRunHost, docroot, projectDir string) (composerDetectResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(composerDetectParams{OSUser: h.user, Domain: "example.com", Docroot: docroot, ProjectDir: &projectDir})
	res, err := composerDetectHandler(context.Background(), raw)
	if err != nil {
		return composerDetectResponse{}, err
	}
	return res.(composerDetectResponse), nil
}

// A domain with a project folder looks only there, not from its docroot up.
func TestComposerDetect_ProjectFolder(t *testing.T) {
	h := newComposerRunHost(t)
	domains := filepath.Join(h.home, "domains")
	for _, f := range []string{filepath.Join(domains, "composer.json"), filepath.Join(domains, "composer.lock"), filepath.Join(h.domainDir, "composer.json")} {
		if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(domains, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := detectIn(t, h, h.domainDir, "domains")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	want := &composerDetectProject{Dir: "domains", Exists: true, HasComposerJSON: true, HasLock: true, HasVendor: true}
	if !reflect.DeepEqual(r.Project, want) || len(r.Dirs) != 0 {
		t.Fatalf("project = %+v dirs = %+v, want %+v and no dirs", r.Project, r.Dirs, want)
	}
	if r.PHPVersion != "8.4" {
		t.Fatalf("php = %q", r.PHPVersion)
	}

	// A folder that isn't there, or is a link, says so.
	if err := os.Symlink(domains, filepath.Join(domains, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"domains/missing", "domains/linked"} {
		r, err = detectIn(t, h, h.domainDir, dir)
		if err != nil {
			t.Fatalf("detect %s: %v", dir, err)
		}
		if r.Project == nil || r.Project.Exists || r.Project.HasComposerJSON {
			t.Fatalf("detect %s: project = %+v, want a missing folder", dir, r.Project)
		}
	}

	for _, dir := range []string{"", ".ssh", "domains/../.ssh", "/etc"} {
		if _, err := detectIn(t, h, h.domainDir, dir); agentCode(t, err) != agentwire.CodeInvalidArgument {
			t.Errorf("project_dir %q: detect = %v, want invalid_argument", dir, err)
		}
	}
}
