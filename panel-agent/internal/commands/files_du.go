package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// files.du — disk-usage view of one directory's immediate children, scoped to
// the user's home. Unlike files.list (which reports a directory's inode size),
// each subdirectory carries its RECURSIVE byte size (one `du --max-depth=1`
// call); files carry their own size. Powers the Files disk-usage tree, which
// lazily fetches one level per expand.
//
//	files.du {user_id, username, path} -> {path, total, entries[]}

type filesDuParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM: root scope + deny-list
	Path      string `json:"path"`
}

type filesDuEntry struct {
	Name       string `json:"name"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	HasSubdirs bool   `json:"has_subdirs"`
}

type filesDuResponse struct {
	Path    string         `json:"path"`
	Total   int64          `json:"total"`
	Entries []filesDuEntry `json:"entries"`
}

func filesDuHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesDuParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.Username == "" || p.Path == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username and path required"}
	}

	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("scope: %v", err)}
	}
	// Resolve is now escape-proof at check time (fail-closed nearest-ancestor
	// canonicalization, Gitea #424); entry metadata comes from the escape-proof
	// ReadDirInScope (fstatat AT_SYMLINK_NOFOLLOW). The recursive-size `du`
	// shell-out runs on the canonical path — a parent swap between resolve and
	// exec is a residual on read-only size info only.
	resolved, err := scope.Resolve(p.Path)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("path validation failed: %v", err)}
	}

	// GH #653: run du against an ESCAPE-PROOF dir fd instead of the resolvable
	// string path. The fd is passed to the child via ExtraFiles (fd 3) so du
	// traverses the pinned inode (/proc/self/fd/3) — a parent-directory swap
	// between resolve and du can no longer redirect root's du outside scope.
	// GH #657: use OpenDirInScope (not OpenInScope). OpenInScope refuses the
	// docroot itself ("refusing to open docroot itself as a file"), so a du at
	// the home root — which the "Folder sizes" button hits by default — 502'd
	// instantly. OpenDirInScope allows rel=="." (the home root) and opens
	// O_DIRECTORY, exactly what du needs; the sibling ReadDirInScope below
	// already relies on it for the same path.
	df, derr := scope.OpenDirInScope(resolved)
	if derr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("open dir: %v", derr)}
	}
	// GH #657: bound the recursive `du` so a huge tree can't hang the request
	// past the gateway timeout (Cloudflare ~100s) — which surfaced as an opaque
	// "invalid or incomplete response" 520. A hard 45s budget (under the panel's
	// call ctx) lets du finish for most trees; if it can't, return a clean,
	// explicit error instead of silently-partial (wrong) sizes or a 520.
	duCtx, duCancel := context.WithTimeout(ctx, 45*time.Second)
	defer duCancel()
	dirSizes, duTimedOut := duSubdirSizes(duCtx, df, resolved)
	df.Close()
	if duTimedOut {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeDeadlineExceeded,
			Message: "size calculation timed out — this directory is too large to size within 45s; size a smaller subfolder instead",
		}
	}

	entries, err := scope.ReadDirInScope(resolved)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read dir: %v", err)}
	}
	result := []filesDuEntry{}
	for _, e := range entries {
		full := filepath.Join(resolved, e.Name)
		isDir := e.IsDir && !e.IsSymlink
		var size int64
		hasSub := false
		if isDir {
			if v, ok := dirSizes[full]; ok {
				size = v
			}
			hasSub = dirHasSubdir(scope, full)
		} else {
			size = e.Size
		}
		result = append(result, filesDuEntry{Name: e.Name, IsDir: isDir, Size: size, HasSubdirs: hasSub})
	}
	// Largest first — disk-usage view wants the heavy entries up top.
	sort.SliceStable(result, func(i, j int) bool { return result[i].Size > result[j].Size })

	return &filesDuResponse{Path: resolved, Total: dirSizes[resolved], Entries: result}, nil
}

// duSubdirSizes runs one `du -B1 -D --max-depth=1` against the pinned directory
// fd (passed as child fd 3 via ExtraFiles) and returns a map of canonical path
// → recursive DISK-USAGE size for the directory itself and each immediate child.
//
// -B1 = disk usage in 1-byte units (actual blocks on disk), NOT apparent size.
// GH #1184: the admin File Manager sizes "/", where apparent size (-b) counted
// /proc/kcore's 128 TiB virtual size and reported a nonsense ~131 TB total.
// Disk usage counts kcore (and every sparse file) as its real 0 blocks, and it
// is the number an operator actually cares about (what fills the disk). The
// pseudo/virtual filesystems are also excluded so du never walks them (they are
// not real storage, and /proc alone can push a busy host past the 45s budget).
// fd 3 is guaranteed by ExtraFiles, so the excludes anchor to /proc/self/fd/3/*
// — no-ops for a tenant /home du, effective only when the root is "/".
//
// --max-depth=1 = this level only; -D = follow the /proc/self/fd/3 magic symlink
// to the pinned inode (WITHOUT it, du measures the symlink node and reports no
// children, so every folder came back 0 — GH #657). -D dereferences only the
// command-line arg; in-tree symlinks are still not followed, preserving the GH
// #653 escape-proofing. The child's fd-relative output paths are remapped back
// onto `canonical` for the caller's lookups.
func duSubdirSizes(ctx context.Context, dirFd *os.File, canonical string) (map[string]int64, bool) {
	dirSizes := map[string]int64{}
	duCmd := execCommandContext(ctx, "du", "-B1", "-D", "--max-depth=1",
		"--exclude=/proc/self/fd/3/proc",
		"--exclude=/proc/self/fd/3/sys",
		"--exclude=/proc/self/fd/3/dev",
		"--exclude=/proc/self/fd/3/run",
		"/proc/self/fd/3")
	duCmd.ExtraFiles = []*os.File{dirFd}
	out, _ := duCmd.Output()
	const procPfx = "/proc/self/fd/3"
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		tab := strings.IndexByte(line, '\t')
		if tab <= 0 {
			continue
		}
		sz, perr := strconv.ParseInt(strings.TrimSpace(line[:tab]), 10, 64)
		if perr != nil {
			continue
		}
		path := strings.TrimSpace(line[tab+1:])
		if path == procPfx {
			path = canonical
		} else if strings.HasPrefix(path, procPfx+"/") {
			path = canonical + path[len(procPfx):]
		}
		dirSizes[path] = sz
	}
	// A fired deadline means du was killed mid-walk (directory too large); the
	// caller turns this into an explicit error rather than shipping the partial
	// (and therefore wrong) sizes we managed to parse.
	return dirSizes, ctx.Err() == context.DeadlineExceeded
}

func init() {
	Default.Register("files.du", filesDuHandler)
}
