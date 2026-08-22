package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// files.chmod — change permission bits on a file or directory within
// the user's scope. Ownership is NOT changed: everything in the user's
// homedir is already owned by <user>:www-data (that's the M9.5 contract),
// and exposing chown would mean exposing root-equivalent capability here.
//
// Mode is a Unix-style octal string ("0644", "755", "0o755" all accepted).
// Only the low 12 bits are honoured — setuid/setgid/sticky allowed so the
// user can tidy up legacy uploads, but higher bits are masked out.

type filesChmodParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM: root scope + deny-list
	Path      string `json:"path"`
	Mode      string `json:"mode"`
}

type filesChmodResponse struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

func filesChmodHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesChmodParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if p.Username == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username required"}
	}
	if p.Path == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "path required"}
	}
	if p.Mode == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "mode required"}
	}

	mode, err := parseChmodMode(p.Mode)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid mode: %v", err),
		}
	}

	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to create scope: %v", err),
		}
	}
	// JAB-358: confine chmod to the admin write allow-list (no-op for tenants).
	scope = scope.WriteScope()
	cleanPath, err := scope.Clean(p.Path)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("path validation failed: %v", err),
		}
	}

	// GH #656: never let chmod widen a tenant secret file (wp-config.php/.env)
	// back to group/world-readable — clamp to the owner-only sensitive mode,
	// matching the write/upload hardening.
	mode = sensitiveFileMode(cleanPath, mode)

	// ChmodInScope opens the leaf O_NOFOLLOW against its escape-proof parent fd
	// and Fchmods that fd — a symlink leaf is refused (never chased) and no
	// parent-symlink swap can redirect the chmod (Gitea #428).
	if err := scope.ChmodInScope(cleanPath, mode); err != nil {
		if ve, ok := err.(*filesafe.ValidationError); ok && ve.Code == filesafe.ErrCodeSymlinkEscape {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: "cannot chmod a symlink",
			}
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("chmod: %v", err),
		}
	}

	return &filesChmodResponse{
		Path: cleanPath,
		Mode: fmt.Sprintf("%04o", mode),
	}, nil
}

// parseChmodMode accepts "0644", "644", "0o644" and returns an os.FileMode
// masked to the low 12 bits, with the setuid and setgid bits FORCED OFF
// (Gitea #429): a tenant must never be able to mark a file in their homedir
// setuid/setgid via the file manager. The sticky bit is preserved (harmless,
// and useful on shared upload dirs). Any higher-bit characters (file-type
// bits like 0100000) are rejected rather than silently stripped.
func parseChmodMode(s string) (os.FileMode, error) {
	// Accept "0o" prefix for the TOML/Go-style writer; otherwise bare octal.
	trimmed := s
	if len(trimmed) > 2 && trimmed[0] == '0' && (trimmed[1] == 'o' || trimmed[1] == 'O') {
		trimmed = trimmed[2:]
	}
	n, err := strconv.ParseUint(trimmed, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("not an octal number: %q", s)
	}
	if n > 0o7777 {
		return 0, fmt.Errorf("mode out of range (max 0o7777): %q", s)
	}
	// Gitea #429: force setuid (0o4000) and setgid (0o2000) off — never settable
	// through the file manager.
	n &^= 0o6000
	return os.FileMode(n), nil
}

func init() {
	Default.Register("files.chmod", filesChmodHandler)
}
