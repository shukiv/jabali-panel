package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// cliUserInput covers the one-shot fields `jabali user create` accepts. It
// intentionally mirrors the HTTP `createUserRequest` shape in
// internal/api/users.go — if a flag exists here, the API has it too. No
// --skip-provision yet because the HTTP handler's skip-provision path is
// admin-test-only and CLI operators always want OS provisioning.
type cliUserInput struct {
	Username  string
	Email     string
	Password  string
	NameFirst string
	NameLast  string
	IsAdmin   bool
}

// createUserDirect creates a panel user row + (optionally) a Kratos identity
// + (optionally) an OS user via the agent, using the same compensating
// transaction as internal/api/users.go but without the HTTP round-trip.
//
// This exists because M20 switched the API middleware to Kratos cookies, so
// the CLI's legacy-JWT mintCLIToken path can no longer reach /api/v1/users.
// Operators who want to drive user creation from a root shell (install
// scripts, recovery tooling) need a path that doesn't require a browser
// session. Direct-DB + shared helpers keeps the invariants the HTTP handler
// already enforces (ADR-0003: one write path — this path and the HTTP path
// both land in the same Kratos/agent call sequence).
//
// Returns the created user + a non-fatal provisioning warning if the OS
// user.create agent call failed (panel row + Kratos identity are kept —
// operator can retry provisioning).
// resolveCreateIdentity works out the login username (nil for an admin created
// without an explicit username) and the email to store, from the operator's
// --username / --email inputs. Login uses the USERNAME only — the Kratos schema
// marks `username` as the password identifier — so an email is optional and is
// synthesized as <username>@<panelHost> when omitted (the users.email column is
// NOT NULL and email is a Kratos trait). Pure (panelHost injected) for testing.
func resolveCreateIdentity(rawUsername, rawEmail string, isAdmin bool, panelHost string) (*string, string, error) {
	username := strings.TrimSpace(rawUsername)
	email := strings.TrimSpace(rawEmail)
	if username == "" && email == "" {
		return nil, "", fmt.Errorf("provide --username (the login name); --email is optional")
	}
	// Backward compat: derive a username from the email when only an email was
	// given for a regular user. Admins own no /home/<user>, so they may stay
	// username-less unless one is passed explicitly.
	if username == "" && !isAdmin {
		username = cliLinuxUserFromEmail(email)
		if username == "" {
			return nil, "", fmt.Errorf("could not derive a username from email %q — pass --username explicitly", email)
		}
	}
	var effectiveUsername *string
	if username != "" {
		if !cliValidUsername(username) {
			return nil, "", fmt.Errorf("username %q is not a valid POSIX name (start with a lowercase letter; lowercase letters, digits, - and _; max 32 chars)", username)
		}
		effectiveUsername = &username
	}
	if email == "" {
		// Synthesised placeholder (login never uses email). Guarantee a valid
		// dotted form: cliValidEmail rejects a bare-hostname domain, so fall
		// back to localhost.localdomain when the panel host has no dot.
		host := panelHost
		if !strings.Contains(host, ".") {
			host = "localhost.localdomain"
		}
		email = username + "@" + host
	} else if !cliValidEmail(email) {
		return nil, "", fmt.Errorf("email %q is not a valid format (need user@domain.tld)", email)
	}
	return effectiveUsername, email, nil
}

