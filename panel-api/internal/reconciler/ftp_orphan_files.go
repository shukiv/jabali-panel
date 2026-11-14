package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Files of deleted isolated FTP accounts (see the agent's
// ftp_account_reown.go). ftpaccount.delete hands most of them to the tenant
// right away; this pass hands over the rest: files moved out of the
// account's folder before the delete, a walk cut short by its budget, and
// accounts deleted before the delete did this.
//
// While there is nothing to do it costs one `repquota`
// (ftpaccount.orphan_uids). When there is, the agent walks tenant homes one
// at a time, tenants with isolated accounts first, until every uid's file
// count is found. A uid a round didn't finish is left alone until its file
// count changes or its wait is over: ftpOrphanRetryAfter after a complete
// round, ftpOrphanIncompleteRetry after an incomplete one. So a new uid is
// looked for on its own, and one that is in no home doesn't make every
// later round walk every home. The pass runs in its own goroutine, one at a
// time, so a long walk never holds up the tick.

const (
	ftpOrphanQueryTimeout  = time.Minute
	ftpOrphanTenantTimeout = 21 * time.Minute // the agent stops its walk at 20
	ftpOrphanSweepTimeout  = 6 * time.Hour
	// Leftovers outside every home don't change on their own, so a set a
	// complete round didn't find is only looked for again weekly.
	ftpOrphanRetryAfter = 7 * 24 * time.Hour
	// An incomplete round (a walk at its time limit, a file that couldn't
	// change, an agent error) is tried again after an hour, not on every
	// pass, so a home that never finishes doesn't keep the agent walking.
	ftpOrphanIncompleteRetry = time.Hour
	ftpOrphanUserPage        = 500
)

// WithFtpOrphanSweep turns the pass on. quotaMount returns the quota mount
// of /home (limits.QuotaMountFor); an error or "" skips that pass.
func (r *Reconciler) WithFtpOrphanSweep(quotaMount func() (string, error)) *Reconciler {
	r.ftpOrphanQuotaMount = quotaMount
	return r
}

type ftpOrphanUID struct {
	UID    uint32 `json:"uid"`
	Files  uint64 `json:"files"`
	UsedKB uint64 `json:"used_kb"`
}

type ftpReownResult struct {
	Reowned    map[string]int `json:"reowned"`
	Scanned    int            `json:"scanned"`
	Stopped    bool           `json:"stopped"`
	Failed     int            `json:"failed"`
	FirstError string         `json:"first_error"`
	Linked     int            `json:"linked"`
}

// kickFtpOrphanSweep starts the pass in the background unless one is
// already running.
func (r *Reconciler) kickFtpOrphanSweep(ctx context.Context, rows []models.FtpAccount, tenantByUserID map[string]string) {
	if r.ftpOrphanQuotaMount == nil || r.agent == nil || r.users == nil {
		return
	}
	if !r.ftpOrphanRunning.CompareAndSwap(false, true) {
		return
	}
	keep, first := ftpOrphanInputs(rows, tenantByUserID)
	go func() {
		defer r.ftpOrphanRunning.Store(false)
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ftpOrphanSweepTimeout)
		defer cancel()
		r.sweepFtpOrphanFiles(sctx, keep, first)
	}()
}

// ftpOrphanInputs returns the uid of every isolated row, whose files are
// never re-owned (a row whose passwd entry is missing is re-created with its
// uid), and the tenants with isolated rows, which are walked first.
func ftpOrphanInputs(rows []models.FtpAccount, tenantByUserID map[string]string) (keep []uint32, first []string) {
	keep = []uint32{}
	seen := map[string]bool{}
	for _, a := range rows {
		if a.UID != nil {
			keep = append(keep, *a.UID)
		}
		if t := tenantByUserID[a.UserID]; a.Isolated && t != "" && !seen[t] {
			seen[t] = true
			first = append(first, t)
		}
	}
	sort.Strings(first)
	return keep, first
}

