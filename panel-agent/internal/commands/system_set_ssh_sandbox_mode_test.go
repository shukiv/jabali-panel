package commands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The opt-in flag the SSH wrapper reads (ADR-0177): "on" when the admin
// turns the log socket on, gone when they turn it off. The wrapper counts
// only a regular file holding "on" (jabali-ssh-shell sandboxSyslogOn).
func TestApplySSHSandboxSyslog(t *testing.T) {
	old := sshSandboxSyslogFile
	t.Cleanup(func() { sshSandboxSyslogFile = old })
	sshSandboxSyslogFile = filepath.Join(t.TempDir(), "ssh-sandbox-syslog")

	if err := applySSHSandboxSyslog(true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sshSandboxSyslogFile)
	if err != nil || string(raw) != "on\n" {
		t.Fatalf("flag = %q, %v; want \"on\\n\"", raw, err)
	}
	if fi, _ := os.Stat(sshSandboxSyslogFile); fi.Mode().Perm() != 0o644 {
		t.Errorf("flag mode = %v, want 0644 (the wrapper runs as the tenant)", fi.Mode().Perm())
	}

	if err := applySSHSandboxSyslog(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sshSandboxSyslogFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("turned off, yet the flag is still there: %v", err)
	}
	// Off when already off is not an error.
	if err := applySSHSandboxSyslog(false); err != nil {
		t.Errorf("off twice: %v", err)
	}
}
