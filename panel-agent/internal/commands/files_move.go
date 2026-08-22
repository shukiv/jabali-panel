package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// files.move — relocate a file or directory to a new path within the
// user's scope. Distinct from files.rename (which is same-parent only):
// move allows different parent directories so the UI can implement
// drag-and-drop of rows into folder rows.
//
// Safety: both source and destination are resolved through the same
// filesafe scope as every other files.* handler, so the user cannot
// move a file out of their homedir or name a destination outside of it.

type filesMoveParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
}

type filesMoveResponse struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Moved   bool   `json:"moved"`
}

func filesMoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesMoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	if p.Username == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username required"}
	}
	if p.OldPath == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "old_path required"}
	}
	if p.NewPath == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "new_path required"}
	}

	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to create scope: %v", err),
		}
	}

	// String-gate both paths; the rename act below is escape-proof (renameat
	// between openat2 parent fds), so a parent-symlink swap can't redirect a
	// root-side move (Gitea #422 / TOCTOU #428).
	// JAB-358: move mutates BOTH ends — confine them to the admin write
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

	// Refuse no-op moves — the caller almost certainly dropped a row
	// back onto itself or onto its own parent. Quiet success is confusing.
	if oldClean == newClean {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "source and destination are the same",
		}
	}

	// Refuse moving a directory into itself (mv foo foo/bar).
	if isDescendant(oldClean, newClean) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "cannot move into a subdirectory of itself",
		}
	}

	// Prevent silent overwrite — check existence through the SAME escape-proof
	// parent fd the rename will use, not an os.Lstat(string) that re-resolves.
	if existing, err := scope.ExistsInScope(newClean); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to stat destination: %v", err),
		}
	} else if existing != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "target path already exists",
		}
	}

	if err := scope.RenameInScope(oldClean, newClean); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to move: %v", err),
		}
	}

	return &filesMoveResponse{
		OldPath: oldClean,
		NewPath: newClean,
		Moved:   true,
	}, nil
}

// isDescendant reports whether descendant is the same path as ancestor
// or lives inside it. Uses filepath.Rel so "/a/b" vs "/a/bar" doesn't
// trigger a false positive (string-prefix check would).
func isDescendant(ancestor, descendant string) bool {
	if ancestor == descendant {
		return true
	}
	rel, err := filepath.Rel(ancestor, descendant)
	if err != nil {
		return false
	}
	// Rel returns "../..." when descendant is NOT under ancestor.
	if rel == "." || rel == "" {
		return true
	}
	if len(rel) >= 2 && rel[0] == '.' && rel[1] == '.' {
		return false
	}
	return true
}

func init() {
	Default.Register("files.move", filesMoveHandler)
}
