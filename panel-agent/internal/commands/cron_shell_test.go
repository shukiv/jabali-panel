package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

// GH #2290: the agent re-checks a tenant job's command with the panel's
// allow_shell_scripts (the account's package includes SSH). A root job keeps
// the closed list whatever the flag says.

func shellApply(t *testing.T, username, command string, allow, root bool) error {
	t.Helper()
	params, _ := json.Marshal(map[string]any{
		"user_id": "1", "username": username, "job_id": "01JABCDEFGHJKMNPQRSTVWXYZ0",
		"name": "Backup", "command": command, "schedule": "0 * * * *",
		"owned_docroots": []string{}, "allow_shell_scripts": allow, "run_as_root": root,
	})
	_, err := cronApplyHandler(context.Background(), params)
	return err
}

func shellErrCode(err error) string {
	if ae, ok := err.(*agentwire.AgentError); ok {
		return ae.Code
	}
	return ""
}

func TestCronApply_ShellScriptNeedsTheFlag(t *testing.T) {
	err := shellApply(t, "nonexistentuser12345", "bash /home/nonexistentuser12345/bin/x.sh", false, false)
	if shellErrCode(err) != agentwire.CodeInvalidArgument || !strings.Contains(err.Error(), cronvalidate.ErrCodeShellNeedsSSH) {
		t.Fatalf("without allow_shell_scripts: got %v, want invalid argument %s", err, cronvalidate.ErrCodeShellNeedsSSH)
	}
	// With it the command passes, and the unknown account stops it after.
	err = shellApply(t, "nonexistentuser12345", "bash /home/nonexistentuser12345/bin/x.sh", true, false)
	if shellErrCode(err) == agentwire.CodeInvalidArgument {
		t.Fatalf("with allow_shell_scripts the command was refused: %v", err)
	}
}

func TestCronApply_RootJobNeverRunsAShellScript(t *testing.T) {
	err := shellApply(t, "root", "bash /root/bin/x.sh", true, true)
	if shellErrCode(err) != agentwire.CodeInvalidArgument || !strings.Contains(err.Error(), cronvalidate.ErrCodeShellNeedsSSH) {
		t.Fatalf("root job with allow_shell_scripts: got %v, want invalid argument %s", err, cronvalidate.ErrCodeShellNeedsSSH)
	}
}

// A tenant's shell job runs in its SSH sandbox, like its other interpreters.
func TestBuildCronServiceContent_ShellScriptIsSandboxed(t *testing.T) {
	cmds, err := cronvalidate.ValidateAnyMultiPolicy("/home/alice/bin/backup.sh nightly", nil, nil, "/home/alice", cronvalidate.Policy{ShellScripts: true})
	if err != nil {
		t.Fatal(err)
	}
	content := buildCronServiceContent("job1", "backup", cmds, "alice", nil)
	want := "ExecStart=" + cronSandboxWrapper + " --exec '/bin/bash' '/home/alice/bin/backup.sh' 'nightly'"
	if !strings.Contains(content, want) {
		t.Errorf("unit lacks %q:\n%s", want, content)
	}
}

func TestCronRunNow_ShellScriptNeedsTheFlag(t *testing.T) {
	runNow := func(allow bool) error {
		params, _ := json.Marshal(map[string]any{
			"user_id": "1", "username": "nonexistentuser12345", "job_id": "01JABCDEFGHJKMNPQRSTVWXYZ0",
			"command": "bash /home/nonexistentuser12345/bin/x.sh", "owned_docroots": []string{},
			"allow_shell_scripts": allow,
		})
		_, err := cronRunNowHandler(context.Background(), params)
		return err
	}
	if err := runNow(false); shellErrCode(err) != agentwire.CodeInvalidArgument || !strings.Contains(err.Error(), cronvalidate.ErrCodeShellNeedsSSH) {
		t.Fatalf("run now without allow_shell_scripts: got %v, want invalid argument %s", err, cronvalidate.ErrCodeShellNeedsSSH)
	}
	// With it the command passes, and the unknown account stops it after.
	if err := runNow(true); shellErrCode(err) == agentwire.CodeInvalidArgument {
		t.Fatalf("with allow_shell_scripts the command was refused: %v", err)
	}
}
