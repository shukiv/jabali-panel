package dbconsoleops

import "net/url"

// Console entrypoint paths. Both are served by nginx on the console vhost;
// the panel only builds the handoff URL that points at them.
const (
	phpMyAdminSSOPath = "/phpmyadmin/sso.php"
	adminerSSOPath    = "/jabali-adminer/"
)

// consoleRedirect builds a single-use DB-console login URL.
//
// base is the already-resolved absolute base (scheme://host[:port]) for the
// console vhost. Adapters resolve it their own way and it is passed in
// verbatim: REST from the Origin/Referer/config chain, CLI from config or the
// panel hostname (per ADR-0083, base-URL/identity resolution stays an adapter
// concern). path is the console entrypoint.
//
// token is the minted single-use handoff token (base64url, always present).
// db and engine are scope hints echoed into the query; each is omitted when
// empty — an admin-all console (the privileged doors mint with
// ssoAdminAllSentinel) carries no specific database, and phpMyAdmin never
// carries an engine.
//
// The consoles read ONLY the token: install/phpmyadmin/sso.php takes
// $_GET['token'] and install/adminer/jabali-sso-plugin.php takes $_GET['token'],
// and both derive the database + driver from the token via the panel validate
// endpoint. So db/engine are cosmetic for login — encoding them through this
// one function is what keeps a database-scoped Adminer request identical across
// every adapter (AC3) and stops the scope encoding silently drifting between
// the REST, privileged, and CLI doors.
func consoleRedirect(base, path, token, db, engine string) string {
	q := url.Values{}
	q.Set("token", token)
	if db != "" {
		q.Set("db", db)
	}
	if engine != "" {
		q.Set("engine", engine)
	}
	// url.Values.Encode sorts keys, so the query is deterministic across
	// adapters: db, engine, token. base already carries no trailing slash
	// (resolvers TrimSuffix), matching the previous inline concatenation
	// byte-for-byte.
	return base + path + "?" + q.Encode()
}

// PhpMyAdminRedirect builds the phpMyAdmin console handoff URL. Pass db="" for
// the admin-all console (no single database in scope).
func PhpMyAdminRedirect(base, token, db string) string {
	return consoleRedirect(base, phpMyAdminSSOPath, token, db, "")
}

// AdminerRedirect builds the Adminer console handoff URL. Pass db="" for the
// admin-all console; engine is the normalized engine ("postgres" today —
// Adminer fronts PostgreSQL in Jabali).
func AdminerRedirect(base, token, db, engine string) string {
	return consoleRedirect(base, adminerSSOPath, token, db, engine)
}
