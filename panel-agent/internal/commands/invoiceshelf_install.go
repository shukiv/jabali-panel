package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// invoiceshelf_install.go — installs InvoiceShelf (GH #631), a Laravel
// invoicing app (maintained Crater fork), on the per-user PHP-FPM pool.
//
// Two things make this the "hard" catalog entry vs the flat-file apps:
//
//  1. Laravel serves from public/, but jabali's docroot is fixed. Rather
//     than an alias/symlink dance (fragile under FPM + open_basedir), we
//     FLATTEN: move public/* up to the install root and rewrite the front
//     controller's two "/../" bootstrap paths — the same "no-public-dir"
//     shape Flarum ships prebuilt. The app's sensitive dirs (vendor,
//     storage, config, bootstrap, database, .env, artisan) then sit in the
//     webroot and are denied by a per-install nginx snippet (Flarum-style,
//     using = / ^~ precedence to beat the vhost's `~ \.php$`).
//
//  2. Onboarding is a stateful web wizard. We skip it: the release ships a
//     seeder path (DatabaseSeeder → UsersTableSeeder) that creates a working
//     super-admin + company + roles + default data. We run migrate --seed,
//     then patch the admin's identity to the operator's values and mark
//     Setting profile_complete=COMPLETED so the site opens at /login. This
//     is the app's own creation code, not a re-implementation.
//
// Pinned to the official release zip (vendor/ bundled), SHA-256 verified
// before extract, per the catalog's no-floating-latest bar (GH #631).

const (
	invoiceshelfVersion    = "2.4.2"
	invoiceshelfTarballURL = "https://github.com/InvoiceShelf/InvoiceShelf/releases/download/" + invoiceshelfVersion + "/InvoiceShelf.zip"
	invoiceshelfTarballSHA = "24277665597a013e7467c31cd7634852ab98491cc6b0cf616552c675d1eeace6"
)

type invoiceshelfInstallReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory"`
	SiteURL      string `json:"site_url"`
	SiteTitle    string `json:"site_title"`
	AdminEmail   string `json:"admin_email"`
	AdminPass    string `json:"admin_pass"`
	DBName       string `json:"db_name"`
	DBUser       string `json:"db_user"`
	DBPassword   string `json:"db_password"`
	DBHost       string `json:"db_host"`
	UseWWW       bool   `json:"use_www"`
}

type invoiceshelfInstallResp struct {
	Version string `json:"version"`
}

func invoiceshelfInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req invoiceshelfInstallReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	switch {
	case req.OSUser == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user is required"}
	case req.Docroot == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot is required"}
	case req.AdminEmail == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	case req.AdminPass == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass is required"}
	case req.DBName == "" || req.DBUser == "" || req.DBPassword == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "db_name, db_user, db_password are required"}
	}
	// admin_email lands in a tinker one-liner; refuse quotes/CR/LF so it
	// can't break out of the PHP string literal (the agent runs as root).
	if len(req.AdminEmail) > 254 || strings.ContainsAny(req.AdminEmail, "'\"\\\r\n") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email contains illegal characters"}
	}
	if req.SiteTitle == "" {
		req.SiteTitle = "InvoiceShelf"
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	installPath, err := appInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	domain, derr := DomainFromSiteURL(req.SiteURL)
	if derr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("derive domain from site_url: %v", derr)}
	}
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}

	if req.Subdirectory != "" {
		if out, err := runBoundedOutput(buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath), 0); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v (%s)", installPath, err, truncateStr(string(out), 256))}
		}
	}
	removePlaceholderIndex(ctx, installPath)

	// Download + verify, then extract as the OS user (strip InvoiceShelf/).
	tmpDir, err := stagingMkdirTemp("invoiceshelf-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	zipPath := filepath.Join(tmpDir, "invoiceshelf.zip")
	dlCtx, dlCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer dlCancel()
	if err := invoiceshelfDownload(dlCtx, zipPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(zipPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod zip: %v", err)}
	}
	// unzip has no --strip-components; extract then move InvoiceShelf/* up.
	if out, err := runBoundedOutput(buildSystemdRunCmd(ctx, req.OSUser, "unzip", "-q", "-o", zipPath, "-d", installPath), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("unzip: %v (%s)", err, truncateStr(string(out), 512))}
	}
	// Move InvoiceShelf/{*,.*} to installPath, then flatten public/.
	moveUp := "shopt -s dotglob nullglob; mv " + shellQuote(filepath.Join(installPath, "InvoiceShelf")) + "/* " + shellQuote(installPath) + "/ && rmdir " + shellQuote(filepath.Join(installPath, "InvoiceShelf"))
	if out, err := runBoundedOutput(buildSystemdRunShell(ctx, req.OSUser, moveUp), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("relocate app root: %v (%s)", err, truncateStr(string(out), 512))}
	}
	// Flatten public/ into the webroot (no-public-dir shape).
	flatten := "shopt -s dotglob nullglob; mv " + shellQuote(filepath.Join(installPath, "public")) + "/* " + shellQuote(installPath) + "/ && rmdir " + shellQuote(filepath.Join(installPath, "public"))
	if out, err := runBoundedOutput(buildSystemdRunShell(ctx, req.OSUser, flatten), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("flatten public/: %v (%s)", err, truncateStr(string(out), 512))}
	}
	// Rewrite the front controller for the flattened layout. Two changes vs
	// stock: (a) the bootstrap/autoload paths lose the "/../" now that
	// index.php sits at the app root, and (b) usePublicPath(__DIR__) tells
	// Laravel the PUBLIC path IS the install root — without it public_path()
	// still resolves to <root>/public (gone after flatten), which 500s the
	// Vite manifest lookup (`public/build/manifest.json`) on the very first
	// page. Found in live serving validation.
	indexPHP := "<?php\n" +
		"use Illuminate\\Http\\Request;\n" +
		"define('LARAVEL_START', microtime(true));\n" +
		"if (file_exists($maintenance = __DIR__.'/storage/framework/maintenance.php')) { require $maintenance; }\n" +
		"require __DIR__.'/vendor/autoload.php';\n" +
		"$app = require_once __DIR__.'/bootstrap/app.php';\n" +
		"$app->usePublicPath(__DIR__);\n" +
		"$app->handleRequest(Request::capture());\n"
	if err := dokuwikiWriteUserFile(ctx, req.OSUser, filepath.Join(installPath, "index.php"), indexPHP, "0644"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// .env from operator inputs. APP_KEY comes from key:generate below.
	appURL := strings.TrimSuffix(req.SiteURL, "/")
	env := "APP_ENV=production\n" +
		"APP_DEBUG=false\n" +
		"APP_NAME=" + envQuoted(req.SiteTitle) + "\n" +
		"APP_URL=" + appURL + "\n" +
		"APP_KEY=\n" +
		"APP_TIMEZONE=UTC\n" +
		"APP_LOCALE=en\n" +
		"DB_CONNECTION=mysql\n" +
		"DB_HOST=" + dbHost + "\n" +
		"DB_PORT=3306\n" +
		"DB_DATABASE=" + req.DBName + "\n" +
		"DB_USERNAME=" + req.DBUser + "\n" +
		"DB_PASSWORD=" + envQuoted(req.DBPassword) + "\n" +
		// File-backed session + sync queue avoid the Laravel `sessions` /
		// `jobs` tables that InvoiceShelf's migrations do NOT ship — a DB
		// session driver 500s on a missing `sessions` table (found in the
		// live serving validation). File sessions live under the app's
		// storage/, denied from the web by the nginx snippet.
		"SESSION_DRIVER=file\n" +
		"SESSION_DOMAIN=" + domain + "\n" +
		"SANCTUM_STATEFUL_DOMAINS=" + domain + "\n" +
		"CACHE_STORE=file\n" +
		"QUEUE_CONNECTION=sync\n" +
		"TRUSTED_PROXIES=*\n"
	if err := dokuwikiWriteUserFile(ctx, req.OSUser, filepath.Join(installPath, ".env"), env, "0640"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	php := phpCLIFor(req.OSUser)
	// artisan steps. Each runs as the OS user with cwd=installPath.
	artisan := func(timeout time.Duration, args ...string) error {
		full := append([]string{php, filepath.Join(installPath, "artisan")}, args...)
		cmd := buildSystemdRunCmd(ctx, req.OSUser, full...)
		cmd.Dir = installPath
		if out, err := runBoundedOutput(cmd, 0); err != nil {
			return fmt.Errorf("artisan %s: %w (%s)", strings.Join(args, " "), err, truncateStr(string(out), 1024))
		}
		return nil
	}
	if err := artisan(2*time.Minute, "key:generate", "--force"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := artisan(10*time.Minute, "migrate", "--force", "--seed"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	// NOTE: `artisan storage:link` is intentionally skipped. After the
	// public/ flatten there is no public/ dir for Laravel's default
	// public_path()/storage target, so the link errors; it only affects
	// serving uploaded files over /storage (e.g. a company logo), not the
	// invoicing core, and is a tracked follow-up.

	// Patch the seeded super-admin to the operator's identity + mark the
	// onboarding complete. The stock UsersTableSeeder created a working
	// admin/company/roles; we only rewrite the login credentials and the
	// company name, then flip profile_complete so the wizard is skipped.
	// Password + title travel via env (never argv) into the tinker script.
	tinker := `$u = App\Models\User::where('role','super admin')->first();` +
		`$u->email = '` + req.AdminEmail + `';` +
		`$u->password = getenv('IS_ADMIN_PASS');` +
		`$u->save();` +
		`$c = App\Models\Company::where('owner_id',$u->id)->first();` +
		`if($c){ $c->name = getenv('IS_SITE_TITLE'); $c->save(); }` +
		`App\Models\Setting::setSetting('profile_complete','COMPLETED');`
	tinkerCmd := buildSystemdRunCmdEnv(ctx, req.OSUser,
		[]string{"IS_ADMIN_PASS=" + req.AdminPass, "IS_SITE_TITLE=" + req.SiteTitle},
		php, filepath.Join(installPath, "artisan"), "tinker", "--execute="+tinker)
	tinkerCmd.Dir = installPath
	if out, err := runBoundedOutput(tinkerCmd, 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("finalise admin: %v (%s)", err, truncateStr(string(out), 1024))}
	}

	// Cache the route table (GH #897). This is Laravel's production
	// best-practice, and here it is load-bearing: jabali's Snuffleupagus
	// hooks override zend_execute_ex, which under the PHP-FPM SAPI defers
	// a discarded temporary's __destruct to script shutdown. Laravel
	// registers every Route::resource() from exactly that pattern
	// (PendingResourceRegistration::__destruct), so under FPM the CRUD
	// routes land AFTER route matching and every /api/v1/<resource> 404s
	// ("route could not be found" on each nav click). route:cache is built
	// here in the CLI SAPI — where destructor timing is correct — and FPM
	// then serves the baked table without re-running registration, so the
	// resource routes exist. Fatal on failure: without a valid cache the app
	// serves 404s on every CRUD route, so a broken cache is a broken install
	// and must surface here rather than ship silently.
	if err := artisan(2*time.Minute, "route:cache"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := writeInvoiceShelfNginx(ctx, domain, req.OSUser, req.Subdirectory); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("nginx snippet: %v", err)}
	}

	return invoiceshelfInstallResp{Version: invoiceshelfVersion}, nil
}

// envQuoted double-quotes a .env value and strips characters that would
// break the line (quotes, CR/LF). Passwords/titles never legitimately
// carry these.
func envQuoted(s string) string {
	s = strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(s)
	return "\"" + s + "\""
}

func invoiceshelfDownload(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, invoiceshelfTarballURL, nil)
	if err != nil {
		return fmt.Errorf("invoiceshelf download: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("invoiceshelf download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("invoiceshelf download: HTTP %d from %s", resp.StatusCode, invoiceshelfTarballURL)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("invoiceshelf download: create %s: %w", dest, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return fmt.Errorf("invoiceshelf download: copy: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != invoiceshelfTarballSHA {
		return fmt.Errorf("invoiceshelf download: SHA256 mismatch: got %s want %s", got, invoiceshelfTarballSHA)
	}
	return nil
}

func init() {
	RegisterAppInstaller("invoiceshelf", invoiceshelfInstallHandler)
}
