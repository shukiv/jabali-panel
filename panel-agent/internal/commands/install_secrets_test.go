package commands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// install_secrets_test.go — no one-click installer hands a password to
// systemd-run, on the unit's command line or in its environment; passwords
// go on stdin (php_script_run.go).

const (
	testDBSecret    = "dbSecretQ7w2x"
	testAdminSecret = "adminSecretZ9k4v"
)

type capturedRun struct {
	name string
	args []string
	cmd  *exec.Cmd
	// shim is the content of the file the run's last argument names, read
	// when the command was built.
	shim string
}

// stdin returns what the run was given on stdin.
func (r *capturedRun) stdin() string {
	rs, ok := r.cmd.Stdin.(io.ReadSeeker)
	if !ok {
		return ""
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	b, _ := io.ReadAll(rs)
	return string(b)
}

// captureSystemdRun replaces execCommandContext: each call's argv and the
// command are recorded; the command itself does nothing.
func captureSystemdRun(t *testing.T) *[]*capturedRun {
	t.Helper()
	dir := t.TempDir()
	runs := &[]*capturedRun{}
	prevExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		r := &capturedRun{name: name, args: append([]string(nil), args...), cmd: exec.CommandContext(ctx, "true")}
		if len(args) > 0 {
			if b, err := os.ReadFile(args[len(args)-1]); err == nil {
				r.shim = string(b)
			}
		}
		*runs = append(*runs, r)
		return r.cmd
	}
	prevStaging := phpShimStagingDir
	phpShimStagingDir = func(prefix string) (string, error) { return os.MkdirTemp(dir, prefix) }
	t.Cleanup(func() {
		execCommandContext = prevExec
		phpShimStagingDir = prevStaging
	})
	return runs
}

// assertSecretsOnlyOnStdin fails if any systemd-run argument carries a
// secret (that covers --setenv=), or if a secret never reaches any run's
// stdin (the installer would no longer get it).
func assertSecretsOnlyOnStdin(t *testing.T, runs []*capturedRun, secrets ...string) {
	t.Helper()
	var stdin strings.Builder
	sawRun := false
	for _, r := range runs {
		if r.name != "systemd-run" {
			continue
		}
		sawRun = true
		for _, a := range r.args {
			for _, s := range secrets {
				if strings.Contains(a, s) {
					t.Errorf("systemd-run argument %q carries a secret", a)
				}
			}
		}
		in := r.stdin()
		for _, s := range secrets {
			if strings.Contains(in, s) && r.shim != phpScriptShim && r.shim != itflowSetupShim {
				t.Errorf("the run given the secrets on stdin is not one of the shims (argv %q)", r.args)
			}
		}
		stdin.WriteString(in)
	}
	if !sawRun {
		t.Fatal("no systemd-run call recorded")
	}
	for _, s := range secrets {
		if !strings.Contains(stdin.String(), s) {
			t.Errorf("secret %q never reaches the installer on stdin", s)
		}
	}
}

