package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// phpPoolRemoveParams is the input shape for php.pool.remove.
type phpPoolRemoveParams struct {
	Username string `json:"username"`
	// Slug selects the pool/instance to remove (GH #329). Empty => the legacy
	// per-user default pool (slug == username). A non-empty slug reaps a single
	// versioned master (e.g. "alice-php8.2") and leaves the default pool and
	// the user's shell CLI php untouched.
	Slug string `json:"slug,omitempty"`
}

// phpPoolRemoveResponse is the output shape for php.pool.remove.
type phpPoolRemoveResponse struct {
	Removed int `json:"removed"`
}

func phpPoolRemoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p phpPoolRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate username format.
	if !phpPoolUsernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid username format",
		}
	}

	// Resolve the slug to remove (default pool == username).
	slug := p.Username
	if p.Slug != "" {
		slug = p.Slug
	}
	if !phpPoolSlugRegex.MatchString(slug) || strings.Contains(slug, "..") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid slug format",
		}
	}
	isVersioned := slug != p.Username

	// Glob delete pool files for this slug across all versions.
	pattern := fmt.Sprintf("/etc/php/*/fpm/pool.d/jabali-%s.conf", slug)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("glob failed: %v", err),
		}
	}

	// Remove pool files.
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to remove %s: %v", path, err),
			}
		}
	}

	// Remove per-user FPM config.
	fpmConfRoot := os.Getenv("JABALI_FPM_CONFIG_ROOT")
	if fpmConfRoot == "" {
		fpmConfRoot = "/etc/jabali-panel/fpm"
	}
	fpmConfPath := filepath.Join(fpmConfRoot, slug+".conf")
	if err := os.Remove(fpmConfPath); err != nil && !os.IsNotExist(err) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to remove per-user fpm config: %v", err),
		}
	}

	// Remove version pin file.
	verPinRoot := os.Getenv("JABALI_PHP_VER_PIN_ROOT")
	if verPinRoot == "" {
		verPinRoot = "/etc/jabali-panel/user-phpver"
	}
	verPinPath := filepath.Join(verPinRoot, slug)
	if err := os.Remove(verPinPath); err != nil && !os.IsNotExist(err) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to remove version pin: %v", err),
		}
	}

	// Remove the per-user CLI php wrapper (GH #184). Best-effort teardown.
	// The CLI php belongs to the DEFAULT pool only — reaping a versioned pool
	// must never repoint or remove the user's shell php.
	if !isVersioned {
		_ = removeUserCLIPHP(p.Username)
	}

	// Stop + disable the slug's FPM service (if loaded).
	if os.Getenv("JABALI_PHP_POOL_SKIP_RELOAD") == "" {
		serviceName := fmt.Sprintf("jabali-fpm@%s.service", slug)
		_ = execCommandContext(ctx, "systemctl", "stop", serviceName).Run()
		_ = execCommandContext(ctx, "systemctl", "disable", "--quiet", serviceName).Run()
	}

	// A versioned slug owns its own systemd drop-in dir; remove it so a reaped
	// version leaves nothing behind (the default pool's drop-in is owned by
	// user.slice.ensure and must be left intact).
	if isVersioned {
		dropinDir := filepath.Join(systemdRoot(), fmt.Sprintf("jabali-fpm@%s.service.d", slug))
		_ = os.RemoveAll(dropinDir)
		if os.Getenv("JABALI_PHP_POOL_SKIP_RELOAD") == "" {
			_ = execCommandContext(ctx, "systemctl", "daemon-reload").Run()
		}
	}

	return phpPoolRemoveResponse{
		Removed: len(matches),
	}, nil
}

func init() {
	Default.Register("php.pool.remove", phpPoolRemoveHandler)
}
