package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

func TestCronApplyMissingUsername(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "",
		"job_id": "job1",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing username")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyMissingJobID(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing job_id")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyMissingCommand(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "job1",
		"name": "Test Job",
		"command": "",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyMissingSchedule(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "job1",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing schedule")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyInvalidCommand(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "job1",
		"name": "Test Job",
		"command": "invalidcmd arg1",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for invalid command")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyInvalidSchedule(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "job1",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "invalid schedule",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for invalid schedule")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyUnknownUser(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "999",
		"username": "nonexistentuser12345",
		"job_id": "job1",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for unknown user")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	// Depending on whether cronvalidate rejects the command first,
	// we might get CodeInvalidArgument before CodeNotFound
	if agentErr.Code != agentwire.CodeNotFound && agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeNotFound or CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyValidParamsStructure(t *testing.T) {
	// Test valid structure but user/linger not available
	currentUser := os.Getenv("USER")
	if currentUser == "" {
		t.Skip("USER env not set, skipping")
	}

	params := json.RawMessage(fmt.Sprintf(`{
		"user_id": "1",
		"username": "%s",
		"job_id": "test-job-123",
		"name": "Test Cron Job",
		"command": "wp cron event list",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`, currentUser))

	result, err := cronApplyHandler(context.Background(), params)
	if err != nil {
		// Expected to fail due to linger check, but structure should be valid
		agentErr, ok := err.(*agentwire.AgentError)
		if ok && agentErr.Code == "user_not_lingering" {
			// This is expected for most users
			return
		}
	}

	if result != nil {
		resp, ok := result.(*cronApplyResponse)
		if !ok {
			t.Fatalf("expected cronApplyResponse, got %T", result)
		}
		if resp.ServicePath == "" || resp.TimerPath == "" {
			t.Error("response missing service_path or timer_path")
		}
	}
}

func TestSingleQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"with'quote", "'with'\\''quote'"},
		{"path/to/file", "'path/to/file'"},
		{"arg with 'multiple' quotes", "'arg with '\\''multiple'\\'' quotes'"},
	}

	for _, tt := range tests {
		got := singleQuote(tt.input)
		if got != tt.expected {
			t.Errorf("singleQuote(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBuildCronServiceContent(t *testing.T) {
	cmd := &cronvalidate.Command{
		Argv: []string{"wp", "cron", "event", "list"},
	}
	ownedDocroots := []string{"/var/www/site1"}

	content := buildCronServiceContent("job1", "Test Job", cmd, "testuser", ownedDocroots)

	// Verify structure
	if !contains(content, "[Unit]") {
		t.Error("missing [Unit] section")
	}
	if !contains(content, "[Service]") {
		t.Error("missing [Service] section")
	}
	if !contains(content, "Type=oneshot") {
		t.Error("missing Type=oneshot")
	}
	if !contains(content, "ExecStart=") {
		t.Error("missing ExecStart=")
	}
	if !contains(content, "WorkingDirectory=%h") {
		t.Error("missing WorkingDirectory=%h")
	}
	// GH #184: the per-user CLI php wrapper dir must lead PATH so a cron
	// `php`/`wp` runs the user's pinned version, not the host default.
	if !contains(content, "Environment=PATH=/home/testuser/.jabali/bin:") {
		t.Error("cron PATH must prepend the per-user .jabali/bin wrapper dir")
	}

	// GH #299: systemd resolves the ExecStart binary against the manager's
	// path, not Environment=PATH, so the unit must add the per-user wrapper
	// dir via ExecSearchPath for bare `php`/`php8.5` to follow the user's
	// pinned version instead of /usr/bin/php.
	//
	// JAB-182: assert the FULL value, not just the prefix. The prefix-only
	// assertion this replaced passed happily while ExecSearchPath listed the
	// wrapper dir and nothing else, which is what broke every non-php cron.
	if !contains(content, "ExecSearchPath=/home/testuser/.jabali/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin\n") {
		t.Error("cron unit ExecSearchPath must be the wrapper dir followed by the standard system dirs")
	}
}

// TestCronExecSearchPathIncludesSystemDirs guards a failure whose whole
// signature was silence: 5,650 cron jobs on one host exiting 203/EXEC in 30
// days, having run no user code at all.
//
// ExecSearchPath REPLACES systemd's default binary search path — it does not
// prepend to it. Listing only /home/<user>/.jabali/bin therefore made every
// bare command that is not a jabali-provided wrapper unresolvable. `php` and
// `php8.5` kept working, because those are precisely the names the wrapper dir
// supplies, so the unit looked correct while `wp`, `git`, `rsync`, `node` and
// `composer` all died before executing a line.
//
// Confirmed against systemd 255 rather than inferred from the docs:
//
//	systemd-run --property=ExecSearchPath=/tmp/empty --wait wp --version  -> 203
//	systemd-run --property=ExecSearchPath=/tmp/empty --wait git --version -> 203
//	systemd-run --wait wp --version                                       -> runs
//
// The wrapper dir must stay FIRST (that is GH #299's pinned-version fix), and
// the system dirs must follow.
func TestCronExecSearchPathIncludesSystemDirs(t *testing.T) {
	cmd := &cronvalidate.Command{Argv: []string{"wp", "cron", "event", "run", "--due-now"}}
	content := buildCronServiceContent("job1", "Nightly", cmd, "testuser", []string{"/var/www/site1"})

	searchPath := unitDirectiveValue(content, "ExecSearchPath")
	if searchPath == "" {
		t.Fatal("cron unit has no ExecSearchPath — bare `php` would resolve to the " +
			"system default instead of the user's pinned version (GH #299)")
	}

	dirs := strings.Split(searchPath, ":")
	if dirs[0] != "/home/testuser/.jabali/bin" {
		t.Errorf("ExecSearchPath starts with %q, want the per-user wrapper dir first "+
			"so a bare `php` follows the user's pinned version", dirs[0])
	}
	for _, want := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin"} {
		found := false
		for _, d := range dirs {
			if d == want {
				found = true
			}
		}
		if !found {
			t.Errorf("ExecSearchPath is missing %s. It REPLACES systemd's default "+
				"search path, so anything not listed here fails 203/EXEC before the "+
				"job runs — wp, git, rsync, node, composer (JAB-182). Got: %s",
				want, searchPath)
		}
	}

	// The two must not drift: ExecSearchPath resolves the ExecStart binary,
	// Environment=PATH is what a `#!/usr/bin/env php` shebang reads. A command
	// resolvable by one and not the other fails in a way that looks like the
	// script is broken.
	if envPath := unitDirectiveValue(content, "Environment"); envPath != "PATH="+searchPath {
		t.Errorf("Environment=%q and ExecSearchPath=%q disagree; a command found by "+
			"one and not the other fails confusingly", envPath, searchPath)
	}
}

// unitDirectiveValue returns the value of the first `key=` line in a systemd
// unit, or "" when absent.
func unitDirectiveValue(unit, key string) string {
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

func TestBuildCronTimerContent(t *testing.T) {
	content, err := buildCronTimerContent("job1", "0 * * * *")
	if err != nil {
		t.Fatalf("buildCronTimerContent: %v", err)
	}

	// Verify structure
	if !contains(content, "[Unit]") {
		t.Error("missing [Unit] section")
	}
	if !contains(content, "[Timer]") {
		t.Error("missing [Timer] section")
	}
	if !contains(content, "[Install]") {
		t.Error("missing [Install] section")
	}
	// 5-field cron is translated to systemd OnCalendar (NOT raw cron,
	// which yields "Loaded: bad-setting"). "0 * * * *" → hourly at :00.
	if !contains(content, "OnCalendar=*-*-* *:0:00") {
		t.Errorf("OnCalendar not translated to systemd format; got:\n%s", content)
	}
	if !contains(content, "Unit=jabali-cron-job1.service") {
		t.Error("missing Unit setting")
	}
	if !contains(content, "WantedBy=timers.target") {
		t.Error("missing WantedBy setting")
	}
}

func TestCheckUserLinger(t *testing.T) {
	currentUser := os.Getenv("USER")
	if currentUser == "" {
		t.Skip("USER env not set")
	}

	// Check for current user (expected to fail unless linger is enabled)
	err := checkUserLinger(context.Background(), currentUser)
	// err may be nil or non-nil depending on system state, just ensure it doesn't panic
	_ = err
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	// File doesn't exist yet
	if fileExists(testFile) {
		t.Error("fileExists returned true for non-existent file")
	}

	// Create file
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// File exists now
	if !fileExists(testFile) {
		t.Error("fileExists returned false for existing file")
	}
}

// TestBuildCronServiceContent_HTTPTrigger locks the GH #400 Part B unit
// shape: a curl/wget self-domain cron is emitted as the rebind-safe
// wrapper invocation, single-quoted, with NO docroot ExecStartPre.
func TestBuildCronServiceContent_HTTPTrigger(t *testing.T) {
	cmd := &cronvalidate.Command{
		Kind: cronvalidate.KindHTTPTrigger,
		URL:  "https://own.example.com/wp-cron.php?doing_wp_cron",
		Argv: []string{"/usr/local/bin/jabali", "cron", "http-trigger", "https://own.example.com/wp-cron.php?doing_wp_cron"},
	}
	content := buildCronServiceContent("job1", "Trigger", cmd, "testuser", nil)

	if !contains(content, "ExecStart='/usr/local/bin/jabali' 'cron' 'http-trigger' 'https://own.example.com/wp-cron.php?doing_wp_cron'") {
		t.Errorf("ExecStart not the wrapper invocation:\n%s", content)
	}
	// No docroot in Argv -> no precheck ExecStartPre.
	if contains(content, "cron-precheck") {
		t.Errorf("http-trigger unit must not have a docroot ExecStartPre:\n%s", content)
	}
}

// GH #403: a cron whose command targets a docroot gates the unit on
// ConditionPathIsDirectory (systemd-native, no exec) instead of the former
// cron-precheck bash ExecStartPre that tripped the M33 suspicious-exec burst.
func TestBuildCronServiceContent_DocrootCondition(t *testing.T) {
	dr := "/home/alice/domains/a.example.com/public_html"
	cmd := &cronvalidate.Command{Argv: []string{"php", dr + "/wp-cron.php"}}
	content := buildCronServiceContent("job9", "WP cron", cmd, "alice", []string{dr})

	if !contains(content, "ConditionPathIsDirectory="+dr) {
		t.Errorf("expected ConditionPathIsDirectory for the docroot:\n%s", content)
	}
	if contains(content, "cron-precheck") || contains(content, "ExecStartPre") {
		t.Errorf("must NOT spawn a cron-precheck shell anymore:\n%s", content)
	}
	// Condition belongs in [Unit], before [Service].
	if indexOfStr(content, "ConditionPathIsDirectory") > indexOfStr(content, "[Service]") {
		t.Errorf("ConditionPathIsDirectory must be in [Unit], before [Service]")
	}
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
