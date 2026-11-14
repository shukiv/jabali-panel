package limits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// GrandfatherHeadroomMB is the room a user already above its own share of the
// disk quota keeps when the share first applies (see TenantDiskQuotaMB).
const GrandfatherHeadroomMB = 64

// IsolatedShares is what a user's isolated FTP/SFTP accounts hold of its disk
// quota. Each such account has its own uid and its own quota, so the files it
// writes don't count against the user's uid; the user's own limit is the
// quota less these shares, or together they could use more than the quota.
type IsolatedShares struct {
	// SharesMB is the sum of the user's isolated accounts' quotas.
	SharesMB uint64
	// Host is the user's own quota on the host now, when known. With it, a
	// user already using more than its share keeps what it uses (see
	// TenantDiskQuotaMB); without it the share applies as is.
	Host *HostDisk
}

// HostDisk is one uid's quota on the host's quota mount, in 1 KiB blocks.
// LimitKB 0 means no limit.
type HostDisk struct {
	UsedKB  uint64
	LimitKB uint64
}

// TenantDiskQuotaMB is the disk limit for the user's own uid: the effective
// quota less the isolated accounts' shares. It is never 0 while a quota is
// set, since 0 is no limit to setquota.
//
// A user already using more than that when the share first applies (shares
// made before the limit counted them) is not cut off: it keeps its current
// usage plus GrandfatherHeadroomMB, never more than its current limit or the
// quota. That figure only ever goes down as the user frees space, until it
// reaches the share. So such a user can't grow past what it held, and a lower
// quota (a package change) still applies in full.
func (p LimitsPlan) TenantDiskQuotaMB() uint32 {
	quota := uint64(p.Effective.DiskQuotaMB)
	shares := p.Shares.SharesMB
	if quota == 0 || shares == 0 {
		return uint32(quota)
	}
	share := uint64(1)
	if shares < quota {
		share = quota - shares
	}
	h := p.Shares.Host
	if h == nil {
		return uint32(share)
	}
	keep := quota
	if cur := h.LimitKB / 1024; cur > 0 && cur < keep {
		keep = cur
	}
	if need := (h.UsedKB+1023)/1024 + GrandfatherHeadroomMB; need < keep {
		keep = need
	}
	if keep > share {
		return uint32(keep)
	}
	return uint32(share)
}

// ErrHostDiskPartial is returned by ReadHostDisks when the agent could not
// parse every row: a user missing from the answer may still have a quota.
var ErrHostDiskPartial = errors.New("limits: the host quota listing is partial")

// ReadHostDisks reads every user's quota on mount from the host, by name (one
// repquota through system.quota_inventory). It reads the kernel's quota state,
// so a user over its limit is listed like any other. A listing with rows the
// agent couldn't parse is returned with ErrHostDiskPartial: the users in it
// are right, a missing one is unknown.
func ReadHostDisks(ctx context.Context, ag AgentCaller, mount string) (map[string]HostDisk, error) {
	if mount == "" {
		return nil, errors.New("limits: no quota mount")
	}
	raw, err := ag.Call(ctx, "system.quota_inventory", map[string]any{"mount": mount})
	if err != nil {
		return nil, err
	}
	var v struct {
		Entries []struct {
			Username string `json:"username"`
			UsedKB   uint64 `json:"used_kb"`
			LimitKB  uint64 `json:"limit_kb"`
		} `json:"entries"`
		Partial bool `json:"partial"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("limits: decode quota inventory: %w", err)
	}
	out := make(map[string]HostDisk, len(v.Entries))
	for _, e := range v.Entries {
		out[e.Username] = HostDisk{UsedKB: e.UsedKB, LimitKB: e.LimitKB}
	}
	if v.Partial {
		return out, ErrHostDiskPartial
	}
	return out, nil
}

// HostDiskOf picks username out of a ReadHostDisks result (disks and its
// error). A user with no row has no usage and no limit on the mount, but only
// a complete listing says so: after any other error, or a partial listing
// without the user, ok is false.
func HostDiskOf(disks map[string]HostDisk, err error, username string) (HostDisk, bool) {
	if err != nil && !errors.Is(err, ErrHostDiskPartial) {
		return HostDisk{}, false
	}
	if d, ok := disks[username]; ok {
		return d, true
	}
	if err != nil {
		return HostDisk{}, false
	}
	return HostDisk{}, true
}
