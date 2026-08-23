package auth

import "time"

// AccessClaims is the resolved identity for an authenticated request. It is
// populated by middleware (RequireKratosSession) from the Kratos whoami
// response + the panel users row, and stashed on the gin.Context via ginctx.
// Downstream handlers read it to answer "who is calling?" and enforce
// ownership/admin checks.
//
// The struct predates M20 — it used to embed jwt.RegisteredClaims because
// we minted our own JWTs. After M20 removed the legacy JWT surface the only
// fields anyone actually reads are UserID, Email, IsAdmin, so those are the
// only fields that live here now.
type AccessClaims struct {
	UserID  string
	Email   string
	IsAdmin bool
	// ImpersonatedBy is the real admin's user id when this request is an
	// act-as override (ADR-0128): UserID/Email/IsAdmin describe the TARGET
	// user the admin is operating as, while ImpersonatedBy stays the admin
	// so the audit trail records the real actor. Empty on normal requests.
	ImpersonatedBy string
	// Source identifies which middleware authenticated this request.
	// Empty defaults to SourceKratos (the historical browser-cookie
	// path) for backwards compatibility — old middleware that pre-
	// dates the user-API-token feature leaves Source unset.
	Source AuthSource
	// AuthenticatedAt is when the Kratos session behind this request was last
	// authenticated (JAB-380). Only populated for SourceKratos requests; zero
	// for API-token / automation-HMAC callers (which have no interactive
	// session). Recent-auth gates on the root File Manager + Root Terminal read
	// it to require a fresh login before privileged root actions.
	AuthenticatedAt time.Time
	// AAL is the Kratos assurance level ("aal1"/"aal2") of the session.
	// Captured for a future TOTP/passkey step-up requirement; not enforced yet.
	AAL string
}

// AuthSource is the kind of credential that produced the claims.
// Handlers that want to gate behaviour on it (e.g. audit-tag API-
// driven mutations, or reject token-auth on a admin-cookie-only
// route) read claims.Source.
type AuthSource int

const (
	// SourceKratos is the browser-cookie path (RequireKratosSession).
	// Zero value so unset == Kratos preserves pre-M51 semantics.
	SourceKratos AuthSource = iota
	// SourceUserAPIToken is a per-user Bearer jat_… token
	// (RequireUserAuth → authenticateUserAPIToken).
	SourceUserAPIToken
	// SourceAutomationHMAC is an admin-scoped HMAC-signed automation
	// API request (M44, RequireAutomationHMAC).
	SourceAutomationHMAC
)

// String renders the source for audit logs. Stable strings — never
// change without a migration of audit rows.
func (s AuthSource) String() string {
	switch s {
	case SourceUserAPIToken:
		return "user_api_token"
	case SourceAutomationHMAC:
		return "automation_hmac"
	default:
		return "kratos"
	}
}
