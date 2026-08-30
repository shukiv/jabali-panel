package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// domain.docroot.delete — opt-in removal of a domain's document root, used only
// when a tenant explicitly ticks "also delete the files" on domain delete
// (GH #1382). Two deliberate safety properties:
//
//   - It is a SEPARATE verb, never folded into domain.delete, so the implicit
//     teardown paths (user-delete cascade, billing-cancel) can NEVER delete a
//     customer's files. Only the explicit human-approved HTTP delete calls it.
//   - The removal runs AS THE TENANT UID (runFSAsUser → setuid child), so the
//     kernel confines it to files the tenant already owns. A tenant who wins a
//     TOCTOU race by swapping a parent dir for a symlink still can't make the
//     agent delete anything they couldn't delete themselves over SSH.

type domainDocrootDeleteParams struct {
	Username string `json:"username"`
	Docroot  string `json:"docroot"`
}

type domainDocrootDeleteResponse struct {
	Docroot string `json:"docroot"`
	Deleted bool   `json:"deleted"`
	Skipped string `json:"skipped,omitempty"`
}

func domainDocrootDeleteHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p domainDocrootDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	uid, gid, err := resolveTenantDocroot(p.Username, p.Docroot)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: err.Error()}
	}

	fi, lerr := os.Lstat(p.Docroot)
	if errors.Is(lerr, os.ErrNotExist) {
		return domainDocrootDeleteResponse{Docroot: p.Docroot, Deleted: false, Skipped: "not present"}, nil
	}
	// A docroot that is itself a symlink: remove ONLY the link, never its target.
	if lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		if out, rerr := runFSAsUser(uid, gid, "rm", "-f", "--", p.Docroot); rerr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove docroot symlink: %v: %s", rerr, string(out))}
		}
		return domainDocrootDeleteResponse{Docroot: p.Docroot, Deleted: true}, nil
	}
	// Recursive delete AS THE TENANT — coreutils rm does not descend into
	// symlinked directories, and the setuid child means the kernel enforces the
	// tenant's own permissions on every unlink.
	if out, rerr := runFSAsUser(uid, gid, "rm", "-rf", "--", p.Docroot); rerr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove docroot: %v: %s", rerr, string(out))}
	}
	return domainDocrootDeleteResponse{Docroot: p.Docroot, Deleted: true}, nil
}

// docrootUnderHome is the pure path guard: the docroot must be a clean absolute
// path at least two levels below home. Two-levels-deep excludes home itself, a
// bare ~/public_html and a bare ~/domains (no denylist to keep in sync), and the
// Rel escape check rejects anything outside home — including the /home/aliceXXX
// prefix-sibling trick, since Rel("/home/alice", "/home/alicexx/y") starts "..".
func docrootUnderHome(home, docroot string) error {
	if !filepath.IsAbs(docroot) || filepath.Clean(docroot) != docroot {
		return errors.New("docroot must be an absolute, clean path")
	}
	rel, err := filepath.Rel(home, docroot)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("docroot must be inside the tenant's home directory")
	}
	if len(strings.Split(rel, string(os.PathSeparator))) < 2 {
		return errors.New("refusing to delete a shared top-level directory")
	}
	return nil
}

// resolveTenantDocroot validates username + docroot and returns the tenant's
// uid/gid. It REFUSES anything that isn't a clean absolute path at least two
// levels below the tenant's own home — which excludes the home itself, a bare
// ~/public_html, and a bare ~/domains, as well as any path outside the home or
// carrying dot-segments. A uid of 0 is refused outright.
func resolveTenantDocroot(username, docroot string) (uid, gid uint32, err error) {
	if username == "" {
		return 0, 0, errors.New("username required")
	}
	u, err := user.Lookup(username)
	if err != nil {
		return 0, 0, fmt.Errorf("unknown user %q", username)
	}
	if u.HomeDir == "" || !filepath.IsAbs(u.HomeDir) {
		return 0, 0, fmt.Errorf("user %q has no home directory", username)
	}
	if err := docrootUnderHome(u.HomeDir, docroot); err != nil {
		return 0, 0, err
	}
	uid64, uerr := strconv.ParseUint(u.Uid, 10, 32)
	gid64, gerr := strconv.ParseUint(u.Gid, 10, 32)
	if uerr != nil || gerr != nil {
		return 0, 0, fmt.Errorf("resolve uid/gid for %q", username)
	}
	if uid64 == 0 {
		return 0, 0, errors.New("refusing to delete as root")
	}
	return uint32(uid64), uint32(gid64), nil
}

func init() {
	Default.Register("domain.docroot.delete", domainDocrootDeleteHandler)
}
