package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// system.set_ssh_sandbox_mode — atomically write the sandbox mode +
// optional default image to /etc/jabali. Wrapper reads on every connect
// so no service reload is needed.

type systemSetSSHSandboxModeParams struct {
	Mode         string `json:"mode"`                    // "bubblewrap" | "nspawn"
	DefaultImage string `json:"default_image,omitempty"` // optional; only updated if non-empty
	// Syslog puts the system log socket in the sandbox (ADR-0177). Absent
	// (an older panel) leaves the flag as it is.
	Syslog *bool `json:"syslog,omitempty"`
}

type systemSetSSHSandboxModeResponse struct {
	Mode         string `json:"mode"`
	DefaultImage string `json:"default_image,omitempty"`
	Syslog       *bool  `json:"syslog,omitempty"`
}

// sshSandboxSyslogFile is the opt-in flag jabali-ssh-shell reads on every
// connect: "on" binds journald's syslog socket at /dev/log in the jail.
// A var for tests.
var sshSandboxSyslogFile = "/etc/jabali/ssh-sandbox-syslog"

// applySSHSandboxSyslog writes the flag ("on") or removes it.
func applySSHSandboxSyslog(on bool) error {
	if !on {
		if err := os.Remove(sshSandboxSyslogFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", sshSandboxSyslogFile, err)
		}
		return nil
	}
	if err := atomicWrite(sshSandboxSyslogFile, "on\n"); err != nil {
		return err
	}
	// Readable by the tenant the wrapper runs as.
	if err := os.Chmod(sshSandboxSyslogFile, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", sshSandboxSyslogFile, err)
	}
	return nil
}

func systemSetSSHSandboxModeHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p systemSetSSHSandboxModeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	switch p.Mode {
	case "bubblewrap", "nspawn":
	default:
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("mode must be bubblewrap or nspawn, got %q", p.Mode),
		}
	}
	if p.DefaultImage != "" && !nspawnImageRe.MatchString(p.DefaultImage) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "default_image must match [a-z0-9-]+",
		}
	}

	if err := os.MkdirAll("/etc/jabali", 0o755); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir /etc/jabali: %v", err),
		}
	}
	if err := atomicWrite("/etc/jabali/ssh-sandbox-mode", p.Mode+"\n"); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: err.Error(),
		}
	}
	if err := os.Chmod("/etc/jabali/ssh-sandbox-mode", 0o644); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("chmod ssh-sandbox-mode: %v", err),
		}
	}

	if p.DefaultImage != "" {
		if err := atomicWrite("/etc/jabali/default-nspawn-image", p.DefaultImage+"\n"); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: err.Error(),
			}
		}
		if err := os.Chmod("/etc/jabali/default-nspawn-image", 0o644); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("chmod default-nspawn-image: %v", err),
			}
		}
	}

	if p.Syslog != nil {
		if err := applySSHSandboxSyslog(*p.Syslog); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
		}
	}

	return &systemSetSSHSandboxModeResponse{Mode: p.Mode, DefaultImage: p.DefaultImage, Syslog: p.Syslog}, nil
}

func init() {
	Default.Register("system.set_ssh_sandbox_mode", systemSetSSHSandboxModeHandler)
}