func createUserDirect(ctx context.Context, in cliUserInput) (*models.User, string, error) {
	if err := initConfig(); err != nil {
		return nil, "", err
	}
	if err := initDB(); err != nil {
		return nil, "", err
	}
	if err := initAgent(); err != nil {
		return nil, "", err
	}

	if in.Password == "" {
		return nil, "", fmt.Errorf("--password is required")
	}
	if len(in.Password) < 10 {
		return nil, "", fmt.Errorf("password must be at least 10 characters")
	}

	effectiveUsername, email, err := resolveCreateIdentity(in.Username, in.Email, in.IsAdmin, sharedCfg.Server.Hostname)
	if err != nil {
		return nil, "", err
	}

	users := userRepo()

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", fmt.Errorf("hash password: %w", err)
	}

	u := &models.User{
		ID:           ids.NewULID(),
		Email:        email,
		Username:     effectiveUsername,
		NameFirst:    in.NameFirst,
		NameLast:     in.NameLast,
		PasswordHash: string(hash),
		IsAdmin:      in.IsAdmin,
	}
	if err := users.Create(ctx, u); err != nil {
		return nil, "", fmt.Errorf("create user row: %w", err)
	}

	// M20: atomic Kratos identity. Compensating delete on failure so retries
	// don't hit a unique-email conflict from a ghost panel row.
	if sharedCfg.Auth.Kratos.PublicURL != "" {
		k := kratosclient.NewClient(sharedCfg.Auth.Kratos.PublicURL, sharedCfg.Auth.Kratos.AdminURL)
		traits := kratosclient.AdminTraits{Email: u.Email, IsAdmin: u.IsAdmin}
		if u.Username != nil {
			traits.Username = *u.Username
		}
		identityID, err := k.CreateIdentityWithPassword(ctx, traits, u.PasswordHash)
		if err != nil {
			if delErr := users.Delete(ctx, u.ID); delErr != nil {
				slog.Error("cli create: kratos failed AND panel rollback failed — orphan row",
					"user_id", u.ID, "email", u.Email, "kratos_err", err, "rollback_err", delErr)
			}
			return nil, "", fmt.Errorf("create kratos identity: %w", err)
		}
		u.KratosIdentityID = &identityID
		if err := users.LinkKratosIdentity(ctx, u.ID, identityID); err != nil {
			if delErr := k.DeleteIdentity(ctx, identityID); delErr != nil {
				slog.Error("cli create: panel link failed AND kratos rollback failed — orphan identity",
					"identity_id", identityID, "link_err", err, "rollback_err", delErr)
			}
			if delErr := users.Delete(ctx, u.ID); delErr != nil {
				slog.Error("cli create: panel link failed AND panel rollback failed",
					"user_id", u.ID, "rollback_err", delErr)
			}
			return nil, "", fmt.Errorf("link kratos identity: %w", err)
		}
	}

	// Best-effort OS user provisioning. Same semantics as the HTTP handler:
	// failure is non-fatal — panel + Kratos rows stay, caller sees a warning.
	var warning string
	if sharedAgent != nil && !in.IsAdmin && effectiveUsername != nil {
		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := sharedAgent.Call(agentCtx, "user.create", map[string]any{
			"username": *effectiveUsername,
			"home_dir": "/home/" + *effectiveUsername,
			"shell":    "/bin/bash",
			"password": in.Password,
		}); err != nil {
			warning = "user saved but OS provisioning failed: " + err.Error()
		}
	}

	return u, warning, nil
}

// cliDomainInput — same scoping logic as cliUserInput. Admins can't own
// domains (same 400 as the HTTP handler).
type cliDomainInput struct {
	Name    string
	UserID  string // ULID of the owning (non-admin) user
	DocRoot string // optional — defaults to /home/<user>/domains/<name>/public_html
	// ReverseProxy (GH #1175): allocate a loopback port and make this a
	// reverse-proxy domain (no docroot/PHP). Mirrors the HTTP create path.
	ReverseProxy bool
}

