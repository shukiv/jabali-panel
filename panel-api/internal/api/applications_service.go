package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// InstallApplication is the transport-agnostic version of the gin
// create handler. It exists so the CLI (panel-api/cmd/server/app_cmd.go)
// can drive the same install pipeline as HTTP without going through
// /api/v1/applications — over the HTTP API, an in-panel auth middleware (RequireKratosSession)
// are 401'd at the HTTP edge, but the service-level pipeline is the
// same.
//
// Returns one of (*InstallResult, nil) or (nil, *InstallError). HTTP
// callers map InstallError.HTTPStatus → response code; CLI callers map
// it to a terminal error.

// InstallParams is the input shape. Handler/CLI assemble these from
// their respective contexts (claims vs operator-supplied flags).
type InstallParams struct {
	AppType      string
	UserID       string // owner for the install (HTTP: claims.UserID; CLI: domain.UserID)
	IsAdminCall  bool   // when true, ownership mismatch returns 403 instead of 404 — preserves the HTTP handler's "don't leak existence" behaviour for non-admins
	DomainID     string
	Subdirectory string
	UseWWW       bool
	Params       map[string]any
}

// InstallResult mirrors the gin response shape — Install carries the
// row that was inserted, AdminPassword is the plaintext credential
// surfaced once (the row stores no password, only the username/email).
type InstallResult struct {
	Install       *models.ApplicationInstall
	AdminPassword string
}

// InstallError is the strongly-typed failure mode. Code matches the
// JSON `error` field the HTTP handler used pre-extraction so existing
// clients see the same payload. Detail is optional.
type InstallError struct {
	Code       string
	Detail     string
	HTTPStatus int
}

