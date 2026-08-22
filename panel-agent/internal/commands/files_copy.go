package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// files.copy — recursively copy a scoped path to a new location, preserving
// mode bits and reproducing symlinks as symlinks (their target string is data,
// not chased). Cross-directory by design: used by the Copy / Paste flow.
//
// Security (Gitea #422): both src and dst are resolved escape-proof and the
// copy descends via O_NOFOLLOW directory fds on both sides (CopyTreeInScope),
// so a tenant-planted PARENT symlink — or a symlink swapped into the
// partially-built copy mid-walk — can never redirect a root-side write out of
// the docroot.

type filesCopyParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM
	SrcPath   string `json:"src_path"`
	DstPath   string `json:"dst_path"`
}

type filesCopyResponse struct {
	SrcPath string `json:"src_path"`
	DstPath string `json:"dst_path"`
	Bytes   int64  `json:"bytes"`
}

func filesCopyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesCopyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if p.Username == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username required"}
	}
	if p.SrcPath == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "src_path required"}
	}
	if p.DstPath == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "dst_path required"}
	}

	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to create scope: %v", err),
		}
	}
	// JAB-358: copy reads the source and writes the destination through a SINGLE
	// escape-proof scope (CopyTreeInScope), so confine BOTH ends to the admin
	// write allow-list. An admin copies within the tenant/app data roots; system
	// files stay view-only (no-op for tenant scopes).
	scope = scope.WriteScope()
	srcClean, err := scope.Clean(p.SrcPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("src_path validation failed: %v", err),
		}
	}
	dstClean, err := scope.Clean(p.DstPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("dst_path validation failed: %v", err),
		}
	}
	if srcClean == dstClean {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "source and destination are the same",
		}
	}
	// Refuse copy of a dir into its own descendant — same shape as move.
	if isDescendant(srcClean, dstClean) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "cannot copy into a subdirectory of itself",
		}
	}
	// dst must not already exist — checked through the escape-proof parent fd.
	if existing, err := scope.ExistsInScope(dstClean); err != nil {
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

	uid, gid := hostingIDs(p.Username)
	bytes, err := scope.CopyTreeInScope(ctx, srcClean, dstClean, uid, gid)
	if err != nil {
		// Attempt rollback — copy may have left a partial tree behind.
		_ = scope.RemoveAllInScope(context.Background(), dstClean)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("copy: %v", err),
		}
	}
	return &filesCopyResponse{
		SrcPath: srcClean,
		DstPath: dstClean,
		Bytes:   bytes,
	}, nil
}

func init() {
	Default.Register("files.copy", filesCopyHandler)
}
