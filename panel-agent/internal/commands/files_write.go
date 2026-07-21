package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// classifyFSWriteErr turns a low-level write/chown/rename error into an
// agent error with a stable machine-readable code in the message prefix.
// The panel-api router matches on these prefixes to return 507 (quota)
// or 507 (disk full) instead of opaque 500s.
//
//   - EDQUOT  → "quota_exceeded: …"
//   - ENOSPC  → "disk_full: …"
//   - EACCES  → permission_denied code (most specific agentwire code)
//   - default → CodeInternal with the raw syserror text
func classifyFSWriteErr(stage string, err error) *agentwire.AgentError {
	switch {
	case errors.Is(err, syscall.EDQUOT):
		return &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("quota_exceeded: %s: %v", stage, err),
		}
	case errors.Is(err, syscall.ENOSPC):
		return &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("disk_full: %s: %v", stage, err),
		}
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("%s: %v", stage, err),
		}
	default:
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("%s: %v", stage, err),
		}
	}
}

// filesWriteParams is the input shape for files.write.
type filesWriteParams struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	Mode     string `json:"mode,omitempty"` // "append" or "overwrite" (default)
}

// filesWriteResponse is the output shape for files.write.
type filesWriteResponse struct {
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytes_written"`
}

func filesWriteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesWriteParams
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

	// Enforce content size cap at 100MB
	const maxContentSize int64 = 100 * 1024 * 1024
	if int64(len(p.Content)) > maxContentSize {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("content exceeds 100MB limit (%d bytes)", len(p.Content)),
		}
	}

	// Create filesafe scope with user's home directory
	homeDir := fmt.Sprintf("/home/%s", p.Username)
	scope, err := filesafe.NewScope(p.UserID, p.Username, []string{homeDir})
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to create scope: %v", err),
		}
	}

	// Validate path is in scope (string gate). The actual file ops below are
	// performed against an escape-proof openat2 fd (RESOLVE_BENEATH), so a
	// tenant-planted PARENT symlink can never redirect the root-side write
	// (Gitea #421 / root cause #424).
	cleanPath, err := scope.Clean(p.Path)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("path validation failed: %v", err),
		}
	}

	// For overwrite mode (or if file doesn't exist), use temp-file-then-rename pattern
	if p.Mode != "append" {
		// Lookup user to get uid/gid
		u, err := user.Lookup(p.Username)
		if err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("failed to lookup user %q: %v", p.Username, err),
			}
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		// New files under the docroot must be group www-data so nginx (running
		// as www-data) can serve them via the 0640 group-read bit set below; the
		// user's own primary group would leave them unreadable AND surface to the
		// operator as "User:User" instead of "User:www-data" (GH #533). Mirror
		// files.mkdir / files.extract (hostingIDs): keep the user's gid only if
		// www-data is somehow absent.
		if g, gerr := user.LookupGroup("www-data"); gerr == nil {
			if wgid, werr := strconv.Atoi(g.Gid); werr == nil {
				gid = wgid
			}
		}

		// Generate temp filename with random suffix
		randBytes := make([]byte, 8)
		if _, err := rand.Read(randBytes); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to generate random suffix: %v", err),
			}
		}
		tmpPath := fmt.Sprintf("%s.tmp.%s", cleanPath, hex.EncodeToString(randBytes))

		// Create the temp file via an escape-proof fd: O_CREAT|O_EXCL|O_NOFOLLOW
		// against the openat2-resolved parent dir, so no parent symlink can
		// redirect where the bytes land.
		tmpFile, err := scope.CreateExclInScope(tmpPath, 0600)
		if err != nil {
			return nil, classifyFSWriteErr("create_tempfile", err)
		}

		// Write content
		n, err := tmpFile.WriteString(p.Content)
		if err != nil {
			tmpFile.Close()
			_ = scope.RemoveInScope(tmpPath, false)
			return nil, classifyFSWriteErr("write_tempfile", err)
		}

		// Fsync to ensure data is written
		if err := tmpFile.Sync(); err != nil {
			tmpFile.Close()
			_ = scope.RemoveInScope(tmpPath, false)
			return nil, classifyFSWriteErr("sync_tempfile", err)
		}

		// Chown the OPEN fd to user:www-data (no path re-resolve). This is where
		// EDQUOT typically surfaces on upload: agent wrote the bytes as root
		// (unlimited quota), and the kernel re-charges the bytes to the target
		// uid when ownership transfers. If the recipient is over quota, chown
		// returns EDQUOT even though the write succeeded.
		if err := tmpFile.Chown(uid, gid); err != nil {
			tmpFile.Close()
			_ = scope.RemoveInScope(tmpPath, false)
			return nil, classifyFSWriteErr("chown_tempfile", err)
		}

		// Chmod to 0640 via the fd: owner rw, www-data group r (nginx static
		// read), other none. Matches per-user FPM isolation.
		if err := tmpFile.Chmod(sensitiveFileMode(cleanPath, 0640)); err != nil {
			tmpFile.Close()
			_ = scope.RemoveInScope(tmpPath, false)
			return nil, classifyFSWriteErr("chmod_tempfile", err)
		}

		tmpFile.Close()

		// Atomic rename, escape-proof on BOTH parents (renameat between fds).
		if err := scope.RenameInScope(tmpPath, cleanPath); err != nil {
			_ = scope.RemoveInScope(tmpPath, false)
			return nil, classifyFSWriteErr("rename_tempfile", err)
		}

		return &filesWriteResponse{
			Path:         cleanPath,
			BytesWritten: int64(n),
		}, nil
	}

	// Append mode: open existing file or create new one, escape-proof. Record
	// whether the file already existed BEFORE opening: if O_CREATE makes a new
	// one, the agent (root) would leave it root-owned, so it must be chowned to
	// <user>:www-data afterwards — the same invariant the overwrite path applies
	// (GH #533 follow-up). An existing file's ownership is left untouched.
	preExisting, existErr := scope.ExistsInScope(cleanPath)
	if existErr != nil {
		return nil, classifyFSWriteErr("stat_append", existErr)
	}

	file, err := scope.OpenInScope(cleanPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, sensitiveFileMode(cleanPath, 0640))
	if err != nil {
		return nil, classifyFSWriteErr("open_append", err)
	}
	defer file.Close()

	// Write content
	n, err := file.WriteString(p.Content)
	if err != nil {
		return nil, classifyFSWriteErr("write_append", err)
	}

	// Newly created via O_CREATE: normalize ownership to <user>:www-data through
	// the open fd (no path re-resolve). Chown after the write so an over-quota
	// target surfaces EDQUOT here, classified like the overwrite path.
	if preExisting == nil {
		u, uerr := user.Lookup(p.Username)
		if uerr != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("failed to lookup user %q: %v", p.Username, uerr),
			}
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		if g, gerr := user.LookupGroup("www-data"); gerr == nil {
			if wgid, werr := strconv.Atoi(g.Gid); werr == nil {
				gid = wgid
			}
		}
		if err := file.Chown(uid, gid); err != nil {
			return nil, classifyFSWriteErr("chown_append", err)
		}
	}

	return &filesWriteResponse{
		Path:         cleanPath,
		BytesWritten: int64(n),
	}, nil
}

func init() {
	Default.Register("files.write", filesWriteHandler)
}
