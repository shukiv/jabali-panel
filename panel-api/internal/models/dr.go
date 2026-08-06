package models

// DR / standby node roles (GH #331). A box is either an active primary or a
// one-way async standby replica that is manually promoted during a disaster.
// There is no automatic failover and no active/active — the operator pointing
// traffic at the standby is the fencing step.
const (
	ServerRolePrimary = "primary"
	ServerRoleStandby = "standby"
)

// IsStandby reports whether this box is a DR standby replica. Any value other
// than the explicit "standby" — including the empty string on an unseeded row —
// is treated as primary, so a missing/garbled role never wrongly parks a live
// box in read-only mode (fail safe toward "active primary").
func (s ServerSettings) IsStandby() bool { return s.ServerRole == ServerRoleStandby }

// IsPrimary is the inverse of IsStandby.
func (s ServerSettings) IsPrimary() bool { return !s.IsStandby() }

// IsValidServerRole validates an operator-supplied role. The empty string is
// accepted and means the primary default.
func IsValidServerRole(r string) bool {
	switch r {
	case "", ServerRolePrimary, ServerRoleStandby:
		return true
	}
	return false
}

// DR standby sync outcomes (GH #331 Step 2). Recorded on server_settings by the
// drsync loop after each tick so `jabali dr status` and the admin banner can show
// how fresh the replica is. Empty (”) means the loop has never recorded a tick.
const (
	// DRSyncStatusOK — the loop applied a newer system_backup manifest this tick.
	DRSyncStatusOK = "ok"
	// DRSyncStatusCurrent — the newest manifest on the destination was already
	// applied; nothing to do.
	DRSyncStatusCurrent = "current"
	// DRSyncStatusWaiting — the DR destination holds no system_backup manifest
	// yet (the primary hasn't shipped one). Not an error — a fresh pairing.
	DRSyncStatusWaiting = "waiting"
	// DRSyncStatusError — the tick failed (destination unreachable, restore
	// error). DRLastSyncError carries the detail.
	DRSyncStatusError = "error"
)
