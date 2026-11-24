package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// KratosIdentityWriter is the subset of kratosclient.Client that BootstrapAdmin
// needs. Defined here (not in kratosclient) per Go idiom — interfaces live with
// the consumer, not the producer. Tests pass a fake implementation; production
// passes a real *kratosclient.Client.
type KratosIdentityWriter interface {
	CreateIdentityWithPassword(ctx context.Context, traits kratosclient.AdminTraits, passwordHash string) (string, error)
	DeleteIdentity(ctx context.Context, identityID string) error
}

// BootstrapOptions captures the inputs to BootstrapAdmin.
type BootstrapOptions struct {
	Email      string
	Password   string
	BcryptCost int
	// Username is the login username the installer was given
	// (JABALI_BOOTSTRAP_ADMIN_USERNAME). Empty, or not a valid username:
	// derived from Email's local part, as before.
	Username string

	// Kratos, when non-nil, makes BootstrapAdmin atomically create a Kratos
	// identity alongside the panel user row. On any failure the created side
	// is rolled back so panel + Kratos can't drift. Pass nil for legacy auth
	// mode — BootstrapAdmin then behaves exactly as it did pre-M20.
	//
	// Matches the API user-create hook in internal/api/users.go (see
	// cfg.kratosEnabled) so boot-time and runtime paths share the same
	// atomic-create invariant — ADR-0003 ("one write path = the API")
	// extends to bootstrap.
	Kratos KratosIdentityWriter
}

// BootstrapResult tells the caller whether an admin was created, skipped,
// or already existed.
type BootstrapResult struct {
	Created          bool   // true if a new admin row was written
	UserID           string // panel users.id of the bootstrapped (or existing) admin
	ExistingID       string // set when an admin already existed (not our insert)
	SkippedEmpty     bool   // true when Email/Password are blank — no-op
	KratosIdentityID string // when Kratos != nil and Created == true, the new identity UUID
}

