package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// files.copy — recursively copy a scoped path to a new location, preserving
// mode bits and reproducing symlinks as symlinks (their target string is data,
// not chased). Cross-directory by design: used by the Copy / Paste flow.
//
// files.copy.start (GH #1392) runs the same copy as a background job so the UI
// can show a real byte-percentage progress bar: it validates synchronously
// (user errors stay immediate 400s), then a detached goroutine counts the
// source bytes, copies with per-chunk progress ticks, and seals the job.
//
// Security (Gitea #422): both src and dst are resolved escape-proof and the
// copy descends via O_NOFOLLOW directory fds on both sides (CopyTreeInScope),
// so a tenant-planted PARENT symlink — or a symlink swapped into the
// partially-built copy mid-walk — can never redirect a root-side write out of
// the docroot. The progress callback only sees a byte count, so it adds nothing
// to that surface.

// copyWallClockBudget bounds a background copy so a detached goroutine
// (context.Background) can't run a runaway copy forever. Mirrors
// extractWallClockBudget.
const copyWallClockBudget = 5 * time.Minute

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

// filesCopyJobResult is the completion payload of an async copy job (GH #1392),
// carried under the job snapshot's "result".
type filesCopyJobResult struct {
	SrcPath string `json:"src_path"`
	DstPath string `json:"dst_path"`
	Bytes   int64  `json:"bytes"`
}

// resolveCopyValidated does all the cheap, synchronous validation shared by the
// blocking and async copy handlers: param presence, escape-proof scoping of both
// ends (confined to the admin write allow-list, JAB-358), same-path/descendant
// refusals, and the no-clobber existence check. On success it returns the
// write-scoped handle, the cleaned paths, and the destination owner ids.
func resolveCopyValidated(p filesCopyParams) (*filesafe.Scope, string, string, int, int, *agentwire.AgentError) {
	if p.Username == "" {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username required"}
	}
	if p.SrcPath == "" {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "src_path required"}
	}
	if p.DstPath == "" {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "dst_path required"}
	}

	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
	if err != nil {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to create scope: %v", err)}
	}
	// JAB-358: copy reads the source and writes the destination through a SINGLE
	// escape-proof scope (CopyTreeInScope), so confine BOTH ends to the admin
	// write allow-list. An admin copies within the tenant/app data roots; system
	// files stay view-only (no-op for tenant scopes).
	scope = scope.WriteScope()
	srcClean, err := scope.Clean(p.SrcPath)
	if err != nil {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("src_path validation failed: %v", err)}
	}
	dstClean, err := scope.Clean(p.DstPath)
	if err != nil {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("dst_path validation failed: %v", err)}
	}
	if srcClean == dstClean {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "source and destination are the same"}
	}
	// Refuse copy of a dir into its own descendant — same shape as move.
	if isDescendant(srcClean, dstClean) {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "cannot copy into a subdirectory of itself"}
	}
	// dst must not already exist — checked through the escape-proof parent fd.
	if existing, err := scope.ExistsInScope(dstClean); err != nil {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("failed to stat destination: %v", err)}
	} else if existing != nil {
		return nil, "", "", 0, 0, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "target path already exists"}
	}

	uid, gid := hostingIDs(p.Username)
	return scope, srcClean, dstClean, uid, gid, nil
}

func filesCopyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesCopyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	scope, srcClean, dstClean, uid, gid, aerr := resolveCopyValidated(p)
	if aerr != nil {
		return nil, aerr
	}

	bytes, err := scope.CopyTreeInScope(ctx, srcClean, dstClean, uid, gid)
	if err != nil {
		// EEXIST is always the very first dst-side write (Mkdirat / O_EXCL open /
		// Symlinkat) — the copy created NOTHING, so a dst appeared in the race
		// window since validation. Never RemoveAll it: that would delete whatever
		// the other writer just put there. Only roll back a partial tree WE built.
		if errors.Is(err, fs.ErrExist) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "target path already exists"}
		}
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

// filesCopyStartHandler (files.copy.start, GH #1392) validates synchronously,
// then copies in a detached goroutine so a large tree can't block the request
// past a proxy timeout and the UI can poll files.job.status for a byte-
// percentage bar.
func filesCopyStartHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p filesCopyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	// Validate up front so user errors are immediate 400s, not jobs that
	// instantly fail.
	scope, srcClean, dstClean, uid, gid, aerr := resolveCopyValidated(p)
	if aerr != nil {
		return nil, aerr
	}
	job, err := newFileJob(p.Username, "copy")
	if err != nil {
		return nil, err
	}
	go func() {
		// Detached from the request context; bound the copy so it can't run
		// forever.
		ctx, cancel := context.WithTimeout(context.Background(), copyWallClockBudget)
		defer cancel()
		// Count first (inside the goroutine — a pre-walk of a huge tree in the
		// start call would re-create the timeout async exists to avoid). A count
		// error just leaves total=0 → the bar shows indeterminate until done.
		if total, cerr := scope.CountTreeBytesInScope(ctx, srcClean); cerr == nil {
			job.setTotal(total)
		}
		var done int64
		bytes, cerr := scope.CopyTreeInScopeProgress(ctx, srcClean, dstClean, uid, gid, func(delta int64) {
			done += delta
			job.tick(done)
		})
		if cerr != nil {
			// EEXIST = a dst appeared during the count window (this async path's
			// count walk widens that window to seconds on the big trees this
			// targets). The copy created nothing, so never RemoveAll someone
			// else's dst — only roll back a partial tree WE built.
			if errors.Is(cerr, fs.ErrExist) {
				job.fail("target path already exists")
				return
			}
			_ = scope.RemoveAllInScope(context.Background(), dstClean)
			job.fail(fmt.Sprintf("copy: %v", cerr))
			return
		}
		job.finish(bytes, &filesCopyJobResult{SrcPath: srcClean, DstPath: dstClean, Bytes: bytes})
	}()
	return map[string]string{"job_id": job.id}, nil
}

func init() {
	Default.Register("files.copy", filesCopyHandler)
	Default.Register("files.copy.start", filesCopyStartHandler)
}
