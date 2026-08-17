package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// setupSSHTestPaths points the agent at temp files for both drop-ins and
// disables the sshd -t / systemctl reload exec calls so unit tests don't
// require root or sshd on the box.
func setupSSHTestPaths(t *testing.T) (globalPath, sftpPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	globalPath = filepath.Join(tmpDir, "jabali-sshd.conf")
	sftpPath = filepath.Join(tmpDir, "jabali-sftp.conf")
	t.Setenv("JABALI_SSHD_DROPIN_PATH", globalPath)
	t.Setenv("JABALI_SSHD_SFTP_DROPIN_PATH", sftpPath)
	t.Setenv("JABALI_SSHD_TEST_SKIP_VALIDATE", "1")
	t.Setenv("JABALI_SSHD_TEST_SKIP_RELOAD", "1")
	return globalPath, sftpPath
}

func TestSystemSetSSHConfig_ValidPort(t *testing.T) {
	globalPath, sftpPath := setupSSHTestPaths(t)

	ctx := context.Background()
	params := json.RawMessage(`{"port":2222,"password_auth":false,"user_password_auth":false}`)
	resp, err := systemSetSSHConfigHandler(ctx, params)
	if err != nil {
		t.Fatalf("systemSetSSHConfigHandler failed: %v", err)
	}

	result, ok := resp.(systemSetSSHConfigResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.Port != 2222 {
		t.Fatalf("expected Port=2222, got %d", result.Port)
	}
	if result.PasswordAuth != false {
		t.Fatalf("expected PasswordAuth=false, got %v", result.PasswordAuth)
	}
	if result.UserPasswordAuth != false {
		t.Fatalf("expected UserPasswordAuth=false, got %v", result.UserPasswordAuth)
	}

	gotGlobal, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("failed to read global config: %v", err)
	}
	expected := "Port 2222\nPasswordAuthentication no\n"
	if string(gotGlobal) != expected {
		t.Fatalf("global config mismatch:\nexpected:\n%s\ngot:\n%s", expected, gotGlobal)
	}

	gotSftp, err := os.ReadFile(sftpPath)
	if err != nil {
		t.Fatalf("failed to read sftp config: %v", err)
	}
	if !strings.Contains(string(gotSftp), "PasswordAuthentication no") {
		t.Fatalf("sftp config should pin PasswordAuthentication no when user_password_auth=false; got:\n%s", gotSftp)
	}
	if !strings.Contains(string(gotSftp), "ForceCommand internal-sftp") {
		t.Fatalf("sftp config should preserve ForceCommand internal-sftp; got:\n%s", gotSftp)
	}
}

func TestSystemSetSSHConfig_PasswordAuthEnabled(t *testing.T) {
	globalPath, _ := setupSSHTestPaths(t)

	ctx := context.Background()
	params := json.RawMessage(`{"port":22,"password_auth":true,"user_password_auth":false}`)
	resp, err := systemSetSSHConfigHandler(ctx, params)
	if err != nil {
		t.Fatalf("systemSetSSHConfigHandler failed: %v", err)
	}

	result, ok := resp.(systemSetSSHConfigResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.PasswordAuth != true {
		t.Fatalf("expected PasswordAuth=true, got %v", result.PasswordAuth)
	}

	got, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("failed to read global config: %v", err)
	}
	expected := "Port 22\nPasswordAuthentication yes\n"
	if string(got) != expected {
		t.Fatalf("global config mismatch:\nexpected:\n%s\ngot:\n%s", expected, got)
	}
}

