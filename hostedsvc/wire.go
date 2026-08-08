package hostedsvc

// Wire shapes for the v1 API. The fixtures in testdata/ pin these Go structs
// so a server-side field change breaks a test, not a running installer. NOTE:
// the phase-3 panel-side client is bash (install/hostname/jabali-hostname.sh),
// so it does NOT consume these fixtures — the contract is enforced only on the
// server side here. If a typed client is added later, embed the same fixtures
// there to restore the bidirectional agentwire-style guarantee.

type RegisterRequest struct {
	Email string `json:"email"`
}

type RegisterResponse struct {
	Ok bool `json:"ok"`
}

type ClaimRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type ClaimResponse struct {
	Label string `json:"label"`
	FQDN  string `json:"fqdn"`
	// Token is the bearer secret for every later call. Shown once; the
	// panel stores it at 0600 under /etc/jabali-panel/.
	Token string `json:"token"`
}

type TokenRequest struct {
	Token string `json:"token"`
}

type AcmePresentRequest struct {
	Token string `json:"token"`
	TXT   string `json:"txt"`
}

type HeartbeatResponse struct {
	Ok bool `json:"ok"`
	// IPMoved tells the box its source address no longer matches its label;
	// it should re-claim from the new address (old label survives 7 days).
	IPMoved bool `json:"ip_moved,omitempty"`
}

type OkResponse struct {
	Ok bool `json:"ok"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}
