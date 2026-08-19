package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// filesListParams is the input shape for files.list.
type filesListParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM: root scope + deny-list
	Path      string `json:"path"`
}

// filesListEntry represents a single file/directory entry.
// HasSubdirs is set for dir entries only; it lets the tree UI hide
// the expand chevron on folders with no subfolders. Computed via one
// ReadDir per directory — OS dcache keeps the cost negligible.
type filesListEntry struct {
	Name       string `json:"name"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	Mode       string `json:"mode"`
	ModTime    string `json:"mod_time"`
	IsSymlink  bool   `json:"is_symlink"`
	HasSubdirs bool   `json:"has_subdirs,omitempty"`
}

// filesListResponse is the output shape for files.list.
type filesListResponse struct {
	Path    string           `json:"path"`
	Entries []filesListEntry `json:"entries"`
}

func filesListHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesListParams
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

	// String-gate the path; the listing is escape-proof — the directory is
	// opened under openat2 RESOLVE_BENEATH and every entry is fstatat'd against
	// that fd with AT_SYMLINK_NOFOLLOW, so neither the dir nor any entry can be
	// redirected by a planted symlink (Gitea #428).
	cleanPath, err := scope.Clean(p.Path)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("path validation failed: %v", err),
		}
	}

	entries, err := scope.ReadDirInScope(cleanPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to list directory: %v", err),
		}
	}

	result := []filesListEntry{}
	for _, entry := range entries {
		// Peek into each real directory for at least one subdir so the tree UI
		// can hide the expand chevron on leaves. Cosmetic hint only.
		hasSubdirs := false
		if entry.IsDir && !entry.IsSymlink {
			hasSubdirs = dirHasSubdir(scope, filepath.Join(cleanPath, entry.Name))
		}
		result = append(result, filesListEntry{
			Name:       entry.Name,
			IsDir:      entry.IsDir,
			Size:       entry.Size,
			Mode:       entry.Mode.String(),
			ModTime:    entry.ModTime.String(),
			IsSymlink:  entry.IsSymlink,
			HasSubdirs: hasSubdirs,
		})
	}

	return &filesListResponse{
		Path:    cleanPath,
		Entries: result,
	}, nil
}

// dirHasSubdir returns true if dir contains at least one subdirectory
// (excluding symlinks). Scans via ReadDir and short-circuits on the
// first hit — we don't care about the count, just existence.
// dirHasSubdir reports whether dir contains at least one real subdirectory,
// using the fd-safe scope traversal (GH #651) — os.ReadDir/os.Lstat on a
// tenant-controlled string path could be raced with a symlink swap into an
// out-of-scope directory.
func dirHasSubdir(scope *filesafe.Scope, dir string) bool {
	entries, err := scope.ReadDirInScope(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir && !e.IsSymlink {
			return true
		}
	}
	return false
}

func init() {
	Default.Register("files.list", filesListHandler)
}
