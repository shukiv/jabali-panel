package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailgroupops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileMailGroups (GH #1818 / #1834) re-applies every DISTRIBUTION mail
// group to Stalwart from the DB. It is both the fleet conversion and the heal:
//
//   - Existing boxes projected a distribution group as a Group account, which
//     parks mail in the group's own inbox. The first pass after `jabali update`
//     re-applies each group with its member list, and the agent converts it to
//     a mailing list (copying any stored mail to the members first).
//   - The HTTP/CLI applies are fire-and-forget, and a member's own changes
//     (disabled, send-only, renamed, deleted) change a list's recipients
//     without touching the group. Hashing the full apply payload catches all of
//     these on the next tick.
//
// Groups on a domain with mail turned off are skipped.
//
// Resource groups are not reconciled here: their Group-account projection and
// memberGroupIds edges are pushed by the HTTP/CLI paths (see the mailgroup.apply
// note in shared_resource_reconcile.go).
//
// Steady state is a map compare per group. A group is re-applied when its
// payload changes, when its last success is older than
// mailGroupReDispatchInterval (heals drift on the mail server), or when its
// last attempt failed and mailGroupRetryInterval has passed. At most
// mailGroupApplyBudgetPerTick groups are applied per tick; the rest follow on
// later ticks; the pass also stops starting applies once mailGroupTickBudget
// of wall time is spent. The cache is process-local, so a restart re-applies every group
// once — each apply is idempotent on the agent side.

const (
	mailGroupReDispatchInterval = 15 * time.Minute
	mailGroupRetryInterval      = 5 * time.Minute
	mailGroupApplyBudgetPerTick = 25
	// mailGroupApplyTimeout is generous because converting a legacy group
	// copies its stored mail into every member's inbox.
	mailGroupApplyTimeout = 2 * time.Minute
)

// mailGroupTickBudget bounds one pass by wall time: once spent, no new apply
// starts and the remaining groups follow on later ticks, so a slow conversion
// cannot stall the rest of ReconcileAll. A var so tests can shrink it.
var mailGroupTickBudget = 2 * time.Minute

type mailGroupDoneEntry struct {
	hash string
	at   time.Time
	ok   bool
}

// WithMailGroups wires the GH #1818 distribution-group reconcile pass. nil
// disables it.
func (r *Reconciler) WithMailGroups(groups repository.MailGroupRepository) *Reconciler {
	r.mailGroups = groups
	return r
}

func (r *Reconciler) reconcileMailGroups(ctx context.Context) {
	if r.agent == nil || r.mailGroups == nil || r.serverSettings == nil {
		return
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.settingsGet(sctx)
	scancel()
	if err != nil || srv == nil || !srv.MailEnabled {
		return
	}

	groups, err := r.mailGroups.ListAllWithDomain(ctx)
	if err != nil {
		r.log.Warn("mail-groups: list groups failed", "error", err)
		return
	}
	members, err := r.mailGroups.ListAllDeliverableMembers(ctx)
	if err != nil {
		r.log.Warn("mail-groups: list members failed", "error", err)
		return
	}
	byGroup := make(map[string][]string)
	for _, m := range members {
		byGroup[m.GroupID] = append(byGroup[m.GroupID], m.Email)
	}

	r.mailGroupMu.Lock()
	if r.mailGroupDone == nil {
		r.mailGroupDone = make(map[string]mailGroupDoneEntry)
	}
	r.mailGroupMu.Unlock()

	now := time.Now()
	deadline := now.Add(mailGroupTickBudget)
	applied := 0
	for i := range groups {
		g := &groups[i].MailGroup
		if g.GroupKind != mailgroupops.KindDistribution || g.EmailCached == "" {
			continue
		}
		// Mail turned off for the domain (soft disable or the mail-only purge)
		// keeps the group rows; re-applying would re-create the Stalwart
		// domain and list the admin just removed.
		if !groups[i].DomainEmailEnabled {
			continue
		}
		if applied >= mailGroupApplyBudgetPerTick || time.Now().After(deadline) {
			return // the rest are applied on following ticks
		}
		params := mailgroupops.BuildApplyParams(g, byGroup[g.ID])
		want := mailGroupFingerprint(params)

		r.mailGroupMu.Lock()
		e, seen := r.mailGroupDone[g.ID]
		r.mailGroupMu.Unlock()
		if seen && e.hash == want {
			age := now.Sub(e.at)
			if (e.ok && age < mailGroupReDispatchInterval) || (!e.ok && age < mailGroupRetryInterval) {
				continue
			}
		}

		cctx, cancel := context.WithTimeout(ctx, mailGroupApplyTimeout)
		_, err := r.agent.Call(cctx, "mailgroup.apply", params)
		cancel()
		applied++
		if err != nil {
			r.log.Warn("mail-groups: apply failed", "group", g.EmailCached, "error", err)
		}
		r.mailGroupMu.Lock()
		r.mailGroupDone[g.ID] = mailGroupDoneEntry{hash: want, at: now, ok: err == nil}
		r.mailGroupMu.Unlock()
	}
}

// mailGroupFingerprint hashes the apply payload (encoding/json sorts map keys,
// and the member list arrives sorted from the repository).
func mailGroupFingerprint(params map[string]any) string {
	b, _ := json.Marshal(params)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}
