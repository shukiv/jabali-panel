package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// filesStatParams is the input shape for files.stat.
type filesStatParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM: root scope + deny-list
	Path      string `json:"path"`
}

// filesStatResponse is the output shape for files.stat.
type filesStatResponse struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Mode      string `json:"mode"`
	IsDir     bool   `json:"is_dir"`
	ModTime   string `json:"mod_time"`
	IsSymlink bool   `json:"is_symlink"`
}

func filesStatHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesStatParams
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

	// String-gate the path; the stat is escape-proof (fstatat AT_SYMLINK_NOFOLLOW
	// against the openat2 parent fd), closing the TOCTOU read (Gitea #428).
	cleanPath, err := scope.Clean(p.Path)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("path validation failed: %v", err),
		}
	}

	info, err := scope.StatInScope(cleanPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to stat file: %v", err),
		}
	}

	return &filesStatResponse{
		Path:      cleanPath,
		Size:      info.Size,
		Mode:      info.Mode.String(),
		IsDir:     info.IsDir,
		ModTime:   info.ModTime.String(),
		IsSymlink: info.IsSymlink,
	}, nil
}

func init() {
	Default.Register("files.stat", filesStatHandler)
}
