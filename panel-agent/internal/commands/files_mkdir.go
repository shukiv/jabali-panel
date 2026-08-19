package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"strconv"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"os"
)

// filesMkdirParams is the input shape for files.mkdir.
type filesMkdirParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM: root scope + deny-list
	Path      string `json:"path"`
	Mode      string `json:"mode,omitempty"` // "parents" or empty (default no parents)
}

// filesMkdirResponse is the output shape for files.mkdir.
type filesMkdirResponse struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

func filesMkdirHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesMkdirParams
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

	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to create scope: %v", err),
		}
	}

	// String-gate the path; the directory is created escape-proof (mkdirat /
	// openat(O_NOFOLLOW) descent against the base fd), so a parent-symlink swap
	// can't redirect a root-side mkdir (Gitea #424 / #428).
	cleanPath, err := scope.Clean(p.Path)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("path validation failed: %v", err),
		}
	}

	created, err := scope.MkdirInScope(cleanPath, p.Mode == "parents", 0700)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to create directory: %v", err),
		}
	}

	if created {
		// Ownership for the new directory:
		//   - Tenant: <user>:www-data, 0750 (nginx traverse + static read).
		//   - Admin FM (GH #1184): root:root 0755 — a root-tree dir must never
		//     be reassigned to a tenant.
		uid, wwwDataGid := 0, 0
		dirMode := os.FileMode(0750)
		if p.AdminRoot {
			dirMode = 0755
		} else {
			u, uerr := user.Lookup(p.Username)
			if uerr != nil {
				return nil, &agentwire.AgentError{
					Code:    agentwire.CodeInvalidArgument,
					Message: fmt.Sprintf("failed to lookup user %q: %v", p.Username, uerr),
				}
			}
			uid, _ = strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			wwwDataGid = gid
			if g, gerr := user.LookupGroup("www-data"); gerr == nil {
				wwwDataGid, _ = strconv.Atoi(g.Gid)
			}
		}

		df, derr := scope.OpenDirInScope(cleanPath)
		if derr != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to open new directory: %v", derr),
			}
		}
		if err := df.Chown(uid, wwwDataGid); err != nil {
			df.Close()
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to chown directory: %v", err),
			}
		}
		// Tenant 0750 (nginx traverse) / admin 0755.
		if err := df.Chmod(dirMode); err != nil {
			df.Close()
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to chmod directory: %v", err),
			}
		}
		df.Close()
	}

	return &filesMkdirResponse{
		Path:    cleanPath,
		Created: created,
	}, nil
}

func init() {
	Default.Register("files.mkdir", filesMkdirHandler)
}
