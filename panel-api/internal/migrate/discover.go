package migrate

import "context"

// Discoverer is the read-only contract every per-source importer
// implements. The connect → list → describe sequence runs without
// mutating the source panel; restore-side mutations live in the
// per-source Restorer (see restore.go in each importer package).
type Discoverer interface {
	// Connect opens a session to the source. Implementations should
	// validate credentials with a single read call (e.g. cPanel UAPI
	// `Variables::get_user_information`) before returning, so
	// downstream callers can surface auth errors early.
	Connect(ctx context.Context, host, user string, secret SecretRef) (Session, error)

	// ListAccounts returns the account headers the connected
	// principal can see. Reseller / WHM principals see many; a
	// single-account principal sees one.
	ListAccounts(ctx context.Context, s Session) ([]AccountSummary, error)

	// DescribeAccount produces the AccountManifest for one source-
	// side account. Idempotent + cacheable: re-running on the same
	// source returns the same manifest modulo size deltas.
	DescribeAccount(ctx context.Context, s Session, accountID string) (*AccountManifest, error)

	// Close releases the session. Importers should treat this as
	// best-effort — the supervisor unit kills any leaked SSH
	// children on terminal state regardless.
	Close(ctx context.Context, s Session) error
}

// SizeProber is the optional capability for Discoverers that can
// answer "how big is account X" cheaply (single du -sh / quota
// lookup). Callers type-assert disc.(SizeProber); when not
// implemented the lazy size endpoint returns 501. ADR-0095 decision 6.
type SizeProber interface {
	AccountSize(ctx context.Context, s Session, login string) (int64, error)
}

// AllowPrivateSetter is the optional capability for Discoverers that
// honor the server_settings.migration_allow_private_hosts toggle. All
// shipped Discoverers implement this; the helper ApplyAllowPrivate
// type-asserts so callers don't have to do a per-kind switch.
type AllowPrivateSetter interface {
	SetAllowPrivate(bool)
}

// ApplyAllowPrivate sets the SSRF private-IP override on a freshly-
// constructed Discoverer when it implements AllowPrivateSetter. No-op
// otherwise — keeps the call site identical regardless of which kind
// migrate.Get returned. Pull every-call-site loads server_settings +
// passes the value here so toggling the DB column takes effect on the
// next operation without a process restart.
func ApplyAllowPrivate(d Discoverer, allow bool) {
	if s, ok := d.(AllowPrivateSetter); ok {
		s.SetAllowPrivate(allow)
	}
}

// PortSetter is the optional capability for Discoverers that connect over a
// configurable SSH port (GH #429). Mirrors AllowPrivateSetter so the
// registry-backed admin call sites can apply job.source_port without a
// per-kind switch.
type PortSetter interface {
	SetPort(int)
}

// ApplyPort sets the source SSH port on a Discoverer obtained from the
// registry when it implements PortSetter. No-op otherwise. Ports outside
// 1..65535 (including 0) are ignored so the Discoverer keeps its default 22.
func ApplyPort(d Discoverer, port int) {
	if port < 1 || port > 65535 {
		return
	}
	if s, ok := d.(PortSetter); ok {
		s.SetPort(port)
	}
}

// Session is an opaque per-importer connection handle. Concrete
// implementations live under internal/migrate/<kind>/.
type Session interface {
	// Kind returns the source kind ("cpanel", "directadmin", ...)
	// for log + error context. Useful when dispatching to the
	// right Discoverer impl from generic code.
	Kind() string
}

// AccountSummary is the row shown in the admin UI's account-picker.
// Cheap to compute (no per-account UAPI calls beyond list-mode).
type AccountSummary struct {
	ID         string `json:"id"`         // source-side account ID
	Login      string `json:"login"`      // source-side username
	Email      string `json:"email,omitempty"`
	Domain     string `json:"domain,omitempty"`     // primary domain
	BytesTotal int64  `json:"bytes_total"`          // best-effort summary
	Suspended  bool   `json:"suspended,omitempty"`
}

// SecretRef points at a credential file the importer reads at
// Connect time. Plaintext secrets never live in process memory
// past the auth call; see the per-source Connect implementations
// for the read-once-then-zero pattern.
type SecretRef struct {
	// Path is /etc/jabali-panel/migration-secrets/<job-id>.env
	// (root:jabali 0640) per ADR-0094 §"tracked risks".
	Path string
	// ExpectedHostKey is an optional admin-supplied SHA256 fingerprint of the
	// source SSH host key (Gitea #461). When set, the connector verifies the
	// presented key against it (fail-hard on mismatch); when empty, the
	// connector trusts on first use and pins the key for later stages.
	ExpectedHostKey string
}