// sweepFtpOrphanFiles is one pass; kickFtpOrphanSweep runs it.
func (r *Reconciler) sweepFtpOrphanFiles(ctx context.Context, keep []uint32, first []string) {
	mount, err := r.ftpOrphanQuotaMount()
	if err != nil || mount == "" {
		return
	}
	orphans, err := r.ftpOrphanUIDs(ctx, mount)
	if err != nil {
		// An agent without the verb, or a mount without quota.
		r.log.Debug("ftp: deleted-account file check unavailable", "err", err)
		return
	}
	remaining := r.ftpOrphansToLookFor(orphans, time.Now())
	if len(remaining) == 0 {
		return
	}
	looked := make(map[uint32]bool, len(remaining))
	for uid := range remaining {
		looked[uid] = true
	}
	tenants, err := r.ftpOrphanTenants(ctx, first)
	if err != nil {
		r.log.Warn("ftp: list tenants for deleted-account files failed", "err", err)
		return
	}
	moved, complete := 0, true
	for _, tenant := range tenants {
		if len(remaining) == 0 || ctx.Err() != nil {
			break
		}
		res, err := r.reownFtpOrphans(ctx, tenant, keep)
		if ftpOrphanNoHome(err) {
			// A panel user with no system account (the admin) or no home on
			// disk: nothing there to hand over.
			r.log.Debug("ftp: no home to look in for deleted-account files", "tenant", tenant, "err", err)
			continue
		}
		if err != nil {
			r.log.Warn("ftp: handing deleted-account files to the tenant failed", "tenant", tenant, "err", err)
			complete = false
			continue
		}
		for uidStr, n := range res.Reowned {
			moved += n
			uid, perr := strconv.ParseUint(uidStr, 10, 32)
			if perr != nil {
				continue
			}
			if left, ok := remaining[uint32(uid)]; ok {
				if left -= int64(n); left <= 0 {
					delete(remaining, uint32(uid))
				} else {
					remaining[uint32(uid)] = left
				}
			}
		}
		if res.Stopped {
			complete = false
			r.log.Warn("ftp: deleted-account file walk stopped at its time limit; the next pass continues", "tenant", tenant, "scanned", res.Scanned)
		}
		if res.Failed > 0 {
			complete = false
			r.log.Warn("ftp: some deleted-account files could not be handed to the tenant", "tenant", tenant, "failed", res.Failed, "first_error", res.FirstError)
		}
		if res.Linked > 0 {
			// Not retried: another walk can't make the outside link go away.
			r.log.Warn("ftp: deleted-account files with a hard link outside the tenant's home were left as they are", "tenant", tenant, "files", res.Linked)
		}
	}
	if ctx.Err() != nil {
		complete = false
	}
	if moved > 0 {
		r.log.Info("ftp: handed files of deleted FTP accounts to their tenants", "files", moved)
	}
	if len(remaining) > 0 && complete {
		r.log.Warn("ftp: files of deleted FTP accounts not found in any tenant home", "uids", ftpOrphanKeys(remaining))
	}

	// Remember what this round left of the uids it looked for, so they
	// aren't looked for again on every pass.
	wait := ftpOrphanRetryAfter
	if !complete {
		wait = ftpOrphanIncompleteRetry
	}
	left := orphans
	if l, err := r.ftpOrphanUIDs(ctx, mount); err == nil {
		left = l
	}
	until := time.Now().Add(wait)
	r.ftpOrphanMu.Lock()
	if r.ftpOrphanSeen == nil {
		r.ftpOrphanSeen = map[uint32]ftpOrphanMemo{}
	}
	for _, o := range left {
		if looked[o.UID] {
			r.ftpOrphanSeen[o.UID] = ftpOrphanMemo{files: o.Files, until: until}
		}
	}
	r.ftpOrphanMu.Unlock()
}

// ftpOrphanMemo: a uid's file count when a round last left it, and until
// when it is left alone at that count.
type ftpOrphanMemo struct {
	files uint64
	until time.Time
}

// ftpOrphansToLookFor returns the listed uids to look for, with their file
// counts: those not left alone at the same count by an earlier round. It
// forgets uids that are no longer listed.
func (r *Reconciler) ftpOrphansToLookFor(orphans []ftpOrphanUID, now time.Time) map[uint32]int64 {
	r.ftpOrphanMu.Lock()
	defer r.ftpOrphanMu.Unlock()
	listed := make(map[uint32]bool, len(orphans))
	out := map[uint32]int64{}
	for _, o := range orphans {
		listed[o.UID] = true
		if m, ok := r.ftpOrphanSeen[o.UID]; ok && m.files == o.Files && now.Before(m.until) {
			continue
		}
		out[o.UID] = max(int64(o.Files), 1)
	}
	for uid := range r.ftpOrphanSeen {
		if !listed[uid] {
			delete(r.ftpOrphanSeen, uid)
		}
	}
	return out
}

// ftpOrphanNoHome: the agent found no tenant home to walk (no system
// account, none usable, or none on disk). Asking again won't change that,
// so it doesn't make the round incomplete. Only the reown handler answers
// not_found; the agent's connection gate answers permission_denied.
func ftpOrphanNoHome(err error) bool {
	var ae *agentwire.AgentError
	return errors.As(err, &ae) && ae.Code == agentwire.CodeNotFound
}

func (r *Reconciler) ftpOrphanUIDs(ctx context.Context, mount string) ([]ftpOrphanUID, error) {
	cctx, cancel := context.WithTimeout(ctx, ftpOrphanQueryTimeout)
	defer cancel()
	raw, err := r.agent.Call(cctx, "ftpaccount.orphan_uids", map[string]any{"mount": mount})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Orphans []ftpOrphanUID `json:"orphans"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse ftpaccount.orphan_uids: %w", err)
	}
	return resp.Orphans, nil
}

func (r *Reconciler) reownFtpOrphans(ctx context.Context, tenant string, keep []uint32) (ftpReownResult, error) {
	cctx, cancel := context.WithTimeout(ctx, ftpOrphanTenantTimeout)
	defer cancel()
	var res ftpReownResult
	raw, err := r.agent.Call(cctx, "ftpaccount.reown_orphans", map[string]any{
		"tenant_username": tenant,
		"keep_uids":       keep,
	})
	if err != nil {
		return res, err
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return res, fmt.Errorf("parse ftpaccount.reown_orphans: %w", err)
	}
	return res, nil
}

// ftpOrphanTenants is first, then every other user with a system account.
func (r *Reconciler) ftpOrphanTenants(ctx context.Context, first []string) ([]string, error) {
	out := append([]string(nil), first...)
	seen := map[string]bool{}
	for _, t := range first {
		seen[t] = true
	}
	for offset := 0; ; offset += ftpOrphanUserPage {
		// Paged by the unique username, so a page boundary can't skip a user.
		users, _, err := r.users.List(ctx, repository.ListOptions{Offset: offset, Limit: ftpOrphanUserPage, Sort: "username", Order: "asc"})
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			if u.Username != nil && *u.Username != "" && !seen[*u.Username] {
				seen[*u.Username] = true
				out = append(out, *u.Username)
			}
		}
		if len(users) < ftpOrphanUserPage {
			return out, nil
		}
	}
}

func ftpOrphanKeys(m map[uint32]int64) []uint32 {
	keys := make([]uint32, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