func (e *InstallError) Error() string {
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

func newInstallErr(status int, code, detail string) *InstallError {
	return &InstallError{Code: code, Detail: detail, HTTPStatus: status}
}

// InstallApplication runs the full create pipeline:
//
//  1. Resolve descriptor + validate subdirectory + validate params
//  2. Load domain + ownership check
//  3. Reject if (domain, subdirectory) slot is already taken
//  4. Resolve owner's linux username
//  5. Provision DB chain (if descriptor.RequiresDB)
//  6. Generate admin username (server-side, never trusted from caller)
//  7. Insert install row (status=pending)
//  8. Dispatch the per-app kicker goroutine
//
// Side-effects (DB rows created, agent calls fired) are identical to
// the previous gin handler. The body is deliberately a near-1:1 move
// — diffing it against the old handler should show only `c.JSON(...);
// return` → `return nil, newInstallErr(...)` translations.
func InstallApplication(ctx context.Context, deps ApplicationHandlerConfig, p InstallParams) (*InstallResult, *InstallError) {
	if deps.Apps == nil {
		// Wiring bug, not user error — fail loud so it shows up in
		// dev/test. In production this is impossible because
		// app.NewWithDeps registers the panic-on-nil at startup.
		return nil, newInstallErr(http.StatusInternalServerError, "internal", "apps registry not wired")
	}

	descriptor, ok := deps.Apps.Get(p.AppType)
	if !ok {
		return nil, newInstallErr(http.StatusBadRequest, "invalid_app_type", "unknown app: "+p.AppType)
	}

	if err := validateSubdirectory(p.Subdirectory); err != nil {
		return nil, newInstallErr(http.StatusBadRequest, "invalid_subdirectory", err.Error())
	}

	// RootOnly apps (ITFlow, GH #226) only work at the domain/subdomain
	// root — reject a subdirectory or www prefix at the boundary so a
	// hand-crafted request can't bypass the hidden UI controls.
	if descriptor.RootOnly {
		if p.Subdirectory != "" {
			return nil, newInstallErr(http.StatusBadRequest, "subdirectory_not_allowed", descriptor.DisplayName+" must be installed at the domain root")
		}
		if p.UseWWW {
			return nil, newInstallErr(http.StatusBadRequest, "www_not_allowed", descriptor.DisplayName+" cannot use a www prefix")
		}
	}

	if err := validateInstallParams(p.Params, descriptor.InstallParamSchema); err != nil {
		return nil, newInstallErr(http.StatusBadRequest, "invalid_params", err.Error())
	}

	domain, err := deps.Domains.FindByID(ctx, p.DomainID)
	if err != nil {
		if isNotFound(err) {
			return nil, newInstallErr(http.StatusNotFound, "domain_not_found", "")
		}
		slog.ErrorContext(ctx, "applications create: domain lookup failed", "err", err, "domain_id", p.DomainID)
		return nil, newInstallErr(http.StatusInternalServerError, "internal", "")
	}
	if domain.UserID != p.UserID {
		// Mirror the HTTP handler's "don't leak existence" rule: an
		// admin acting on someone else's domain gets 403; a non-admin
		// gets 404 so they can't confirm the row exists.
		if p.IsAdminCall {
			return nil, newInstallErr(http.StatusForbidden, "forbidden", "")
		}
		return nil, newInstallErr(http.StatusNotFound, "domain_not_found", "")
	}

	// (domain, subdirectory) precheck — at most one app per slot
	// regardless of app_type. Migration 000046 still includes app_type
	// in the DB UNIQUE for forward compat, so this stricter rule lives
	// at the API boundary.
	existing, lookupErr := deps.ApplicationInstalls.FindByDomainAndSubdirectory(ctx, p.DomainID, p.Subdirectory)
	if lookupErr == nil && existing != nil {
		return nil, newInstallErr(http.StatusConflict, "install_exists", "")
	}
	if lookupErr != nil && !isNotFound(lookupErr) {
		slog.ErrorContext(ctx, "applications create: existing install lookup failed", "err", lookupErr)
		return nil, newInstallErr(http.StatusInternalServerError, "internal", "")
	}

	var osUser string
	var suspended bool
	if u, uErr := deps.Users.FindByID(ctx, p.UserID); uErr == nil && u != nil {
		if u.Username != nil {
			osUser = *u.Username
		}
		suspended = u.Suspended
	}
	if osUser == "" {
		slog.ErrorContext(ctx, "applications create: user has no linux username", "user_id", p.UserID)
		return nil, newInstallErr(http.StatusConflict, "user_not_provisioned", "")
	}
	// Refuse app installs on suspended users — domain.create already
	// refuses, but the app surface has its own user lookup so guard
	// here too. Operator must unsuspend first.
	if suspended {
		return nil, newInstallErr(http.StatusConflict, "user_suspended", "user is suspended — unsuspend before installing applications")
	}

	now := time.Now().UTC()

	// Optional DB chain. RequiresDB=false apps (DokuWiki, Grav)
	// continue with chain zero-value so DBID="" and the install row
	// records no database.
	var (
		chain         provisionedDB
		adminPassword string
	)
	if descriptor.RequiresDB {
		adminPassword, _ = paramString(p.Params, "admin_password")
		if adminPassword == "" {
			adminPassword = ids.NewSecret()
		}
		// DB password is ALWAYS its own generated secret, never the admin
		// password (GH #226). The admin password (above) is only for the
		// app's admin account; the DB credential is independent.
		dbPassword := ids.NewSecret()
		chain, err = provisionDBChain(ctx, deps, p.UserID, osUser, descriptor.Name, dbPassword)
		if err != nil {
			slog.ErrorContext(ctx, "applications create: provision db chain", "err", err)
			// JAB-114: err here is agent-origin (db.create) — logged above,
			// never echoed to the client (InstallError.Detail renders to the body).
			return nil, newInstallErr(http.StatusBadGateway, "agent_failed", "")
		}
	}

	// Always generate the admin username server-side. The descriptor
	// schema deliberately omits admin_username so the UI never asks
	// (per operator's "admin username is a bad idea, 6 letters auto
	// generated"). For MediaWiki we uppercase the first letter to
	// satisfy its "username must start with capital" rule.
	adminUsername := generateAdminUsername(6)
	// Operator may optionally choose the admin username (GH #228); blank keeps
	// the generated random one. Apps opt in via the AdminUsernameParam field.
	if v := strings.TrimSpace(paramOr(p.Params, "admin_username", "")); v != "" {
		adminUsername = v
	}
	if descriptor.Name == "mediawiki" && len(adminUsername) > 0 {
		adminUsername = strings.ToUpper(adminUsername[:1]) + adminUsername[1:]
	}
	// Email-login apps (ITFlow #226) authenticate by admin email, not a
	// username. Surface the email as the login so the post-install screen
	// shows the real credential instead of a meaningless random username.
	if descriptor.EmailLogin {
		if em := paramOr(p.Params, "admin_email", ""); em != "" {
			adminUsername = em
		}
	}

	installID := ids.NewULID()
	install := &models.ApplicationInstall{
		ID:            installID,
		UserID:        p.UserID,
		DomainID:      p.DomainID,
		DBID:          models.DBIDPtr(chain.DBID),
		AppType:       descriptor.Name,
		AdminUsername: adminUsername,
		AdminEmail:    paramOr(p.Params, "admin_email", ""),
		Locale:        paramOr(p.Params, "locale", "en_US"),
		UseWWW:        p.UseWWW,
		Subdirectory:  p.Subdirectory,
		Status:        "pending",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := deps.ApplicationInstalls.Create(ctx, install); err != nil {
		if descriptor.RequiresDB {
			rollbackDBChain(ctx, deps, chain)
		}
		slog.ErrorContext(ctx, "applications create: install row create failed", "err", err, "install_id", installID)
		return nil, newInstallErr(http.StatusInternalServerError, "internal", "")
	}

	// Per-app install kicker. Adding an app means adding a case here +
	// the descriptor + the agent installer. Each branch may also
	// overwrite adminPassword with the per-app credential the user
	// will actually use to log in (separate from the DB password).
	siteURL := buildSiteURL(domain.Name, p.UseWWW, p.Subdirectory)

	// Snapshot the install state before launching the async kicker.
	// The kicker goroutine calls UpdateStatus, which the GORM repo
	// implements as SQL UPDATE (no shared memory), but in-memory repos
	// (tests, potential future caches) may mutate the *ApplicationInstall
	// pointer we also return to the HTTP handler — a data race under
	// `go test -race`. The HTTP response should reflect the deterministic
	// "pending" state at creation time, not whatever the kicker raced to
	// write first; snapshotting gives callers a stable view either way.
	snapshot := *install

	adminPassword = dispatchInstallKicker(ctx, descriptor.Name, kickContext{
		InstallID:     installID,
		UserID:        p.UserID,
		OSUser:        osUser,
		DocRoot:       domain.DocRoot,
		Subdirectory:  install.Subdirectory,
		SiteURL:       siteURL,
		AdminUsername: install.AdminUsername,
		AdminEmail:    install.AdminEmail,
		Locale:        install.Locale,
		UseWWW:        install.UseWWW,
		Chain:         chain,
		Params:        p.Params,
		// Admin-password seed for the kicker (WordPress reuses it for the
		// admin account); the DB password is carried separately on Chain.
		DBPassword: adminPassword,
	}, deps)

	return &InstallResult{Install: &snapshot, AdminPassword: adminPassword}, nil
}

// kickContext bundles the per-app args we already computed so the
// dispatcher doesn't re-derive them in every case. DBPassword is
// pre-populated from the chain (or empty for RequiresDB=false apps);
// the per-app kicker may either reuse it (WordPress) or generate its
// own admin password (Drupal/Joomla/...) and return it as the new
// AdminPassword to surface.
type kickContext struct {
	InstallID     string
	UserID        string
	OSUser        string
	DocRoot       string
	Subdirectory  string
	SiteURL       string
	AdminUsername string
	AdminEmail    string
	Locale        string
	UseWWW        bool
	Chain         provisionedDB
	Params        map[string]any
	DBPassword    string
}

// dispatchInstallKicker runs the per-app installer goroutine and
// returns the admin password to surface in the response. The body is
// the same per-app switch the original gin handler had — extracted so
// both InstallApplication (service) and CLI can call it.
func dispatchInstallKicker(ctx context.Context, appName string, k kickContext, deps ApplicationHandlerConfig) string {
	adminPassword := k.DBPassword
	switch appName {
	case "wordpress":
		go createInstallAndKickAgent(ctx, installKickArgs{
			InstallID:     k.InstallID,
			UserID:        k.UserID,
			OSUser:        k.OSUser,
			DocRoot:       k.DocRoot,
			DBName:        k.Chain.DBName,
			DBUser:        k.Chain.DBUsername,
			DBPassword:    k.Chain.DBPassword,
			SiteURL:       k.SiteURL,
			SiteTitle:     paramOr(k.Params, "site_title", "My WordPress Site"),
			AdminUsername: k.AdminUsername,
			AdminPassword: adminPassword,
			AdminEmail:    k.AdminEmail,
			Locale:        k.Locale,
			Version:       paramOr(k.Params, "version", "latest"), // JAB-180
			Subdirectory:  k.Subdirectory,
			UseWWW:        k.UseWWW,
		}, deps)
	case "drupal":
		drupalPass := paramOr(k.Params, "admin_password", "")
		if drupalPass == "" {
			drupalPass = ids.NewULID()
		}
		go createDrupalInstallAndKickAgent(ctx, drupalKickArgs{
			InstallID:    k.InstallID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			DBName:       k.Chain.DBName,
			DBUser:       k.Chain.DBUsername,
			DBPassword:   k.Chain.DBPassword,
			SiteTitle:    paramOr(k.Params, "site_title", "My Drupal Site"),
			AdminUser:    k.AdminUsername,
			AdminPass:    drupalPass,
			AdminEmail:   k.AdminEmail,
			SiteMail:     paramOr(k.Params, "site_mail", k.AdminEmail),
			Profile:      paramOr(k.Params, "profile", "standard"),
			UseWWW:       k.UseWWW,
		}, deps)
		adminPassword = drupalPass
	case "joomla":
		joomlaPass := paramOr(k.Params, "admin_password", "")
		if joomlaPass == "" {
			joomlaPass = ids.NewULID()
		}
		go createJoomlaInstallAndKickAgent(ctx, joomlaKickArgs{
			InstallID:     k.InstallID,
			UserID:        k.UserID,
			OSUser:        k.OSUser,
			DocRoot:       k.DocRoot,
			Subdirectory:  k.Subdirectory,
			SiteURL:       k.SiteURL,
			DBName:        k.Chain.DBName,
			DBUser:        k.Chain.DBUsername,
			DBPassword:    k.Chain.DBPassword,
			SiteTitle:     paramOr(k.Params, "site_title", "My Joomla Site"),
			AdminUser:     k.AdminUsername,
			AdminPass:     joomlaPass,
			AdminEmail:    k.AdminEmail,
			AdminFullName: paramOr(k.Params, "admin_full_name", "Super User"),
			UseWWW:        k.UseWWW,
		}, deps)
		adminPassword = joomlaPass
	case "phpbb":
		phpbbPass := paramOr(k.Params, "admin_password", "")
		if phpbbPass == "" {
			phpbbPass = ids.NewULID()
		}
		go createPhpBBInstallAndKickAgent(ctx, phpbbKickArgs{
			InstallID:        k.InstallID,
			OSUser:           k.OSUser,
			DocRoot:          k.DocRoot,
			Subdirectory:     k.Subdirectory,
			SiteURL:          k.SiteURL,
			DBName:           k.Chain.DBName,
			DBUser:           k.Chain.DBUsername,
			DBPassword:       k.Chain.DBPassword,
			SiteTitle:        paramOr(k.Params, "site_title", "My Forum"),
			BoardDescription: paramOr(k.Params, "board_description", "A discussion forum"),
			AdminUser:        k.AdminUsername,
			AdminPass:        phpbbPass,
			AdminEmail:       k.AdminEmail,
			Language:         paramOr(k.Params, "language", "en"),
			UseWWW:           k.UseWWW,
		}, deps)
		adminPassword = phpbbPass
	case "flarum":
		flarumPass := paramOr(k.Params, "admin_password", "")
		if flarumPass == "" {
			flarumPass = ids.NewULID()
		}
		go createFlarumInstallAndKickAgent(ctx, flarumKickArgs{
			InstallID:    k.InstallID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			DBName:       k.Chain.DBName,
			DBUser:       k.Chain.DBUsername,
			DBPassword:   k.Chain.DBPassword,
			SiteTitle:    paramOr(k.Params, "site_title", "My Forum"),
			AdminUser:    k.AdminUsername,
			AdminPass:    flarumPass,
			AdminEmail:   k.AdminEmail,
			UseWWW:       k.UseWWW,
		}, deps)
		adminPassword = flarumPass
	case "itflow":
		itflowPass := paramOr(k.Params, "admin_password", "")
		if itflowPass == "" {
			itflowPass = ids.NewULID()
		}
		go createITFlowInstallAndKickAgent(ctx, itflowKickArgs{
			InstallID:    k.InstallID,
			UserID:       k.UserID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			DBName:       k.Chain.DBName,
			DBUser:       k.Chain.DBUsername,
			DBPassword:   k.Chain.DBPassword,
			CompanyName:  paramOr(k.Params, "company_name", "My Company"),
			AdminName:    paramOr(k.Params, "admin_name", "Administrator"),
			AdminEmail:   k.AdminEmail,
			AdminPass:    itflowPass,
			RepoBranch:   paramOr(k.Params, "repo_branch", "master"),
			UseWWW:       k.UseWWW,
		}, deps)
		adminPassword = itflowPass
	case "osticket":
		ostPass := paramOr(k.Params, "admin_password", "")
		if ostPass == "" {
			ostPass = ids.NewULID()
		}
		go createOsTicketInstallAndKickAgent(ctx, osticketKickArgs{
			InstallID:     k.InstallID,
			UserID:        k.UserID,
			OSUser:        k.OSUser,
			DocRoot:       k.DocRoot,
			SiteURL:       k.SiteURL,
			DBName:        k.Chain.DBName,
			DBUser:        k.Chain.DBUsername,
			DBPassword:    k.Chain.DBPassword,
			HelpdeskName:  paramOr(k.Params, "helpdesk_name", "Support"),
			HelpdeskEmail: paramOr(k.Params, "helpdesk_email", ""),
			AdminFirst:    paramOr(k.Params, "admin_first_name", "Admin"),
			AdminLast:     paramOr(k.Params, "admin_last_name", "User"),
			AdminEmail:    k.AdminEmail,
			// k.AdminUsername is the stored install credential (generated
			// server-side, or the operator's admin_username param — the
			// service already folded that in). Re-deriving from Params here
			// seeded "sysadmin" while the UI showed the generated name, so
			// the displayed login never worked (JAB-231 E2E).
			AdminUsername: k.AdminUsername,
			AdminPass:     ostPass,
		}, deps)
		adminPassword = ostPass
	case "prestashop":
		prestaPass := paramOr(k.Params, "admin_password", "")
		if prestaPass == "" {
			prestaPass = ids.NewULID()
		}
		go createPrestaShopInstallAndKickAgent(ctx, prestashopKickArgs{
			InstallID:      k.InstallID,
			OSUser:         k.OSUser,
			DocRoot:        k.DocRoot,
			Subdirectory:   k.Subdirectory,
			SiteURL:        k.SiteURL,
			DBName:         k.Chain.DBName,
			DBUser:         k.Chain.DBUsername,
			DBPassword:     k.Chain.DBPassword,
			SiteTitle:      paramOr(k.Params, "site_title", "My Shop"),
			AdminEmail:     k.AdminEmail,
			AdminPass:      prestaPass,
			AdminFirstName: paramOr(k.Params, "admin_first_name", "Site"),
			AdminLastName:  paramOr(k.Params, "admin_last_name", "Owner"),
			Country:        paramOr(k.Params, "country", "us"),
			Language:       paramOr(k.Params, "language", "en"),
			UseWWW:         k.UseWWW,
		}, deps)
		adminPassword = prestaPass
	case "openemr":
		openemrPass := paramOr(k.Params, "admin_password", "")
		if openemrPass == "" {
			openemrPass = ids.NewSecret()
		}
		go createOpenEMRInstallAndKickAgent(ctx, openEMRKickArgs{
			InstallID:    k.InstallID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			DBName:       k.Chain.DBName,
			DBUser:       k.Chain.DBUsername,
			DBPassword:   k.Chain.DBPassword,
			SiteTitle:    paramOr(k.Params, "site_title", "OpenEMR"),
			AdminUser:    k.AdminUsername,
			AdminEmail:   k.AdminEmail,
			AdminPass:    openemrPass,
			UseWWW:       k.UseWWW,
		}, deps)
		adminPassword = openemrPass
	case "invoiceshelf":
		invoicePass := paramOr(k.Params, "admin_password", "")
		if invoicePass == "" {
			// CSPRNG secret (192-bit), NOT a truncated ULID — ULID's leading
			// chars are a millisecond timestamp, so ids.NewULID()[:16] would
			// be mostly install-time-predictable. Satisfies InvoiceShelf's
			// >= 8 char policy with mixed case + digits.
			invoicePass = ids.NewSecret()
		}
		go createInvoiceShelfInstallAndKickAgent(ctx, invoiceShelfKickArgs{
			InstallID:    k.InstallID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			DBName:       k.Chain.DBName,
			DBUser:       k.Chain.DBUsername,
			DBPassword:   k.Chain.DBPassword,
			SiteTitle:    paramOr(k.Params, "site_title", "InvoiceShelf"),
			Currency:     paramOr(k.Params, "currency", "USD"),
			Country:      paramOr(k.Params, "country", "US"),
			AdminEmail:   k.AdminEmail,
			AdminPass:    invoicePass,
			UseWWW:       k.UseWWW,
		}, deps)
		adminPassword = invoicePass
	case "privatebin":
		// No accounts, no DB — the smallest kick there is.
		go createPrivateBinInstallAndKickAgent(ctx, privateBinKickArgs{
			InstallID:    k.InstallID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			SiteTitle:    paramOr(k.Params, "site_title", "PrivateBin"),
			UseWWW:       k.UseWWW,
		}, deps)
	case "dokuwiki":
		dokuPass := paramOr(k.Params, "admin_password", "")
		if dokuPass == "" {
			dokuPass = ids.NewULID()
		}
		go createDokuWikiInstallAndKickAgent(ctx, dokuWikiKickArgs{
			InstallID:    k.InstallID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			SiteTitle:    paramOr(k.Params, "site_title", "My Wiki"),
			AdminUser:    k.AdminUsername,
			AdminPass:    dokuPass,
			AdminEmail:   k.AdminEmail,
			UseWWW:       k.UseWWW,
		}, deps)
		adminPassword = dokuPass
	case "opencart":
		opencartPass := paramOr(k.Params, "admin_password", "")
		if opencartPass == "" {
			// OpenCart's install/cli_install.php enforces 5-20 chars
			// and — critically — PRINTS the error + exits 0 on failure,
			// so passing the 26-char ULID caused silent install failures
			// (empty config.php, no schema, install/ still deleted).
			// Truncate to 20 = 100 bits of base32 entropy, well above
			// WP's default bcrypt work.
			opencartPass = ids.NewULID()[:20]
		}
		go createOpenCartInstallAndKickAgent(ctx, opencartKickArgs{
			InstallID:    k.InstallID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			DBName:       k.Chain.DBName,
			DBUser:       k.Chain.DBUsername,
			DBPassword:   k.Chain.DBPassword,
			AdminUser:    k.AdminUsername,
			AdminPass:    opencartPass,
			AdminEmail:   k.AdminEmail,
			UseWWW:       k.UseWWW,
		}, deps)
		adminPassword = opencartPass
	case "mediawiki":
		mwPass := paramOr(k.Params, "admin_password", "")
		if mwPass == "" {
			mwPass = ids.NewULID()
		}
		go createMediaWikiInstallAndKickAgent(ctx, mediaWikiKickArgs{
			InstallID:    k.InstallID,
			UserID:       k.UserID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			DBName:       k.Chain.DBName,
			DBUser:       k.Chain.DBUsername,
			DBPassword:   k.Chain.DBPassword,
			SiteTitle:    paramOr(k.Params, "site_title", "My MediaWiki"),
			AdminUser:    k.AdminUsername,
			AdminPass:    mwPass,
			AdminEmail:   k.AdminEmail,
			Language:     paramOr(k.Params, "language", "en"),
			UseWWW:       k.UseWWW,
		}, deps)
		adminPassword = mwPass
	case "moodle":
		moodlePass := paramOr(k.Params, "admin_password", "")
		if moodlePass == "" {
			// Moodle's default policy needs upper+lower+digit+symbol. A ULID is
			// upper+digit only, so lowercase the first chars + append a digit and
			// symbol to satisfy the policy while keeping the ULID entropy.
			u := ids.NewULID()
			moodlePass = strings.ToLower(u[:5]) + u[5:] + "7#"
		}
		go createMoodleInstallAndKickAgent(ctx, moodleKickArgs{
			InstallID:    k.InstallID,
			UserID:       k.UserID,
			OSUser:       k.OSUser,
			DocRoot:      k.DocRoot,
			Subdirectory: k.Subdirectory,
			SiteURL:      k.SiteURL,
			DBName:       k.Chain.DBName,
			DBUser:       k.Chain.DBUsername,
			DBPassword:   k.Chain.DBPassword,
			SiteTitle:    paramOr(k.Params, "site_title", "My Moodle"),
			AdminUser:    k.AdminUsername,
			AdminPass:    moodlePass,
			AdminEmail:   k.AdminEmail,
			Language:     paramOr(k.Params, "language", "en"),
		}, deps)
		adminPassword = moodlePass
	}
	return adminPassword
}
