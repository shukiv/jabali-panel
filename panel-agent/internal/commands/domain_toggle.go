package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// domainToggleParams is the input shape for domain.enable/domain.disable.
type domainToggleParams struct {
	Domain string `json:"domain"`
}

// domainToggleResponse is the output shape for domain.enable/domain.disable.
type domainToggleResponse struct {
	Domain  string `json:"domain"`
	Enabled bool   `json:"enabled"`
}

func domainEnableHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p domainToggleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate domain format
	if !domainRegex.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid domain %q: must match ^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$", p.Domain),
		}
	}

	// JAB-71: serialize the symlink→test→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	// Create symlink from sites-available to sites-enabled
	availablePath := filepath.Join("/etc/nginx/sites-available", p.Domain+".conf")
	enabledPath := filepath.Join("/etc/nginx/sites-enabled", p.Domain+".conf")

	// Remove existing symlink if present
	os.Remove(enabledPath)

	if err := os.Symlink(availablePath, enabledPath); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("symlink failed: %v", err),
		}
	}

	// Test nginx configuration
	testCmd := execCommandContext(ctx, "nginx", "-t")
	var testOutput bytes.Buffer
	testCmd.Stdout = &testOutput
	testCmd.Stderr = &testOutput
	if err := testCmd.Run(); err != nil {
		// Remove symlink on test failure
		os.Remove(enabledPath)
		return nil, nginxTestFailure("domain.enable", testOutput.String())
	}

	// Reload nginx
	reloadCmd := execCommandContext(ctx, "systemctl", "reload", "nginx")
	var reloadOutput bytes.Buffer
	reloadCmd.Stdout = &reloadOutput
	reloadCmd.Stderr = &reloadOutput
	if err := reloadCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl reload nginx failed: %s", reloadOutput.String()),
		}
	}

	return domainToggleResponse{
		Domain:  p.Domain,
		Enabled: true,
	}, nil
}

func domainDisableHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p domainToggleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate domain format
	if !domainRegex.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid domain %q: must match ^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$", p.Domain),
		}
	}

	// JAB-71: serialize the unlink→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	// Remove symlink from sites-enabled (keep sites-available)
	enabledPath := filepath.Join("/etc/nginx/sites-enabled", p.Domain+".conf")
	os.Remove(enabledPath)

	// Reload nginx
	reloadCmd := execCommandContext(ctx, "systemctl", "reload", "nginx")
	var reloadOutput bytes.Buffer
	reloadCmd.Stdout = &reloadOutput
	reloadCmd.Stderr = &reloadOutput
	if err := reloadCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl reload nginx failed: %s", reloadOutput.String()),
		}
	}

	return domainToggleResponse{
		Domain:  p.Domain,
		Enabled: false,
	}, nil
}

func init() {
	Default.Register("domain.enable", domainEnableHandler)
	Default.Register("domain.disable", domainDisableHandler)
}
