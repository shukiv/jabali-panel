// domain.reown — move a domain's served docroot tree from the old owner's home
// to the new owner's home and re-own it to the new uid (GH #1238 owner-change).
//
// Same-filesystem move (both docroots live under /home) so the rename is instant
// and preserves the docroot's setgid bit. fsperm.MoveDirUnder reaches both
// parents from their homes without following a link, and the docroot must be a
// real directory. chownTreeRecursive then repoints every
// entry to new_uid:www-data (Lchown-based, symlink-safe — never follows a tenant
// -planted symlink out of the tree). The uid change is the point here: after the
// move the old owner owns nothing in the tree and cannot traverse into the new
// owner's home, so the domain's files are isolated to the new owner.
//
// v1 moves the docroot directory itself (the served tree). The panel refuses the
// owner-change when the domain has an app install (its config would carry the old
// owner's DB credentials), so this verb assumes that gate has passed.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/fsperm"
)

type domainReownParams struct {
	OldDocRoot string `json:"old_doc_root"`
	NewDocRoot string `json:"new_doc_root"`
	NewUID     int    `json:"new_uid"`
	// PruneEmptyDir (GH #1579 rename) is an ancestor of OldDocRoot whose
	// basename is the OLD domain name (e.g. /home/<u>/domains/<oldname> for the
	// nested docroot layout). After the docroot leaf moves out of it, that dir
	// is left empty; when set, prune it if — and only if — it is now empty.
	// Optional and opt-in: chown (GH #1238) never sets it, so its behaviour is
	// unchanged. The default docroot layout (leaf == domain name) leaves it "".
	PruneEmptyDir string `json:"prune_empty_dir,omitempty"`
}

type domainReownResponse struct {
	NewDocRoot  string `json:"new_doc_root"`
	AlreadyDone bool   `json:"already_done"`
}

func domainReownHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p domainReownParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if !strings.HasPrefix(p.OldDocRoot, "/home/") || !strings.HasPrefix(p.NewDocRoot, "/home/") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "old_doc_root and new_doc_root must be under /home/"}
	}
	// Reject traversal / relative segments — the panel builds these by prefix
	// swap, so a clean absolute path is expected.
	if filepath.Clean(p.OldDocRoot) != p.OldDocRoot || filepath.Clean(p.NewDocRoot) != p.NewDocRoot {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "doc_root paths must be clean absolute paths"}
	}
	if p.NewUID <= 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "new_uid must be a positive uid"}
	}
	wwwGID, err := lookupGroup("www-data")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "lookup www-data: " + err.Error()}
	}

	_, oldErr := os.Lstat(p.OldDocRoot)
	_, newErr := os.Lstat(p.NewDocRoot)
	oldExists := oldErr == nil
	newExists := newErr == nil

	// Idempotent: already moved. Still attempt the empty-parent prune — a prior
	// run may have moved the docroot but died before pruning.
	if !oldExists && newExists {
		pruneEmptyRenameDir(p.PruneEmptyDir, p.OldDocRoot)
		return domainReownResponse{NewDocRoot: p.NewDocRoot, AlreadyDone: true}, nil
	}
	if !oldExists {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("source docroot %q not found", p.OldDocRoot)}
	}
	if newExists {
		return nil, &agentwire.AgentError{Code: agentwire.CodeAlreadyExists, Message: fmt.Sprintf("target docroot %q already exists", p.NewDocRoot)}
	}

	// Ensure the destination's parent chain under the new owner's home exists and
	// is owned new_uid:www-data + setgid (matching a normally-created docroot
	// wrapper). Re-owning an already-correct dir is a no-op.
	parent := filepath.Dir(p.NewDocRoot) // /home/<new>/domains/<name>
	grandparent := filepath.Dir(parent)  // /home/<new>/domains
	anchor := filepath.Dir(grandparent)  // /home/<new>, owned by the new owner
	for _, d := range []string{grandparent, parent} {
		if err := ensureOwnedSetgidDir(anchor, d, p.NewUID, wwwGID); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("prepare destination dir %q: %v", d, err)}
		}
	}

	// Move (same fs → instant; preserves the docroot's own setgid), then re-own.
	if err := fsperm.MoveDirUnder(homeOf(p.OldDocRoot), p.OldDocRoot, anchor, p.NewDocRoot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("move docroot %q -> %q: %v", p.OldDocRoot, p.NewDocRoot, err)}
	}
	if err := chownTreeRecursive(filepath.Dir(grandparent), p.NewDocRoot, p.NewUID, wwwGID); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("re-own moved tree (docroot moved to %q; re-run to resume): %v", p.NewDocRoot, err)}
	}
	// Re-assert setgid 2750 on the docroot itself so new files keep inheriting the
	// www-data group (rename preserved it, but be explicit).
	if err := chmodDirUnder(anchor, p.NewDocRoot, os.ModeSetgid|0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("set docroot mode: %v", err)}
	}

	// The docroot leaf has moved out of its old-name wrapper dir (nested layout);
	// remove that wrapper if it is now empty. Best-effort — a non-empty dir
	// (extra siblings the rename did not move) is left untouched.
	pruneEmptyRenameDir(p.PruneEmptyDir, p.OldDocRoot)

	return domainReownResponse{NewDocRoot: p.NewDocRoot}, nil
}

