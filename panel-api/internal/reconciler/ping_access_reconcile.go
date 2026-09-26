package reconciler

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// WithPingAccess wires the GH #1798 ping-access pass. nil disables it.
func (r *Reconciler) WithPingAccess(repo repository.PingAccessRepository) *Reconciler {
	r.pingAccess = repo
	return r
}

// reconcilePingAccess keeps the Agent's jabali-ping group equal to the users
// whose hosting package allows ping (GH #1798). Members of that group, and
// only they, may open ICMP ping sockets. That is the only way ping works in
// the SSH sandbox, where no_new_privs ignores the cap_net_raw file capability
// on /usr/bin/ping. The Agent also keeps net.ipv4.ping_group_range on the
// group.
//
// Every user is considered, not only the users on the egress firewall: ping
// is a package allowance. For enforced and learning users the firewall has to
// allow it too (allow_ping in user.egress.apply).
//
// PhasePingAccess gates the call on the sorted list, so a steady tick sends
// nothing. A failed read skips the tick: an empty list would clear the group.
func (r *Reconciler) reconcilePingAccess(ctx context.Context) {
	if r.agent == nil || r.pingAccess == nil {
		return
	}
	members, err := r.pingAccess.ListPingAllowedUsernames(ctx)
	if err != nil {
		r.log.Warn("ping-access: list users failed; group left as is", "error", err)
		return
	}
	if members == nil {
		// Sent as [] (clear the group). The Agent refuses a null list.
		members = []string{}
	}
	sort.Strings(members)
	params := map[string]any{"members": members}

	_, _ = r.project(ctx, PhasePingAccess, "all", fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		raw, err := r.agent.Call(callCtx, "user.ping_access.apply", params)
		if err != nil {
			r.log.Warn("ping-access: agent apply failed; will retry next tick", "error", err)
			return err
		}
		var resp struct {
			Added   []string          `json:"added"`
			Removed []string          `json:"removed"`
			Refused map[string]string `json:"refused"`
		}
		if json.Unmarshal(raw, &resp) == nil {
			if len(resp.Added)+len(resp.Removed) > 0 {
				r.log.Info("ping-access: group updated", "added", resp.Added, "removed", resp.Removed)
			}
			if len(resp.Refused) > 0 {
				r.log.Warn("ping-access: users not added to the ping group", "refused", resp.Refused)
			}
		}
		return nil
	})
}
