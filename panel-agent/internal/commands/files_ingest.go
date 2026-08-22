package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// files.ingest — take a file in the upload staging dir (written by panel-api
// from an incoming chunked or streaming upload) and move it into the user's
// scope at the requested destination path.
//
// The `tmp_path` MUST start with "/var/lib/jabali-uploads/jabali-upload-" so a
// malicious panel-api request can't coerce this command into relocating, e.g.,
// /etc/shadow into the user's homedir. panel-api always writes into that prefix
// so this gate is a belt-and-braces check against request tampering. That
// staging dir is root-owned, flat, and contains no tenant-controlled symlinks,
// so the source side is trusted; the DESTINATION is the attacker surface and is
// resolved escape-proof (Gitea #427 — fourth parent-symlink escape sink).
//
// Why /var/lib/jabali-uploads and not /tmp: jabali-agent.service runs with
// PrivateTmp=yes (install.sh) and so does jabali-panel-api.service. Each service
// therefore has its own per-unit /tmp namespace, so a file panel-api writes to
// /tmp is invisible to the agent. Same scar story as the app-install staging
// path documented in commands/staging_tmp.go (commit 29823c3).

type filesIngestParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM
	TmpPath   string `json:"tmp_path"`
	DestPath  string `json:"dest_path"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

type filesIngestResponse struct {
	DestPath string `json:"dest_path"`
	Bytes    int64  `json:"bytes"`
}

const tmpUploadPrefix = "/var/lib/jabali-uploads/jabali-upload-"

func filesIngestHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesIngestParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if p.Username == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username required"}
	}
	if p.TmpPath == "" || p.DestPath == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "tmp_path and dest_path required"}
	}
	if !strings.HasPrefix(p.TmpPath, tmpUploadPrefix) || strings.Contains(p.TmpPath[len(tmpUploadPrefix):], "/") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "tmp_path must be a jabali-upload scratch file",
		}
	}

	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("scope: %v", err),
		}
	}
	// JAB-358: confine the ingest destination to the admin write allow-list.
	scope = scope.WriteScope()
	dst, err := scope.Clean(p.DestPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("dest_path validation failed: %v", err),
		}
	}

	// Existence/collision check through the escape-proof parent fd.
	existing, serr := scope.ExistsInScope(dst)
	if serr != nil {
		_ = os.Remove(p.TmpPath)
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("stat dest: %v", serr)}
	}
	if existing != nil {
		// Never replace a directory with an uploaded file.
		if existing.IsDir {
			_ = os.Remove(p.TmpPath)
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeFailedPrecondition,
				Message: "target is a directory",
			}
		}
		if !p.Overwrite {
			_ = os.Remove(p.TmpPath)
			// "already_exists" token lets panel-api map this to HTTP 409 so the
			// upload UI can offer overwrite / keep-both / cancel (GH #188).
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeAlreadyExists,
				Message: "already_exists: target path already exists",
			}
		}
		// Overwrite: unlink the existing leaf (file or symlink) escape-proof so
		// the rename/create below lands a fresh file, never writes THROUGH a
		// planted symlink.
		if rerr := scope.RemoveInScope(dst, false); rerr != nil && !os.IsNotExist(rerr) {
			_ = os.Remove(p.TmpPath)
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("prepare overwrite: %v", rerr)}
		}
	}

	uid, gid := fileOwnerIDs(p.Username, p.AdminRoot)

	// Try rename first — same filesystem, atomic. The destination parent is an
	// openat2 fd; the source is the trusted staging file (renameat AT_FDCWD).
	if err := scope.RenameIntoInScope(p.TmpPath, dst); err == nil {
		size, cerr := finalizeIngest(scope, dst, uid, gid)
		if cerr != nil {
			return nil, cerr
		}
		return &filesIngestResponse{DestPath: dst, Bytes: size}, nil
	}

	// Cross-filesystem fallback: copy the bytes into a fresh escape-proof file,
	// then unlink the source.
	in, err := os.Open(p.TmpPath)
	if err != nil {
		return nil, classifyFSWriteErr("open_tmp", err)
	}
	defer in.Close()
	out, err := scope.CreateExclInScope(dst, 0o644)
	if err != nil {
		return nil, classifyFSWriteErr("open_dst", err)
	}
	size, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = scope.RemoveInScope(dst, false)
		return nil, classifyFSWriteErr("copy_dst", err)
	}
	_ = os.Remove(p.TmpPath)
	if _, cerr := finalizeIngest(scope, dst, uid, gid); cerr != nil {
		return nil, cerr
	}
	return &filesIngestResponse{DestPath: dst, Bytes: size}, nil
}

// finalizeIngest reopens the ingested file escape-proof and sets its
// owner/group to <user>:www-data and mode 0644 via the fd (no path
// re-resolution). chown is where an over-quota target surfaces EDQUOT — it is
// classified so panel-api returns a clear 'quota exceeded' instead of 500.
func finalizeIngest(scope *filesafe.Scope, dst string, uid, gid int) (int64, *agentwire.AgentError) {
	f, err := scope.OpenInScope(dst, os.O_RDONLY, 0)
	if err != nil {
		_ = scope.RemoveInScope(dst, false)
		return 0, classifyFSWriteErr("reopen_ingest", err)
	}
	defer f.Close()
	var size int64
	if fi, serr := f.Stat(); serr == nil {
		size = fi.Size()
	}
	if err := f.Chown(uid, gid); err != nil {
		_ = scope.RemoveInScope(dst, false)
		return 0, classifyFSWriteErr("chown_ingest", err)
	}
	_ = f.Chmod(sensitiveFileMode(dst, 0o644))
	return size, nil
}

func init() {
	Default.Register("files.ingest", filesIngestHandler)
}
