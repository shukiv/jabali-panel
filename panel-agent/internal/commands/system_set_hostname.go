package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemSetHostnameParams is the input shape for system.set_hostname.
type systemSetHostnameParams struct {
	Hostname string `json:"hostname"`
}

// systemSetHostnameResponse is the output shape. Returns the hostname that
// was actually applied so the panel can confirm the round-trip succeeded.
type systemSetHostnameResponse struct {
	Hostname string `json:"hostname"`
}

// hostnameAllowedRE matches a standard DNS hostname. Intentionally loose —
// the panel API enforces stricter validation before we ever get here; this
// is a belt-and-braces check to block shell metacharacters from reaching
// hostnamectl.
var hostnameAllowedRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// systemSetHostnameHandler applies a new hostname to the OS via hostnamectl
// and appends it to /etc/hosts if not already present. /etc/hosts IP
// columns are left alone — that's operator territory.
func systemSetHostnameHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p systemSetHostnameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	p.Hostname = strings.TrimSpace(p.Hostname)
	if p.Hostname == "" || len(p.Hostname) > 253 || !hostnameAllowedRE.MatchString(p.Hostname) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid hostname",
		}
	}

	// hostnamectl handles both systemd-hostnamed and the /etc/hostname
	// write. Fall through with a clear error on distros that lack it.
	cmd := execCommandContext(ctx, "hostnamectl", "set-hostname", p.Hostname)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("hostnamectl failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Append to /etc/hosts if the hostname doesn't already appear there.
	// We use 127.0.1.1 (Debian convention) rather than touching the public
	// IP line, which ops might be managing themselves.
	hostsPath := "/etc/hosts"
	existing, err := os.ReadFile(hostsPath)
	if err != nil {
		return nil, fmt.Errorf("read /etc/hosts: %w", err)
	}
	if !strings.Contains(string(existing), p.Hostname) {
		line := fmt.Sprintf("127.0.1.1\t%s\n", p.Hostname)
		f, err := os.OpenFile(hostsPath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return nil, fmt.Errorf("open /etc/hosts: %w", err)
		}
		if _, err := f.WriteString(line); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("append /etc/hosts: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close /etc/hosts: %w", err)
		}
	}

	return systemSetHostnameResponse{Hostname: p.Hostname}, nil
}

func init() {
	Default.Register("system.set_hostname", systemSetHostnameHandler)
}
