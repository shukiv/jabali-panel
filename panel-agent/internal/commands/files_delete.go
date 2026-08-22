package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// filesDeleteParams is the input shape for files.delete.
type filesDeleteParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM: root scope + deny-list
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

// filesDeleteResponse is the output shape for files.delete.
type filesDeleteResponse struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted"`
}

func filesDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesDeleteParams
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
	if p.Path == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "path required",
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

	// String-gate the path; the unlink act is escape-proof (unlinkat / fd-descent
	// against openat2 parent fds), so os.RemoveAll's string re-resolution can't
	// be redirected through a swapped parent symlink (Gitea #428).
	// JAB-358: confine deletes to the admin write allow-list (no-op for tenants).
	scope = scope.WriteScope()
	cleanPath, err := scope.Clean(p.Path)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("path validation failed: %v", err),
		}
	}

	// Prevent deletion of docroot roots themselves
	for _, docroot := range scope.OwnedDocroots {
		if cleanPath == docroot {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: "cannot delete docroot directory",
			}
		}
	}

	// Delete file or directory, escape-proof.
	var deleteErr error
	if p.Recursive {
		deleteErr = scope.RemoveAllInScope(ctx, cleanPath)
	} else {
		// Non-recursive: AT_REMOVEDIR needs to know if the leaf is a dir.
		info, serr := scope.ExistsInScope(cleanPath)
		if serr != nil {
			deleteErr = serr
		} else if info != nil {
			deleteErr = scope.RemoveInScope(cleanPath, info.IsDir)
		}
	}

	if deleteErr != nil {
		// If path doesn't exist, return success (idempotent)
		if !os.IsNotExist(deleteErr) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to delete: %v", deleteErr),
			}
		}
	}

	return &filesDeleteResponse{
		Path:    cleanPath,
		Deleted: true,
	}, nil
}

func init() {
	Default.Register("files.delete", filesDeleteHandler)
}