func TestInstallers_PasswordsReachTheInstallerOnlyOnStdin(t *testing.T) {
	const (
		user        = "alice"
		installPath = "/home/alice/domains/example.com/public_html"
	)
	ctx := context.Background()
	cases := []struct {
		name    string
		secrets []string
		run     func() error
	}{
		{"moodle", []string{testDBSecret, testAdminSecret}, func() error {
			return runMoodleCLIInstaller(ctx, moodleInstallReq{
				OSUser: user, SiteURL: "https://example.com", DBName: "db", DBUser: "dbu", DBPassword: testDBSecret,
				SiteTitle: "Site", AdminUser: "admin", AdminPass: testAdminSecret, AdminEmail: "a@example.com",
			}, installPath, "/home/alice/domains/example.com/moodledata")
		}},
		{"mediawiki", []string{testDBSecret, testAdminSecret}, func() error {
			return runMediaWikiCLIInstaller(ctx, mediawikiInstallReq{
				OSUser: user, SiteURL: "https://example.com", DBName: "db", DBUser: "dbu", DBPassword: testDBSecret,
				SiteTitle: "Wiki", AdminUser: "Admin", AdminPass: testAdminSecret, AdminEmail: "a@example.com",
			}, installPath)
		}},
		{"joomla", []string{testDBSecret, testAdminSecret}, func() error {
			return runJoomlaCLIInstaller(ctx, joomlaInstallReq{
				OSUser: user, SiteURL: "https://example.com", DBName: "db", DBUser: "dbu", DBPassword: testDBSecret,
				SiteTitle: "Site", AdminUser: "admin", AdminPass: testAdminSecret, AdminEmail: "a@example.com",
			}, installPath)
		}},
		{"drupal", []string{testDBSecret, testAdminSecret}, func() error {
			return runDrushSiteInstall(ctx, drupalInstallReq{
				OSUser: user, SiteURL: "https://example.com", DBName: "db", DBUser: "dbu", DBPassword: testDBSecret,
				SiteTitle: "Site", AdminUser: "admin", AdminPass: testAdminSecret, AdminEmail: "a@example.com",
			}, "/usr/bin/php8.4", installPath)
		}},
		{"prestashop", []string{testDBSecret, testAdminSecret}, func() error {
			return runPrestaShopCLIInstaller(ctx, prestashopInstallReq{
				OSUser: user, SiteURL: "https://example.com", DBName: "db", DBUser: "dbu", DBPassword: testDBSecret,
				SiteTitle: "Shop", AdminPass: testAdminSecret, AdminEmail: "a@example.com",
			}, installPath)
		}},
		{"opencart", []string{testDBSecret, testAdminSecret}, func() error {
			return runOpenCartCLIInstaller(ctx, opencartInstallReq{
				OSUser: user, SiteURL: "https://example.com", DBName: "db", DBUser: "dbu", DBPassword: testDBSecret,
				AdminUser: "admin", AdminPass: testAdminSecret, AdminEmail: "a@example.com",
			}, installPath)
		}},
		{"itflow", []string{testDBSecret, testAdminSecret}, func() error {
			return runITFlowSetupCLI(ctx, itflowInstallReq{
				OSUser: user, SiteURL: "https://example.com", DBName: "db", DBUser: "dbu", DBPassword: testDBSecret,
				CompanyName: "Acme", AdminName: "Admin", AdminEmail: "a@example.com", AdminPass: testAdminSecret,
			}, installPath, "example.com")
		}},
		{"openemr", []string{testDBSecret, testAdminSecret}, func() error {
			_, err := openemrInstallRun(openemrInstallReq{
				OSUser: user, SiteURL: "https://example.com", SiteTitle: "Clinic", AdminUser: "admin",
				AdminPass: testAdminSecret, DBName: "db", DBUser: "dbu", DBPassword: testDBSecret,
			}, installPath, "localhost").run(ctx)
			return err
		}},
		{"osticket", []string{testDBSecret, testAdminSecret}, func() error {
			_, err := osticketInstallRun(osticketInstallReq{
				OSUser: user, HelpdeskName: "Help", HelpdeskEmail: "h@example.com", AdminFirst: "A", AdminLast: "B",
				AdminEmail: "a@example.com", AdminUsername: "admin", AdminPass: testAdminSecret,
				DBName: "db", DBUser: "dbu", DBPassword: testDBSecret,
			}, installPath, "localhost", "example.com").run(ctx)
			return err
		}},
		{"invoiceshelf", []string{testAdminSecret}, func() error {
			_, err := invoiceshelfFinaliseRun(invoiceshelfInstallReq{
				OSUser: user, SiteTitle: "Acme", Currency: "USD", Country: "US",
				AdminEmail: "a@example.com", AdminPass: testAdminSecret,
			}, "php8.4", installPath, "/tmp/psysh-alice").run(ctx)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runs := captureSystemdRun(t)
			_ = tc.run() // the stand-in installer prints nothing; only what reached it matters
			assertSecretsOnlyOnStdin(t, *runs, tc.secrets...)
		})
	}
}

func TestBuildSystemdRunCmd_DescriptionNamesOnlyTheBinary(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"/usr/bin/php8.4", "/x/shim.php", "--pass=" + testAdminSecret}, "--description=jabali-agent task: php8.4"},
		{[]string{"--working-directory=/home/alice/x", "/usr/bin/php8.4", "--pass=" + testAdminSecret}, "--description=jabali-agent task: php8.4"},
	}
	for _, tc := range cases {
		cmd := buildSystemdRunCmd(context.Background(), "alice", tc.args...)
		var desc []string
		for _, a := range cmd.Args {
			if strings.HasPrefix(a, "--description=") {
				desc = append(desc, a)
			}
		}
		if len(desc) != 1 || desc[0] != tc.want {
			t.Errorf("args %q: description flags %q, want exactly %q", tc.args, desc, tc.want)
		}
	}
}

func TestPHPScriptRun_RejectsBadInput(t *testing.T) {
	ctx := context.Background()
	if _, err := (phpScriptRun{OSUser: "alice", PHP: "php", Script: "relative/x.php"}).cmd(ctx, "/s.php"); err == nil {
		t.Error("relative script path accepted")
	}
	if _, err := (phpScriptRun{OSUser: "alice", PHP: "php", Script: "/x.php", Env: map[string]string{"A=B": "c"}}).cmd(ctx, "/s.php"); err == nil {
		t.Error("environment name with '=' accepted")
	}
}