// createDomainDirect replicates the non-auth side of internal/api/domains.go
// create handler: owner must exist + be non-admin + have a username + pass
// package quota. On success the caller should trigger a reconcile tick so
// the nginx vhost materialises.
//
// Second return is a slice of soft warnings (DNS autoconfig conflicts, the
// agent being unavailable so email couldn't auto-enable, etc.) that the CLI
// front-end prints to stderr. The domain itself is created regardless; only
// hard errors (row insert conflict, bad input) return err != nil.
func createDomainDirect(ctx context.Context, in cliDomainInput) (*models.Domain, []string, error) {
	if err := initConfig(); err != nil {
		return nil, nil, err
	}
	if err := initDB(); err != nil {
		return nil, nil, err
	}

	if in.Name == "" || in.UserID == "" {
		return nil, nil, fmt.Errorf("--name and --user are required")
	}
	if err := validateDomainName(in.Name); err != nil {
		return nil, nil, err
	}

	domains := domainRepoFromDB()
	packages := packageRepoFromDB()

	// Accept email / username / ULID — same resolver as the other user-
	// facing CLIs so operators don't have to copy-paste ULIDs.
	owner, err := resolveUser(ctx, in.UserID)
	if err != nil {
		return nil, nil, err
	}
	if owner.IsAdmin {
		return nil, nil, fmt.Errorf("admin users cannot host domains — create a regular user")
	}
	if owner.Username == nil || *owner.Username == "" {
		return nil, nil, fmt.Errorf("user %q has no username — inconsistent state", owner.ID)
	}

	// All subsequent DB ops use the resolved ULID, not the free-form
	// spec the operator passed — email / username lookups land here via
	// resolveUser and d.UserID must always be the real ID.
	ownerID := owner.ID

	// Package-quota check — matches the HTTP handler (409 domain_quota_exceeded).
	if owner.PackageID != nil && *owner.PackageID != "" {
		count, err := domains.CountByUserID(ctx, ownerID)
		if err != nil {
			return nil, nil, fmt.Errorf("count existing domains: %w", err)
		}
		pkg, err := packages.FindByID(ctx, *owner.PackageID)
		if err == nil && pkg.MaxDomains > 0 && count >= int64(pkg.MaxDomains) {
			return nil, nil, fmt.Errorf("package quota exceeded: %d/%d domains", count, pkg.MaxDomains)
		}
	}

	docRoot := in.DocRoot
	if docRoot == "" {
		docRoot = "/home/" + *owner.Username + "/domains/" + in.Name + "/public_html"
	}

	now := time.Now().UTC()
	d := &models.Domain{
		ID:         ids.NewULID(),
		UserID:     ownerID,
		Name:       in.Name,
		DocRoot:    docRoot,
		IsEnabled:  true,
		SSLEnabled: true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	// GH #1175: reverse-proxy domains draw a loopback port from the shared
	// allocator BEFORE the insert (owner_id = the ULID above), released if the
	// insert then fails so a crash can't leak the reservation. Same pool + code
	// as the HTTP create path (repository.AllocateReverseProxy).
	ports := repository.NewPortAllocationRepository(sharedDB)
	if in.ReverseProxy {
		port, aerr := ports.AllocateReverseProxy(ctx, d.ID)
		if aerr != nil {
			return nil, nil, fmt.Errorf("allocate reverse-proxy port: %w", aerr)
		}
		d.ReverseProxyPort = uint32(port)
	}

	if err := domains.Create(ctx, d); err != nil {
		if in.ReverseProxy {
			_ = ports.Release(ctx, models.PortOwnerReverseProxy, d.ID)
		}
		if errors.Is(err, repository.ErrConflict) {
			return nil, nil, fmt.Errorf("domain %q already exists", in.Name)
		}
		return nil, nil, fmt.Errorf("create domain row: %w", err)
	}

	// Auto-enable email. Best-effort — if the agent's down or Stalwart
	// refuses the domain name, we record the reason as a soft warning
	// and the operator can retry via `jabali domain email-enable <name>`
	// or the Email tab in the UI.
	var warnings []string
	if err := initAgent(); err != nil {
		warnings = append(warnings, fmt.Sprintf("email auto-enable skipped: agent unavailable (%v)", err))
	} else {
		deps := newDomainEmailDepsFromGlobals()
		_, dnsWarnings, err := enableDomainEmailDirect(ctx, deps, d)
		if err != nil {
			warnings = append(warnings,
				fmt.Sprintf("email auto-enable failed (can retry with `jabali domain email-enable %s`): %v", d.Name, err))
		} else {
			warnings = append(warnings, dnsWarnings...)
		}
	}

	// The reconciler picks up the new row within 60s (default interval). No
	// inline nginx call — matches the HTTP handler, keeps ADR-0013's inline
	// best-effort pattern confined to users.
	return d, warnings, nil
}

// ---------- local username helpers (duplicated from internal/api to avoid exporting) ----------
// The originals live in internal/api/users.go as unexported lowercase
// helpers. Duplicated here because exporting them for a CLI-only caller
// would widen the API surface for one consumer — copy is cheaper.

var cliUsernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func cliValidUsername(s string) bool { return cliUsernameRe.MatchString(s) }

// cliEmailRe — pragmatic email shape: local-part allowed alphanumerics
// + . _ % + -, exactly one '@', domain must match cliDomainNameRe shape.
// Stricter than RFC 5322 but covers ≥99% of real addresses without a
// 6KB regex; downstream Kratos applies its own validation if this passes.
var cliEmailRe = regexp.MustCompile(
	`^[A-Za-z0-9._%+\-]+@(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

func cliValidEmail(s string) bool {
	return len(s) <= 320 && cliEmailRe.MatchString(s)
}

// cliDomainNameRe enforces the structural shape of a hostable domain
// at the CLI surface. Rules (intentionally narrower than RFC 1035):
//
//   - Two or more labels separated by '.', total ≤253 chars.
//   - Each label 1..63 chars, alphanumeric + hyphen, no leading/trailing
//     hyphen.
//   - Final label (TLD) ≥2 chars, all letters — rejects bare hostnames
//     ("invalid"), IP literals ("192.168.1.1"), and numeric "TLDs".
//   - No spaces (operators forgetting to quote get a clear error
//     instead of a silently-truncated domain row).
//
// Stricter validation (TLD allowlist, .local handling) lives downstream
// in stalwart / DNS layers; this CLI gate just rejects nonsense early.
var cliDomainNameRe = regexp.MustCompile(
	`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

func validateDomainName(s string) error {
	if strings.ContainsAny(s, " \t\n") {
		return fmt.Errorf("domain %q contains whitespace — quote the value if it has special chars", s)
	}
	if len(s) > 253 {
		return fmt.Errorf("domain %q exceeds 253 chars", s)
	}
	if !cliDomainNameRe.MatchString(s) {
		return fmt.Errorf("domain %q is not a valid FQDN (need at least two labels and a 2+ letter TLD; bare hostnames + IP addresses are rejected)", s)
	}
	return nil
}

func cliLinuxUserFromEmail(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return ""
}