// BootstrapAdmin creates a single admin user when none exists. Callable
// idempotently on every boot:
//
//   - no env vars set           → SkippedEmpty
//   - admin already exists      → ExistingID populated
//   - no admin exists           → Created=true, new row written
//
// Never updates an existing admin's password — explicit by design so a leaked
// env var can't silently overwrite the live operator credential.
//
// When opt.Kratos is non-nil, also creates a Kratos identity atomically:
//   - panel row insert
//   - Kratos identity create (bcrypt passthrough, same hash as the panel row)
//   - panel row update with kratos_identity_id
//
// Any failure rolls back the prior step(s) so the two systems can't drift. If
// Kratos create fails we delete the panel row; if the second panel update
// fails we delete the Kratos identity and the panel row. Best-effort unwind
// — if a rollback itself fails we log so the operator sees the orphan and
// return the original error so the caller sees a clear signal.
func BootstrapAdmin(ctx context.Context, users repository.UserRepository, opt BootstrapOptions) (BootstrapResult, error) {
	if opt.Email == "" || opt.Password == "" {
		return BootstrapResult{SkippedEmpty: true}, nil
	}

	// Heuristic: an admin exists if we can find one by the given email
	// with is_admin=true. For v1 we don't enumerate the full user list —
	// a simple FindByEmail is sufficient and avoids leaking user counts.
	if u, err := users.FindByEmail(ctx, opt.Email); err == nil {
		if u.IsAdmin {
			existing := BootstrapResult{ExistingID: u.ID, UserID: u.ID}
			if u.KratosIdentityID != nil {
				existing.KratosIdentityID = *u.KratosIdentityID
			}
			return existing, nil
		}
		return BootstrapResult{}, fmt.Errorf("auth: user %q exists but is not admin; refusing to upgrade", opt.Email)
	} else if !errors.Is(err, repository.ErrNotFound) {
		return BootstrapResult{}, fmt.Errorf("auth: lookup admin: %w", err)
	}

	hash, err := HashPassword(opt.Password, opt.BcryptCost)
	if err != nil {
		return BootstrapResult{}, err
	}

	now := time.Now().UTC()
	// M54: username is the login identifier under the v2 schema, so the
	// bootstrap admin must have one (derived from the email local-part).
	// Harmless under the old schema where username is optional.
	adminUsername := deriveBootstrapUsername(opt.Email)
	if opt.Username != "" {
		if bootstrapUsernameValid.MatchString(opt.Username) {
			adminUsername = opt.Username
		} else {
			// Never fail the boot over it: a panel that can't start has no admin.
			slog.Warn("bootstrap: admin username is not a valid username; using the one derived from the email",
				"given", opt.Username, "using", adminUsername)
		}
	}
	u := &models.User{
		ID:           ids.NewULID(),
		Email:        opt.Email,
		Username:     &adminUsername,
		PasswordHash: hash,
		IsAdmin:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := users.Create(ctx, u); err != nil {
		return BootstrapResult{}, fmt.Errorf("auth: create admin: %w", err)
	}

	// M20 LEGACY: remove after 2026-05-20 — legacy bootstrap path
	if opt.Kratos == nil {
		return BootstrapResult{Created: true, UserID: u.ID}, nil
	}

	// Kratos-aware path. Mirrors the compensating transaction in
	// internal/api/users.go so bootstrap + API share one invariant.
	traits := kratosclient.AdminTraits{
		Email:    u.Email,
		Username: adminUsername,
		IsAdmin:  u.IsAdmin,
	}
	identityID, err := opt.Kratos.CreateIdentityWithPassword(ctx, traits, u.PasswordHash)
	// ErrIdentityExisted (Kratos 409) returns a VALID existing identity
	// id — Kratos already had an identity for these traits (stale
	// bootstrap env after a hostname change, migration rerun, or a
	// prior destroy that left the Kratos row behind). Adopt it: stamp
	// the panel row with the existing id instead of rolling back and
	// crash-looping the panel on every boot. Idempotent. Incident
	// 2026-05-17 (mx): JABALI_BOOTSTRAP_ADMIN_EMAIL=admin@jabali.local
	// lingered while the real admin was admin@jabali.com → panel-api
	// exit 1 every boot → 502 until the env was hand-fixed.
	if err != nil && !errors.Is(err, kratosclient.ErrIdentityExisted) {
		if delErr := users.Delete(ctx, u.ID); delErr != nil {
			slog.Error("bootstrap: kratos create failed AND panel rollback also failed — orphan panel row",
				"user_id", u.ID, "email", u.Email, "kratos_err", err, "rollback_err", delErr)
		}
		return BootstrapResult{}, fmt.Errorf("auth: create kratos identity: %w", err)
	}

	// LinkKratosIdentity is the narrow write path — Update() deliberately
	// excludes kratos_identity_id from its column allowlist, so using it
	// here would silently drop the write.
	u.KratosIdentityID = &identityID
	if err := users.LinkKratosIdentity(ctx, u.ID, identityID); err != nil {
		if delErr := opt.Kratos.DeleteIdentity(ctx, identityID); delErr != nil {
			slog.Error("bootstrap: panel link failed AND kratos rollback also failed — orphan identity",
				"user_id", u.ID, "identity_id", identityID, "link_err", err, "rollback_err", delErr)
		}
		if delErr := users.Delete(ctx, u.ID); delErr != nil {
			slog.Error("bootstrap: panel link failed AND panel rollback also failed — orphan row",
				"user_id", u.ID, "link_err", err, "rollback_err", delErr)
		}
		return BootstrapResult{}, fmt.Errorf("auth: link kratos identity: %w", err)
	}

	return BootstrapResult{Created: true, UserID: u.ID, KratosIdentityID: identityID}, nil
}

var bootstrapUsernameInvalid = regexp.MustCompile(`[^a-z0-9_-]`)

// bootstrapUsernameValid is the users API's username rule (api.usernameRe).
var bootstrapUsernameValid = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// deriveBootstrapUsername turns the admin email into a schema-valid username
// (local-part, lowercased, non [a-z0-9_-] -> _, starts [a-z_], max 32). Same
// derivation as `jabali admin backfill-usernames` so a re-run agrees. On a
// fresh DB the first admin has no collisions to resolve.
func deriveBootstrapUsername(email string) string {
	local := email
	if i := strings.IndexByte(email, '@'); i > 0 {
		local = email[:i]
	}
	local = bootstrapUsernameInvalid.ReplaceAllString(strings.ToLower(local), "_")
	local = strings.Trim(local, "-")
	if local == "" {
		local = "admin"
	}
	if c := local[0]; !(c == '_' || (c >= 'a' && c <= 'z')) {
		local = "u" + local
	}
	if len(local) > 32 {
		local = local[:32]
	}
	return local
}