// pruneEmptyRenameDir removes dir when it is a safe, now-empty old-name wrapper
// left behind by a rename move. It is deliberately conservative:
//   - only acts when dir is set (the default docroot layout passes "");
//   - refuses anything not under /home/ or not a clean absolute path;
//   - refuses a dir that is not a strict ANCESTOR of oldDocRoot (so it can only
//     ever be the wrapper the move emptied, never the docroot or an unrelated
//     path);
//   - removes only an EMPTY directory, reached from the old owner's home without
//     following a link (fsperm.RemoveEmptyDirUnder); a non-empty one fails
//     (ignored), so extra sibling files the rename did not move are kept.
func pruneEmptyRenameDir(dir, oldDocRoot string) {
	if !shouldPruneRenameDir(dir, oldDocRoot) {
		return
	}
	_ = fsperm.RemoveEmptyDirUnder(homeOf(oldDocRoot), dir) // no-op when non-empty or absent.
}

// homeOf returns /home/<user> for a path under /home/<user>/.
func homeOf(p string) string {
	user, _, _ := strings.Cut(strings.TrimPrefix(p, "/home/"), "/")
	return "/home/" + user
}

// shouldPruneRenameDir is the pure guard for pruneEmptyRenameDir: it returns
// true only for a set, clean, /home-rooted path that is a STRICT ANCESTOR of
// oldDocRoot (so it can only be the wrapper the move emptied — never the docroot
// itself, an unrelated path, or "").
func shouldPruneRenameDir(dir, oldDocRoot string) bool {
	if dir == "" {
		return false
	}
	if !strings.HasPrefix(dir, "/home/") || filepath.Clean(dir) != dir {
		return false
	}
	// Strict ancestor: oldDocRoot starts with dir + "/". Rules out dir ==
	// oldDocRoot and any sideways path.
	return strings.HasPrefix(oldDocRoot, dir+"/")
}

// ensureOwnedSetgidDir creates dir (if absent) under anchor and sets each new
// component to uid:gid + setgid 2750. Components are created and owned through
// their file descriptors (fsperm.OpenDirUnder), so a symlink swapped in while
// this runs cannot make root chown the link's target. An existing directory is
// left untouched. OpenDirUnder takes a unix mode: os.ModeSetgid is Go's own
// bit, not 02000, and the kernel would drop it.
func ensureOwnedSetgidDir(anchor, dir string, uid, gid int) error {
	f, err := fsperm.OpenDirUnder(anchor, dir, 0o2750, uid, gid)
	if err != nil {
		return err
	}
	return f.Close()
}

// chmodDirUnder sets the mode of an existing directory through its file
// descriptor, after walking to it from anchor without following links.
func chmodDirUnder(anchor, dir string, mode os.FileMode) error {
	f, err := fsperm.OpenDirUnder(anchor, dir, uint32(mode.Perm()), 0, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(mode)
}

func init() {
	Default.Register("domain.reown", domainReownHandler)
}
