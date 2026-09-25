package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainmailops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
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
		raw, err := sharedAgent.Call(agentCtx, "user.create", map[string]any{
			"username": *effectiveUsername,
			"home_dir": "/home/" + *effectiveUsername,
			// JAB-277 (security): the hardened Jabali SSH shell, matching the
			// REST + userops paths. The CLI used to provision /bin/bash, so a
			// CLI-created tenant got an UNRESTRICTED login shell — bypassing the
			// SSH sandbox (restricted shell + forwarding lockdown) every other
			// creation path enforces.
			"shell":    "/usr/local/bin/jabali-ssh-shell",
			"password": in.Password,
		})
		if err != nil {
			warning = "user saved but OS provisioning failed: " + err.Error()
		} else {
			// JAB-277 / JAB-33 parity: persist the provisioned OS UID so
			// users.linux_uid isn't left NULL for CLI-created accounts (egress
			// policy, quotas, and SFTP all map the panel row through linux_uid).
			// Best-effort; `u` is the fully-built row, so Update writes it back
			// unchanged plus the UID (no partial-model clobber — cf. JAB-280).
			var pr struct {
				UID int `json:"uid"`
			}
			if jErr := json.Unmarshal(raw, &pr); jErr == nil && pr.UID > 0 {
				uid := uint32(pr.UID)
				u.LinuxUID = &uid
				if uErr := users.Update(ctx, u); uErr != nil {
					slog.Warn("cli create: persist linux_uid failed", "user_id", u.ID, "uid", pr.UID, "err", uErr)
				}
			}
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
	// ReverseProxyPort (GH #1401): a specific loopback port to proxy to; 0 =
	// auto-assign from the pool. Validated the same as the HTTP path.
	ReverseProxyPort int
	// GH #1449: independent services. WebDisabled → docroot-less (DNS-only /
	// mail-only), DNSDisabled → external DNS. INVERTED so the zero value keeps
	// the historic full-service behaviour for any caller that doesn't set them.
	WebDisabled bool
	DNSDisabled bool
	// MailProvider (GH#181) — jabali (default) | none | m365 | google. Empty =
	// jabali. Lets the CLI create a DNS-only zone (--mail none) or a mail-only
	// domain (--web-enabled=false, mail jabali).
	MailProvider string
	// DNSTemplateID (GH #1627) is an admin-defined custom DNS template to seed
	// this domain's fresh zone from. When set it overrides the mail posture to
	// external ('custom'); it is mutually exclusive with an explicit --mail
	// provider and requires the panel to host DNS (--manage-dns). Empty = no
	// template. Mirrors the HTTP createDomainOp dns_template_id path.
	DNSTemplateID string
}

