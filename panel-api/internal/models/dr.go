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
