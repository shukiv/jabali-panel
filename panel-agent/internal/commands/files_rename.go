package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// filesRenameParams is the input shape for files.rename.
type filesRenameParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM: root scope + deny-list
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
}

// filesRenameResponse is the output shape for files.rename.
type filesRenameResponse struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Renamed bool   `json:"renamed"`
}

func filesRenameHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesRenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate inputs
	if p.Username == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "username required",
		}
	}
	if p.OldPath == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "old_path required",
		}
	}
	if p.NewPath == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "new_path required",
		}
	}

	// Create filesafe scope with user's home directory
	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to create scope: %v", err),
		}
	}

	// String-gate both paths; the rename act is escape-proof (renameat between
	// openat2 parent fds), closing the TOCTOU parent-swap (Gitea #428).
	// JAB-358: rename mutates BOTH ends — confine them to the admin write
	// allow-list (no-op for tenants).
	scope = scope.WriteScope()
	oldClean, err := scope.Clean(p.OldPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("old_path validation failed: %v", err),
		}
	}

	newClean, err := scope.Clean(p.NewPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("new_path validation failed: %v", err),
		}
	}

	// Validate that both paths share the same parent directory
	if filepath.Dir(oldClean) != filepath.Dir(newClean) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "rename across directories not allowed",
		}
	}

	// Prevent overwriting existing target — checked through the escape-proof
	// parent fd, not an os.Lstat(string) that re-resolves a swapped parent.
	if existing, err := scope.ExistsInScope(newClean); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to stat target: %v", err),
		}
	} else if existing != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "target path already exists",
		}
	}

	// Rename file
	if err := scope.RenameInScope(oldClean, newClean); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to rename: %v", err),
		}
	}

	return &filesRenameResponse{
		OldPath: oldClean,
		NewPath: newClean,
		Renamed: true,
	}, nil
}

func init() {
	Default.Register("files.rename", filesRenameHandler)
}