// createDomainDirect is the `jabali domain create` adapter over
// domainops.Create, the one create entrypoint the REST door also runs
// (JAB-279 AC1). The new row is converged by the reconciler's next tick.
//
// Second return is a slice of soft warnings (a shared-certificate lookup or
// attach failure, DNS autoconfig conflicts, the agent being unavailable so
// email couldn't auto-enable, etc.) that the CLI front-end prints to stderr.
// The domain itself is created regardless; only hard errors (row insert
// conflict, bad input) return err != nil.
func createDomainDirect(ctx context.Context, in cliDomainInput) (*models.Domain, []string, error) {
	if err := initConfig(); err != nil {
		return nil, nil, err
	}
	if err := initDB(); err != nil {
		return nil, nil, err
	}

	// JAB-279 / GH #884: canonicalize the name (lowercase + trim) through the
	// shared leaf BEFORE anything consumes it, mirroring the REST create path
	// which normalizes at its source of truth. jabali stores the name verbatim
	// for the docroot path, cert lineage, DNS zone, and nginx server_name, so a
	// mixed-case --name would stand up a site that never resolves AND persist a
	// different identity than REST/automation store for the same input (AC2:
	// same input -> same stored domain across adapters). Everything below
	// (validate, docroot default, insert) now sees the one canonical form.
	in.Name = domainops.NormalizeDomainName(in.Name)

	if in.Name == "" || in.UserID == "" {
		return nil, nil, fmt.Errorf("--name and --user are required")
	}

	// JAB-279 AC1: the create itself is domainops.Create — the same rules, in
	// the same order, that the REST door runs — so the CLI stores what REST
	// stores for the same input (AC2). This adapter supplies the CLI's stores,
	// resolves --user through resolveUser (email, username or ULID), and maps
	// each rejection to the CLI's own message.
	//
	// The CLI is an operator tool, so it acts as an admin: the document root is
	// held to the admin floor (anywhere under the owner's home, no ".."), and
	// the cross-tenant suffix guard, which trusts admins to place delegations,
	// does not run. The CLI takes no --ssl-mode, so the mode starts from Let's
	// Encrypt; a DNS-only domain is forced to none.
	owners := &cliOwnerFinder{}
	deps := domainops.CreateDeps{
		Domains:      domainRepoFromDB(),
		Users:        owners,
		Aliases:      repository.NewWebDomainAliasRepository(sharedDB),
		Packages:     packageRepoFromDB(),
		Settings:     serverSettingsRepoFromDB(),
		DNSTemplates: repository.NewDNSTemplateRepository(sharedDB),
		SharedCerts:  sharedCertRepoFromDB(),
		Ports:        repository.NewPortAllocationRepository(sharedDB),
	}
	// The port probe is only needed for an explicit port. Assign the agent only
	// when the pointer is set: a nil *agent.Client in the interface would pass
	// the module's nil check and panic on Call.
	if in.ReverseProxy && in.ReverseProxyPort != 0 && initAgent() == nil && sharedAgent != nil {
		deps.Agent = sharedAgent
	}
	res, err := domainops.Create(ctx, deps, domainops.CreateHooks{
		// No Schedule or InlineSSL: the CLI holds no in-process reconciler (the
		// JAB-355 cross-process limitation). The reconciler picks the new row up
		// within 60s (default interval) and bootstraps the certificate.
		EnableMail: cliEnableMail,
	}, cliCreateInput(in))
	if err != nil {
		return nil, nil, cliCreateError(err, in, owners.resolved)
	}
	return res.Domain, cliCreateWarnings(res), nil
}

// cliCreateInput is the create request `jabali domain create` makes. The CLI
// acts as an admin (see createDomainDirect) and has no flag for an SSL mode, a
// web template, a preview URL, www, apex IPs or the M365 / Google tokens, so
// those stay at their defaults.
func cliCreateInput(in cliDomainInput) domainops.CreateInput {
	return domainops.CreateInput{
		OwnerID:          in.UserID,
		Name:             in.Name,
		DocRoot:          in.DocRoot,
		ActorIsAdmin:     true,
		MailProvider:     in.MailProvider,
		DNSTemplateID:    in.DNSTemplateID,
		ReverseProxy:     in.ReverseProxy,
		ReverseProxyPort: in.ReverseProxyPort,
		WebDisabled:      in.WebDisabled,
		DNSDisabled:      in.DNSDisabled,
	}
}

// cliOwnerFinder resolves --user for domainops.Create through resolveUser, the
// resolver the other user-facing CLIs share (email, username or ULID), and
// keeps the resolved row for the adapter's messages.
type cliOwnerFinder struct{ resolved *models.User }

func (f *cliOwnerFinder) FindByID(ctx context.Context, spec string) (*models.User, error) {
	u, err := resolveUser(ctx, spec)
	if err == nil {
		f.resolved = u
	}
	return u, err
}

// cliAgentUnavailableError marks a mail enable skipped because the agent could
// not be reached.
type cliAgentUnavailableError struct{ err error }

func (e *cliAgentUnavailableError) Error() string { return e.err.Error() }
func (e *cliAgentUnavailableError) Unwrap() error { return e.err }

// cliEnableMail is the CLI's mail-enable hook. Best-effort: if the agent is
// down or Stalwart refuses the domain name, the reason becomes a soft warning
// and the operator can retry via `jabali domain email-enable <name>` or the
// Email tab in the UI; the reconciler also finishes the enable.
func cliEnableMail(ctx context.Context, d *models.Domain) ([]string, error) {
	if err := initAgent(); err != nil {
		return nil, &cliAgentUnavailableError{err: err}
	}
	_, _, warnings, err := domainmailops.Enable(ctx, newDomainEmailDepsFromGlobals(), d)
	if err != nil {
		return nil, err
	}
	return domainmailops.WarningMessages(warnings), nil
}

