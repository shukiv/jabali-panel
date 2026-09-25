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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
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
	if err := validateDomainName(in.Name); err != nil {
		return nil, nil, err
	}

	// Cross-tenant alias-collision guard (JAB-279 / GH #1625) — the REST create,
	// rename, and docker-app paths reject a name whose apex / www / mail-helper
	// server_name is already claimed by another domain's web-domain alias; the
	// CLI ran none of them, so `jabali domain create` could stand up a domain
	// that reopens the duplicate-server_name hijack. Route through the shared
	// api.AliasCollision so the candidate derivation cannot drift from the HTTP
	// path. Fail CLOSED: a lookup error aborts the create, never proceeds. Runs
	// after validateDomainName and before any side effect, mirroring the REST
	// create precedence (validate -> collision -> owner).
	if hit, clash, cerr := api.AliasCollision(ctx, repository.NewWebDomainAliasRepository(sharedDB), in.Name); cerr != nil {
		return nil, nil, fmt.Errorf("verify domain name against existing aliases: %w", cerr)
	} else if clash {
		return nil, nil, fmt.Errorf("the name %q is already used as an alias of another domain", hit)
	}

	domains := domainRepoFromDB()
	packages := packageRepoFromDB()

	// Accept email / username / ULID — same resolver as the other user-
	// facing CLIs so operators don't have to copy-paste ULIDs.
	owner, err := resolveUser(ctx, in.UserID)
	if err != nil {
		return nil, nil, err
	}
	// Owner-eligibility gate (JAB-279) — the same domainops policy the REST
	// handler runs. Routing through the leaf closes a CLI gap: the CLI never
	// checked owner.Suspended, so a suspended owner could get a live vhost from
	// the command line while the account stayed locked. The messages stay the
	// CLI's own (the leaf owns the policy, each adapter owns its transport).
	switch err := domainops.CheckOwnerEligible(owner); {
	case errors.Is(err, domainops.ErrAdminCannotHost):
		return nil, nil, fmt.Errorf("admin users cannot host domains — create a regular user")
	case errors.Is(err, domainops.ErrOwnerSuspended):
		return nil, nil, fmt.Errorf("user %q is suspended — unsuspend before adding domains", owner.ID)
	case errors.Is(err, domainops.ErrOwnerNoUsername):
		return nil, nil, fmt.Errorf("user %q has no username — inconsistent state", owner.ID)
	case err != nil:
		return nil, nil, err
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

	// GH #1449: resolve the web / mail / dns service matrix before building the
	// row, mirroring the HTTP createDomainOp.
	webEnabled := !in.WebDisabled
	dnsEnabled := !in.DNSDisabled
	mailProvider := in.MailProvider
	if mailProvider == "" {
		mailProvider = models.MailProviderJabali
	}
	if !models.ValidMailProvider(mailProvider) {
		return nil, nil, fmt.Errorf("invalid --mail %q (want jabali|none|m365|google)", mailProvider)
	}
	// GH #1627: 'custom' is the posture of a domain created from a DNS template,
	// not a directly-selectable provider — ValidMailProvider recognises it (so a
	// persisted row validates) but the CLI must not create a bare 'custom' domain
	// with no template (an inert external domain with no mail records). DNS
	// templates are not a CLI feature in this phase; reject the value at input.
	if mailProvider == models.MailProviderCustom {
		return nil, nil, fmt.Errorf("invalid --mail %q (custom is the posture of a domain created from a DNS template; not selectable on the CLI)", mailProvider)
	}
	// GH #1627: a chosen custom DNS template overrides the mail posture to
	// external ('custom') — the reconciler seeds its records into the fresh
	// zone (keyed off MailTemplateID). Mutually exclusive with an explicit mail
	// provider, and requires the panel to host DNS (nothing to seed into
	// otherwise). Mirrors the HTTP createDomainOp dns_template_id path; resolved
	// BEFORE DeriveMailFlags so the external posture is derived from 'custom'.
	var mailTemplateID *string
	if tmplID := strings.TrimSpace(in.DNSTemplateID); tmplID != "" {
		if mailProvider != models.MailProviderJabali {
			return nil, nil, fmt.Errorf("--dns-template sets the mail posture; do not also pass --mail %q", mailProvider)
		}
		if !dnsEnabled {
			return nil, nil, fmt.Errorf("--dns-template seeds records into the panel-hosted zone; --manage-dns=false hosts DNS externally")
		}
		if _, terr := repository.NewDNSTemplateRepository(sharedDB).FindByID(ctx, tmplID); terr != nil {
			if errors.Is(terr, repository.ErrNotFound) {
				return nil, nil, fmt.Errorf("--dns-template %q does not exist", tmplID)
			}
			return nil, nil, fmt.Errorf("look up DNS template: %w", terr)
		}
		mailProvider = models.MailProviderCustom
		mailTemplateID = &tmplID
	}
	// GH #1409: coerce a 'jabali' provider to 'none' when this server's mail
	// module is switched off, through the same domainops leaf the REST create
	// path uses (createDomainOp) — otherwise `jabali domain create` (default
	// --mail jabali) on a mail-less server would persist EmailEnabled=true and
	// provision Jabali mail that can't run, a different stored domain than REST
	// stores for the same input (AC2). Runs after the DNS-template block (so a
	// 'custom' posture is untouched) and BEFORE DeriveMailFlags, mirroring the
	// REST ordering. Fail OPEN on an unreadable settings row (assume the module
	// is installed): a provisioning coercion must never block a create — the
	// same choice the REST path makes.
	mailModuleEnabled := true
	if st, sErr := serverSettingsRepoFromDB().Get(ctx); sErr == nil && st != nil {
		mailModuleEnabled = st.MailEnabled
	}
	mailProvider = domainops.MailProviderForServer(mailProvider, mailModuleEnabled)
	mailEnabled, mailSkipSAN := models.DeriveMailFlags(mailProvider)
	if !webEnabled && !dnsEnabled && !mailEnabled {
		return nil, nil, fmt.Errorf("select at least one service: web hosting (--web-enabled), DNS (--manage-dns), or mail (--mail)")
	}

	// Trim first (matches the REST create path, which trims before it validates
	// and stores) so a trailing space is neither stored nor mkdir'd.
	docRoot := strings.TrimSpace(in.DocRoot)
	if !webEnabled {
		if in.ReverseProxy {
			return nil, nil, fmt.Errorf("a reverse-proxy domain requires web hosting")
		}
		if docRoot != "" {
			return nil, nil, fmt.Errorf("a web-disabled domain has no document root")
		}
		docRoot = "" // docroot-less (DNS-only zone / mail-only domain)
	} else {
		// JAB-279 (AC1 module owns validate / AC2 same stored state across
		// adapters): confine the document root to the owner's home before it is
		// stored — the reconciler mkdir -p's the path and renders a vhost for it,
		// so an unchecked --doc-root would stand up a site on an arbitrary path.
		// The CLI is an operator tool, so it applies the same admin-floor rule the
		// REST admin create path uses (domainops.ValidateDocumentRoot); a path
		// outside /home/<user>/ or containing ".." is refused here. *owner.Username
		// is safe: the eligibility gate above already rejected a nil/empty username.
		if err := domainops.ValidateDocumentRoot(docRoot, *owner.Username, in.Name); err != nil {
			return nil, nil, fmt.Errorf("invalid --doc-root: %w", err)
		}
		if docRoot == "" {
			docRoot = "/home/" + *owner.Username + "/domains/" + in.Name + "/public_html"
		}
	}

	// DNS-only (web off + no Jabali mail) has nothing to serve over TLS.
	sslMode := models.SSLModeLE
	if !webEnabled && !mailEnabled {
		sslMode = models.SSLModeNone
	}

	now := time.Now().UTC()
	d := &models.Domain{
		ID:             ids.NewULID(),
		UserID:         ownerID,
		Name:           in.Name,
		DocRoot:        docRoot,
		IsEnabled:      true,
		WebDisabled:    in.WebDisabled,
		DNSDisabled:    in.DNSDisabled,
		MailProvider:   mailProvider,
		MailTemplateID: mailTemplateID, // GH #1627: nil unless --dns-template was chosen
		EmailEnabled:   mailEnabled,
		SkipAutoSAN:    mailSkipSAN,
		SSLMode:        sslMode,
		SSLEnabled:     models.SSLEnabledForMode(sslMode),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	// GH #1175 / #1401: reverse-proxy domains draw a loopback port from the
	// shared allocator BEFORE the insert (owner_id = the ULID above). The
	// validate → system-uid probe → allocate sequence and the release on a
	// failed insert are the domainops module's (JAB-279), the same code the
	// REST create door runs; this adapter maps its sentinels to CLI messages.
	ports := repository.NewPortAllocationRepository(sharedDB)
	if in.ReverseProxy {
		deps := domainops.PortDeps{Ports: ports}
		// The probe is only needed for an explicit port. Assign the agent only
		// when the pointer is set: a nil *agent.Client in the interface would
		// pass the module's nil check and panic on Call.
		if in.ReverseProxyPort != 0 && initAgent() == nil && sharedAgent != nil {
			deps.Agent = sharedAgent
		}
		port, perr := domainops.ReserveReverseProxyPort(ctx, deps, d.ID, in.ReverseProxyPort)
		switch {
		case errors.Is(perr, domainops.ErrReverseProxyPortInvalid):
			return nil, nil, perr // the static policy's own reason
		case errors.Is(perr, domainops.ErrReverseProxyPortSystemBound):
			return nil, nil, fmt.Errorf("port %d is already in use by a system service — choose another", in.ReverseProxyPort)
		case errors.Is(perr, domainops.ErrReverseProxyPortInUse):
			return nil, nil, fmt.Errorf("port %d is already assigned to another domain — choose another", in.ReverseProxyPort)
		case perr != nil:
			return nil, nil, fmt.Errorf("allocate reverse-proxy port: %w", perr)
		}
		d.ReverseProxyPort = uint32(port)
	}

	// PersistDomain releases the reverse-proxy port on any insert failure.
	if err := domainops.PersistDomain(ctx, domains, ports, d); err != nil {
		if errors.Is(err, domainops.ErrDomainExists) {
			return nil, nil, fmt.Errorf("domain %q already exists", in.Name)
		}
		return nil, nil, fmt.Errorf("create domain row: %w", err)
	}

	var warnings []string

	// JAB-170 phase 5 / JAB-279: auto-attach a covering shared certificate so a
	// CLI-created web domain reaches HTTPS immediately (no ACME wait). The
	// list → cover → attach step and its web-off skip are the domainops
	// module's, the same code the REST create door runs.
	//
	// Fail-OPEN by design — a lookup or attach failure is recorded as a soft
	// warning, never a create failure. Auto-attach is an optimisation, not an
	// invariant; the reconciler still issues or attaches a cert on its next
	// tick. No Reconciler.Schedule here: the CLI holds no in-process reconciler
	// handle (the JAB-355 cross-process limitation), so convergence relies on
	// that tick — the same accepted deviation as the rest of the CLI create
	// path (see the reconciler note below).
	switch cert, err := domainops.AttachCoveringSharedCert(ctx, domainops.SharedCertDeps{
		Certs:   sharedCertRepoFromDB(),
		Domains: domains,
	}, d); {
	case errors.Is(err, domainops.ErrSharedCertLookup):
		warnings = append(warnings, fmt.Sprintf("shared-certificate lookup skipped: %v", err))
	case errors.Is(err, domainops.ErrSharedCertAttach):
		warnings = append(warnings, fmt.Sprintf(
			"shared-certificate auto-attach failed (retry with `jabali ssl shared attach --domain %s --cert-id %s`): %v",
			d.Name, cert.ID, err))
	}

	// Auto-enable email. Best-effort — if the agent's down or Stalwart
	// refuses the domain name, we record the reason as a soft warning
	// and the operator can retry via `jabali domain email-enable <name>`
	// or the Email tab in the UI.
	if mailProvider != models.MailProviderJabali {
		// External (m365/google) or no mail — nothing to register on Stalwart;
		// the reconciler publishes the provider's own DNS (or none). A DNS-only
		// zone (--mail none) must not auto-enable Jabali mail.
	} else if err := initAgent(); err != nil {
		warnings = append(warnings, fmt.Sprintf("email auto-enable skipped: agent unavailable (%v)", err))
	} else {
		deps := newDomainEmailDepsFromGlobals()
		_, _, dnsWarnings, err := domainmailops.Enable(ctx, deps, d)
		if err != nil {
			warnings = append(warnings,
				fmt.Sprintf("email auto-enable failed (can retry with `jabali domain email-enable %s`): %v", d.Name, err))
		} else {
			warnings = append(warnings, domainmailops.WarningMessages(dnsWarnings)...)
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

// validateDomainName is the CLI adapter over the shared domainops FQDN gate
// (JAB-279 AC2/AC6). The leaf owns the rule — at least two labels and a 2+ letter
// TLD, letters/digits/hyphens only — so `jabali domain create` accepts exactly
// the names the REST and automation doors accept. This adapter maps each typed
// reason to a CLI-voice message; it now distinguishes empty / HTML / path /
// whitespace reasons like the HTTP door instead of collapsing them all into a
// single "not a valid FQDN" (the accepted/rejected set is unchanged — the leaf's
// regex already rejected every one of those inputs).
func validateDomainName(s string) error {
	switch err := domainops.ValidateDomainName(s); {
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
