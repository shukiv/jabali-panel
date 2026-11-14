package commands

// Files a deleted isolated FTP/SFTP account (GH #1145) left behind keep its
// uid, which no account holds afterwards. They are handed to the tenant that
// owned the account, so the files in a tenant's home are the tenant's own:
//
//   - ftpaccount.delete re-owns what is in the account's folder right after
//     userdel, through the jail's bind mount (<jail>/data), before the jail is
//     torn down. It has ftpDeleteReownBudget and never fails the delete.
//   - ftpaccount.orphan_uids lists the reserved-range uids that still own
//     files but have no passwd entry (repquota prints them as "#<uid>").
//   - ftpaccount.reown_orphans walks one tenant's home and re-owns what such
//     a uid owns. The panel's reconciler runs these two while the list is
//     not empty. That also covers files moved out of the folder before the
//     delete, and accounts deleted before this existed.
//
// Only the owner always changes. The group changes when it is the deleted
// account's own group or a group that no longer exists; any other group (a
// docroot's www-data) stays.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/fsperm"
)

var (
	// ftpDeleteReownBudget bounds the re-own inside ftpaccount.delete, which
	// the panel calls with a 30s timeout. The sweep finds what is left.
	ftpDeleteReownBudget = 8 * time.Second
	// ftpReownOrphansBudget bounds one ftpaccount.reown_orphans walk; the
	// panel waits a little longer.
	ftpReownOrphansBudget = 20 * time.Minute

	// Seams for tests.
	ftpReownTree   = fsperm.ReownTree
	ftpJailMounted = isPathMounted
)

// ftpReownKeepMax caps keep_uids: one uid per isolated account on the host.
const ftpReownKeepMax = 100000

// ftpUIDHasPasswd reports whether uid has a passwd entry. Anything but a clean
// "unknown uid" answer counts as yes, so a failed lookup never re-owns the
// files of an account that exists.
var ftpUIDHasPasswd = func(uid uint32) bool {
	_, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	var unknown user.UnknownUserIdError
	return !errors.As(err, &unknown)
}

// ftpGroupGone reports whether no group has gid. A failed lookup counts as
// present, so the group is kept.
var ftpGroupGone = func(gid uint32) bool {
	_, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), 10))
	var unknown user.UnknownGroupIdError
	return errors.As(err, &unknown)
}

// ftpReownDeadline is now+budget, or earlier when the caller's deadline is
// closer, leaving room for the rest of the call.
func ftpReownDeadline(ctx context.Context, budget, reserve time.Duration) time.Time {
	deadline := time.Now().Add(budget)
	if dl, ok := ctx.Deadline(); ok && dl.Add(-reserve).Before(deadline) {
		deadline = dl.Add(-reserve)
	}
	return deadline
}

// ftpReownSummary is what ftpaccount.delete reports about the re-own.
type ftpReownSummary struct {
	Reowned    int    `json:"reowned"`
	Stopped    bool   `json:"stopped,omitempty"`
	Failed     int    `json:"failed,omitempty"`
	FirstError string `json:"first_error,omitempty"`
	Linked     int    `json:"linked,omitempty"`
	Skipped    string `json:"skipped,omitempty"`
}

// reownDeletedAccountFiles gives the tenant what the just-deleted isolated
// account (uid, gid) owns in its folder. The folder is reached through the
// jail's bind mount: the jail chain is root-owned and the mount pins the
// folder, so renaming a directory in the tenant's home can't move the walk.
// It never fails the delete; the sweep finds anything left.
func reownDeletedAccountFiles(ctx context.Context, tenant *ftpTenant, jailPath string, uid, gid int) ftpReownSummary {
	if uid < ftpSubaccountUIDMin || uid == tenant.UID {
		return ftpReownSummary{Skipped: "not an isolated account uid"}
	}
	mountpoint := filepath.Join(jailPath, ftpJailMountpoint)
	if mounted, err := ftpJailMounted(mountpoint); err != nil || !mounted {
		return ftpReownSummary{Skipped: "folder not mounted"}
	}
	deadline := ftpReownDeadline(ctx, ftpDeleteReownBudget, 5*time.Second)
	stop := func() bool { return ctx.Err() != nil || time.Now().After(deadline) }
	match := func(owner uint32) bool { return int64(owner) == int64(uid) }
	newGID := func(g uint32) int {
		if int64(g) == int64(gid) || ftpGroupGone(g) {
			return tenant.GID
		}
		return -1
	}
	st, err := ftpReownTree(jailPath, mountpoint, match, tenant.UID, newGID, stop)
	sum := ftpReownSummary{Reowned: st.Reowned[uint32(uid)], Stopped: st.Stopped, Failed: st.Failed, FirstError: st.FirstError, Linked: st.Linked}
	if err != nil {
		sum.Skipped = err.Error()
	}
	if err != nil || sum.Stopped || sum.Failed > 0 || sum.Linked > 0 {
		slog.Warn("ftpaccount.delete: files of the deleted account not all handed to the tenant; the sweep finishes",
			"tenant", tenant.Username, "uid", uid, "reowned", sum.Reowned, "stopped", sum.Stopped,
			"failed", sum.Failed, "linked", sum.Linked, "first_error", sum.FirstError, "err", err)
	}
	return sum
}

// ---- ftpaccount.orphan_uids ----

type ftpOrphanUIDsParams struct {
	Mount string `json:"mount"`
}