// phpForTest returns a PHP CLI binary, or skips.
func phpForTest(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"php", "php8.4", "php8.3", "php8.2"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no php binary")
	return ""
}

func TestPHPScriptShim_RunsTheScriptWithArgvAndEnvironment(t *testing.T) {
	php := phpForTest(t)
	dir := t.TempDir()
	shim := filepath.Join(dir, "shim.php")
	if err := os.WriteFile(shim, []byte(phpScriptShim), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "target.php")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env php\n<?php\necho json_encode(['argv' => $argv, 'server_argv' => $_SERVER['argv'], 'argc' => $argc, 'env' => getenv('JABALI_T'), 'self' => $_SERVER['SCRIPT_FILENAME'], 'leftover' => isset($jabali_in)]);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--pass=" + testAdminSecret, "a b", "--x=&$(id)'\"\\", ""}
	in, _ := json.Marshal(phpScriptInput{Script: script, Args: args, Env: map[string]string{"JABALI_T": testDBSecret}})
	cmd := exec.Command(php, shim)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("shim: %v (%s)", err, out)
	}
	var got struct {
		Argv       []string `json:"argv"`
		ServerArgv []string `json:"server_argv"`
		Argc       int      `json:"argc"`
		Env        string   `json:"env"`
		Self       string   `json:"self"`
		Leftover   bool     `json:"leftover"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	want := append([]string{script}, args...)
	if strings.Join(got.Argv, "\x00") != strings.Join(want, "\x00") || strings.Join(got.ServerArgv, "\x00") != strings.Join(want, "\x00") || got.Argc != len(want) {
		t.Errorf("argv %q / %q (argc %d), want %q", got.Argv, got.ServerArgv, got.Argc, want)
	}
	if got.Env != testDBSecret {
		t.Errorf("getenv = %q, want %q", got.Env, testDBSecret)
	}
	if got.Self != script {
		t.Errorf("SCRIPT_FILENAME = %q, want %q", got.Self, script)
	}
	if got.Leftover {
		t.Error("the shim's input is still a global in the script")
	}
}

func TestITFlowSetupShim_FeedsTheOptionsAndRemovesItsCopy(t *testing.T) {
	php := phpForTest(t)
	dir := t.TempDir()
	shim := filepath.Join(dir, "shim.php")
	if err := os.WriteFile(shim, []byte(itflowSetupShim), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(dir, "itflow")
	if err := os.MkdirAll(filepath.Join(install, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	setup := "#!/usr/bin/env php\n<?php\nchdir(__DIR__);\n$shortopts = \"\";\n$longopts = [\"password:\", \"non-interactive\"];\n" +
		itflowSetupGetopt + "\necho json_encode(['options' => $options, 'cwd' => getcwd(), 'ni' => isset($options['non-interactive'])]);\n"
	setupPath := filepath.Join(install, "scripts", "setup_cli.php")
	if err := os.WriteFile(setupPath, []byte(setup), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func() ([]byte, error) {
		in, _ := json.Marshal(itflowSetupInput{InstallPath: install, Options: map[string]any{"password": testDBSecret, "non-interactive": false}})
		cmd := exec.Command(php, shim)
		cmd.Stdin = strings.NewReader(string(in))
		return cmd.CombinedOutput()
	}
	out, err := run()
	if err != nil {
		t.Fatalf("shim: %v (%s)", err, out)
	}
	var got struct {
		Options map[string]any `json:"options"`
		Cwd     string         `json:"cwd"`
		NI      bool           `json:"ni"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if got.Options["password"] != testDBSecret || !got.NI {
		t.Errorf("options %v (non-interactive set: %v)", got.Options, got.NI)
	}
	if got.Cwd != filepath.Join(install, "scripts") {
		t.Errorf("cwd %q, want the scripts dir", got.Cwd)
	}
	if _, err := os.Stat(filepath.Join(install, "scripts", itflowSetupCopyName)); !os.IsNotExist(err) {
		t.Errorf("setup copy left behind (stat err %v)", err)
	}

	// A setup_cli.php that reads its options some other way is refused.
	if err := os.WriteFile(setupPath, []byte(strings.Replace(setup, itflowSetupGetopt, "$options = getopt(\"\", $longopts);", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err == nil || !strings.Contains(string(out), "SETUP_FAIL") {
		t.Errorf("changed setup_cli.php ran: err %v, output %q", err, out)
	}
}