// TestSystemSetSSHConfig_UserPasswordAuthEnabled — the M12 jabali-sftp Match
// block must drop the explicit `PasswordAuthentication no` so the global
// `yes` (or sshd's default) takes effect for hosting users. ForceCommand
// stays — hosting users land in SFTP, not a shell.
func TestSystemSetSSHConfig_UserPasswordAuthEnabled(t *testing.T) {
	_, sftpPath := setupSSHTestPaths(t)

	ctx := context.Background()
	params := json.RawMessage(`{"port":22,"password_auth":true,"user_password_auth":true}`)
	resp, err := systemSetSSHConfigHandler(ctx, params)
	if err != nil {
		t.Fatalf("systemSetSSHConfigHandler failed: %v", err)
	}
	result := resp.(systemSetSSHConfigResponse)
	if !result.UserPasswordAuth {
		t.Fatalf("expected UserPasswordAuth=true, got %v", result.UserPasswordAuth)
	}

	got, err := os.ReadFile(sftpPath)
	if err != nil {
		t.Fatalf("failed to read sftp config: %v", err)
	}
	gotStr := string(got)
	if !strings.Contains(gotStr, "PasswordAuthentication yes") {
		t.Fatalf("sftp config should set PasswordAuthentication yes; got:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "KbdInteractiveAuthentication yes") {
		t.Fatalf("sftp config should set KbdInteractiveAuthentication yes; got:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "ForceCommand internal-sftp") {
		t.Fatalf("sftp config must still pin ForceCommand internal-sftp (no-shell guarantee); got:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "Match Group jabali-sftp") {
		t.Fatalf("sftp config must keep the Match Group jabali-sftp directive; got:\n%s", gotStr)
	}
}

func TestSystemSetSSHConfig_PortTooLow(t *testing.T) {
	setupSSHTestPaths(t)

	ctx := context.Background()
	params := json.RawMessage(`{"port":0,"password_auth":false,"user_password_auth":false}`)
	_, err := systemSetSSHConfigHandler(ctx, params)
	if err == nil {
		t.Fatal("expected error for port 0")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestSystemSetSSHConfig_PortTooHigh(t *testing.T) {
	setupSSHTestPaths(t)

	ctx := context.Background()
	params := json.RawMessage(`{"port":65536,"password_auth":false,"user_password_auth":false}`)
	_, err := systemSetSSHConfigHandler(ctx, params)
	if err == nil {
		t.Fatal("expected error for port 65536")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestSystemSetSSHConfig_InvalidJSON(t *testing.T) {
	setupSSHTestPaths(t)

	ctx := context.Background()
	params := json.RawMessage(`{not valid json}`)
	_, err := systemSetSSHConfigHandler(ctx, params)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestSystemSetSSHConfig_AtomicWrite(t *testing.T) {
	globalPath, sftpPath := setupSSHTestPaths(t)

	// Pre-populate both targets so we can confirm the atomic rename swapped them.
	for _, p := range []string{globalPath, sftpPath} {
		if err := os.WriteFile(p, []byte("# old\n"), 0600); err != nil {
			t.Fatalf("failed to write initial %s: %v", p, err)
		}
	}

	ctx := context.Background()
	params := json.RawMessage(`{"port":2222,"password_auth":true,"user_password_auth":false}`)
	if _, err := systemSetSSHConfigHandler(ctx, params); err != nil {
		t.Fatalf("systemSetSSHConfigHandler failed: %v", err)
	}

	for _, p := range []string{globalPath, sftpPath} {
		if _, err := os.Stat(p + ".new"); !os.IsNotExist(err) {
			t.Fatalf("expected %s.new to be removed after rename, stat err=%v", p, err)
		}
	}
	got, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("failed to read global config: %v", err)
	}
	expected := "Port 2222\nPasswordAuthentication yes\n"
	if string(got) != expected {
		t.Fatalf("global config mismatch:\nexpected:\n%s\ngot:\n%s", expected, got)
	}
}

// TestSystemSetSSHConfig_RestoresBothOnValidationFailure — when sshd -t
// rejects the new config, BOTH drop-ins must be restored to their previous
// contents so we don't leave the box with one updated and one stale file.
func TestSystemSetSSHConfig_RestoresBothOnValidationFailure(t *testing.T) {
	withRealExec(t) // GH #994: needs the real read-only command (stub returns empty output)
	globalPath, sftpPath := setupSSHTestPaths(t)
	t.Setenv("JABALI_SSHD_TEST_SKIP_VALIDATE", "") // re-enable the exec
	t.Setenv("PATH", t.TempDir())                  // sshd not on PATH → exec fails

	prevGlobal := []byte("# previous global\n")
	prevSftp := []byte("# previous sftp\n")
	if err := os.WriteFile(globalPath, prevGlobal, 0600); err != nil {
		t.Fatalf("write prev global: %v", err)
	}
	if err := os.WriteFile(sftpPath, prevSftp, 0600); err != nil {
		t.Fatalf("write prev sftp: %v", err)
	}

	ctx := context.Background()
	params := json.RawMessage(`{"port":22,"password_auth":true,"user_password_auth":true}`)
	if _, err := systemSetSSHConfigHandler(ctx, params); err == nil {
		t.Fatal("expected validation to fail when sshd not on PATH")
	}

	gotGlobal, _ := os.ReadFile(globalPath)
	if string(gotGlobal) != string(prevGlobal) {
		t.Fatalf("global drop-in not restored; got:\n%s", gotGlobal)
	}
	gotSftp, _ := os.ReadFile(sftpPath)
	if string(gotSftp) != string(prevSftp) {
		t.Fatalf("sftp drop-in not restored; got:\n%s", gotSftp)
	}
}

func TestSystemSetSSHConfig_Registration(t *testing.T) {
	commands := Default.Commands()
	found := false
	for _, cmd := range commands {
		if cmd == "system.set_ssh_config" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("system.set_ssh_config not registered in Default registry")
	}
}

func TestSystemSetSSHConfig_PortBoundary(t *testing.T) {
	setupSSHTestPaths(t)

	tests := []struct {
		port  uint16
		valid bool
	}{
		{1, true},
		{65535, true},
		{22, true},
		{443, true},
	}

	for _, tt := range tests {
		t.Run(string(rune(tt.port)), func(t *testing.T) {
			ctx := context.Background()
			type paramStruct struct {
				Port             uint16 `json:"port"`
				PasswordAuth     bool   `json:"password_auth"`
				UserPasswordAuth bool   `json:"user_password_auth"`
			}
			paramJSON, _ := json.Marshal(paramStruct{Port: tt.port})
			resp, err := systemSetSSHConfigHandler(ctx, json.RawMessage(paramJSON))
			if !tt.valid && err == nil {
				t.Fatalf("expected error for port %d", tt.port)
			}
			if tt.valid && err != nil {
				t.Fatalf("unexpected error for port %d: %v", tt.port, err)
			}
			if tt.valid {
				result := resp.(systemSetSSHConfigResponse)
				if result.Port != tt.port {
					t.Fatalf("expected port %d, got %d", tt.port, result.Port)
				}
			}
		})
	}
}