type ftpOrphanUID struct {
	UID    uint32 `json:"uid"`
	Files  uint64 `json:"files"`
	UsedKB uint64 `json:"used_kb"`
}

func ftpOrphanUIDsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ftpOrphanUIDsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if !filepath.IsAbs(p.Mount) || filepath.Clean(p.Mount) != p.Mount || strings.ContainsAny(p.Mount, "\x00\r\n") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "mount must be an absolute, clean path (explicit-mount invariant, ADR-0032)"}
	}
	stdout, stderr, err := runCmd(ctx, "repquota", "-O", "csv", p.Mount)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("repquota %s failed (is quota enabled on the mount?): %v: %s", p.Mount, err, strings.TrimSpace(string(stderr))),
		}
	}
	found, partial := parseRepquotaOrphans(string(stdout))
	orphans := make([]ftpOrphanUID, 0, len(found))
	for _, o := range found {
		if !ftpUIDHasPasswd(o.UID) {
			orphans = append(orphans, o)
		}
	}
	return map[string]any{"mount": p.Mount, "orphans": orphans, "partial": partial}, nil
}

// parseRepquotaOrphans picks the "#<uid>" rows of `repquota -O csv` (a uid
// with no passwd entry) in the reserved isolated range that still hold blocks
// or files. Columns as in parseRepquotaCSV; FileUsed is index 7. A row whose
// numbers don't parse sets partial.
func parseRepquotaOrphans(output string) (orphans []ftpOrphanUID, partial bool) {
	for _, line := range strings.Split(output, "\n") {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) < 8 || !strings.HasPrefix(f[0], "#") {
			continue
		}
		uid, err := strconv.ParseUint(strings.TrimPrefix(f[0], "#"), 10, 32)
		if err != nil {
			partial = true
			continue
		}
		if uid < ftpSubaccountUIDMin {
			continue
		}
		used, uErr := strconv.ParseUint(strings.TrimSpace(f[3]), 10, 64)
		files, fErr := strconv.ParseUint(strings.TrimSpace(f[7]), 10, 64)
		if uErr != nil || fErr != nil {
			partial = true
			continue
		}
		if used == 0 && files == 0 {
			continue // a limit left on the uid, no files
		}
		orphans = append(orphans, ftpOrphanUID{UID: uint32(uid), Files: files, UsedKB: used})
	}
	return orphans, partial
}

// ---- ftpaccount.reown_orphans ----

type ftpReownOrphansParams struct {
	TenantUsername string `json:"tenant_username"`
	// KeepUIDs are the uids of every isolated account the panel has a row
	// for. A row whose passwd entry is missing is drift the reconciler
	// re-creates with the same uid, so its files are never re-owned.
	KeepUIDs []uint32 `json:"keep_uids"`
}

func ftpReownOrphansHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ftpReownOrphansParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	tenant, aerr := resolveFtpTenant(p.TenantUsername)
	if aerr != nil {
		return nil, aerr
	}
	if len(p.KeepUIDs) > ftpReownKeepMax {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("keep_uids has %d entries, more than %d", len(p.KeepUIDs), ftpReownKeepMax)}
	}
	keep := make(map[uint32]struct{}, len(p.KeepUIDs))
	for _, u := range p.KeepUIDs {
		keep[u] = struct{}{}
	}
	// Looked up when first seen in the walk. A uid is never reused, so a
	// "no passwd entry" answer holds for the rest of the walk.
	dead := map[uint32]bool{}
	match := func(owner uint32) bool {
		if owner < ftpSubaccountUIDMin {
			return false
		}
		if _, ok := keep[owner]; ok {
			return false
		}
		d, seen := dead[owner]
		if !seen {
			d = !ftpUIDHasPasswd(owner)
			dead[owner] = d
		}
		return d
	}
	gone := map[uint32]bool{}
	newGID := func(g uint32) int {
		v, seen := gone[g]
		if !seen {
			v = ftpGroupGone(g)
			gone[g] = v
		}
		if v {
			return tenant.GID
		}
		return -1
	}
	deadline := ftpReownDeadline(ctx, ftpReownOrphansBudget, 5*time.Second)
	stop := func() bool { return ctx.Err() != nil || time.Now().After(deadline) }

	st, err := ftpReownTree(tenant.HomeDir, tenant.HomeDir, match, tenant.UID, newGID, stop)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("walk %s: %v", tenant.HomeDir, err)}
	}
	reowned := make(map[string]int, len(st.Reowned))
	for uid, n := range st.Reowned {
		reowned[strconv.FormatUint(uint64(uid), 10)] = n
	}
	if st.Failed > 0 || st.Linked > 0 {
		slog.Warn("ftpaccount.reown_orphans: some entries were not handed to the tenant",
			"tenant", tenant.Username, "failed", st.Failed, "linked", st.Linked, "first_error", st.FirstError)
	}
	return map[string]any{
		"tenant_username": tenant.Username,
		"reowned":         reowned,
		"scanned":         st.Scanned,
		"stopped":         st.Stopped,
		"failed":          st.Failed,
		"first_error":     st.FirstError,
		// Files with a hard link outside the tenant's home: left as they are.
		"linked": st.Linked,
	}, nil
}

func init() {
	Default.Register("ftpaccount.orphan_uids", ftpOrphanUIDsHandler)
	Default.Register("ftpaccount.reown_orphans", ftpReownOrphansHandler)
}