// cliCreateWarnings renders the soft results of a successful create as the
// warnings the CLI front-end prints to stderr.
func cliCreateWarnings(res *domainops.CreateResult) []string {
	var warnings []string
	d := res.Domain
	switch {
	case errors.Is(res.SharedCertErr, domainops.ErrSharedCertLookup):
		warnings = append(warnings, fmt.Sprintf("shared-certificate lookup skipped: %v", res.SharedCertErr))
	case errors.Is(res.SharedCertErr, domainops.ErrSharedCertAttach):
		warnings = append(warnings, fmt.Sprintf(
			"shared-certificate auto-attach failed (retry with `jabali ssl shared attach --domain %s --cert-id %s`): %v",
			d.Name, res.SharedCert.ID, res.SharedCertErr))
	}
	var unavailable *cliAgentUnavailableError
	switch {
	case errors.As(res.MailErr, &unavailable):
		warnings = append(warnings, fmt.Sprintf("email auto-enable skipped: agent unavailable (%v)", unavailable.err))
	case res.MailErr != nil:
		warnings = append(warnings,
			fmt.Sprintf("email auto-enable failed (can retry with `jabali domain email-enable %s`): %v", d.Name, res.MailErr))
	default:
		warnings = append(warnings, res.MailWarnings...)
	}
	return warnings
}

// cliCreateError maps a domainops.Create rejection to the message `jabali
// domain create` printed before the module owned the orchestration (JAB-279).
// owner is the resolved --user, or nil when the lookup did not succeed.
func cliCreateError(err error, in cliDomainInput, owner *models.User) error {
	var (
		aliasHit *domainops.AliasConflictError
		docRoot  *domainops.DocRootError
	)
	ownerID := ""
	if owner != nil {
		ownerID = owner.ID
	}
	switch {
	case errors.Is(err, domainops.ErrAliasLookup):
		// Fail closed: a lookup error aborts the create, never proceeds.
		return fmt.Errorf("verify domain name against existing aliases: %w", err)
	case errors.As(err, &aliasHit):
		return fmt.Errorf("the name %q is already used as an alias of another domain", aliasHit.Hostname)
	case errors.Is(err, domainops.ErrOwnerLookup):
		return err // resolveUser's own message
	case errors.Is(err, domainops.ErrAdminCannotHost):
		return fmt.Errorf("admin users cannot host domains — create a regular user")
	case errors.Is(err, domainops.ErrOwnerSuspended):
		return fmt.Errorf("user %q is suspended — unsuspend before adding domains", ownerID)
	case errors.Is(err, domainops.ErrOwnerNoUsername):
		return fmt.Errorf("user %q has no username — inconsistent state", ownerID)
	case errors.Is(err, domainops.ErrDomainQuotaExceeded), errors.Is(err, domainops.ErrDomainCount):
		return cliDomainQuotaError(err)
	case errors.Is(err, domainops.ErrWebOffReverseProxy):
		return fmt.Errorf("a reverse-proxy domain requires web hosting")
	case errors.Is(err, domainops.ErrWebOffDocRoot):
		return fmt.Errorf("a web-disabled domain has no document root")
	case errors.As(err, &docRoot):
		return fmt.Errorf("invalid --doc-root: %w", err)
	case errors.Is(err, domainops.ErrNoServiceSelected):
		return fmt.Errorf("select at least one service: web hosting (--web-enabled), DNS (--manage-dns), or mail (--mail)")
	case errors.Is(err, domainops.ErrReverseProxyPortInvalid):
		return err // the static policy's own reason
	case errors.Is(err, domainops.ErrReverseProxyPortSystemBound):
		return fmt.Errorf("port %d is already in use by a system service — choose another", in.ReverseProxyPort)
	case errors.Is(err, domainops.ErrReverseProxyPortInUse):
		return fmt.Errorf("port %d is already assigned to another domain — choose another", in.ReverseProxyPort)
	case errors.Is(err, domainops.ErrReverseProxyUnavailable), errors.Is(err, domainops.ErrReverseProxyPortUnavailable):
		return fmt.Errorf("allocate reverse-proxy port: %w", err)
	case errors.Is(err, domainops.ErrDomainExists):
		return fmt.Errorf("domain %q already exists", in.Name)
	case errors.Is(err, domainops.ErrPersist):
		return fmt.Errorf("create domain row: %w", err)
	}
	if nameErr := cliDomainNameError(err, in.Name); nameErr != nil {
		return nameErr
	}
	return cliMailPostureError(err, in)
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

// cliDomainQuotaError maps a domainops quota rejection to the message
// `jabali domain create` printed before the module owned the check (JAB-279).
func cliDomainQuotaError(err error) error {
	var quota *domainops.DomainQuotaError
	switch {
	case errors.As(err, &quota):
		return fmt.Errorf("package quota exceeded: %d/%d domains", quota.Count, quota.Max)
	case errors.Is(err, domainops.ErrDomainCount):
		return fmt.Errorf("count existing domains: %w", err)
	default:
		return err
	}
}

// cliMailPostureError maps a domainops mail-posture rejection to the message
// `jabali domain create` printed before the module owned the rules (JAB-279).
// A rejected provider is never empty (empty means Jabali), so in.MailProvider is
// the value the old inline check reported.
func cliMailPostureError(err error, in cliDomainInput) error {
	switch {
	case errors.Is(err, domainops.ErrMailProviderInvalid):
		return fmt.Errorf("invalid --mail %q (want jabali|none|m365|google)", in.MailProvider)
	case errors.Is(err, domainops.ErrMailProviderCustomReserved):
		return fmt.Errorf("invalid --mail %q (custom is the posture of a domain created from a DNS template; not selectable on the CLI)", in.MailProvider)
	case errors.Is(err, domainops.ErrDNSTemplateProviderExclusive):
		return fmt.Errorf("--dns-template sets the mail posture; do not also pass --mail %q", in.MailProvider)
	case errors.Is(err, domainops.ErrDNSTemplateRequiresDNS):
		return fmt.Errorf("--dns-template seeds records into the panel-hosted zone; --manage-dns=false hosts DNS externally")
	case errors.Is(err, domainops.ErrDNSTemplateUnknown):
		return fmt.Errorf("--dns-template %q does not exist", strings.TrimSpace(in.DNSTemplateID))
	case errors.Is(err, domainops.ErrDNSTemplateLookup):
		return fmt.Errorf("look up DNS template: %w", err)
	default:
		return err
	}
}

// validateDomainName is the CLI adapter over the shared domainops FQDN gate
// (JAB-279 AC2/AC6). The leaf owns the rule — at least two labels and a 2+ letter
// TLD, letters/digits/hyphens only — so `jabali domain create` accepts exactly
// the names the REST and automation doors accept. This adapter maps each typed
// reason to a CLI-voice message; it now distinguishes empty / HTML / path /
// whitespace reasons like the HTTP door instead of collapsing them all into a
// single "not a valid FQDN" (the accepted/rejected set is unchanged — the leaf's
// regex already rejected every one of those inputs).
func validateDomainName(s string) error {
	return cliDomainNameError(domainops.ValidateDomainName(s), s)
}

// cliDomainNameError maps a domainops name rejection for s to the CLI's
// message; nil and any other error map to nil. domainops.Create returns the
// same sentinels, so the create adapter reuses it.
func cliDomainNameError(err error, s string) error {
	switch {
	case errors.Is(err, domainops.ErrDomainNameEmpty):
		return fmt.Errorf("domain name cannot be empty")
	case errors.Is(err, domainops.ErrDomainNameWhitespace):
		return fmt.Errorf("domain %q contains whitespace — quote the value if it has special chars", s)
	case errors.Is(err, domainops.ErrDomainNameTooLong):
		return fmt.Errorf("domain %q exceeds 253 chars", s)
	case errors.Is(err, domainops.ErrDomainNameHTML):
		return fmt.Errorf("domain %q contains invalid HTML characters", s)
	case errors.Is(err, domainops.ErrDomainNameTraversal):
		return fmt.Errorf("domain %q contains invalid path characters", s)
	case errors.Is(err, domainops.ErrDomainNameNotFQDN):
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
