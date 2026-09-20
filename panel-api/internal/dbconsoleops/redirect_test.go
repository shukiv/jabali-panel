package dbconsoleops

import "testing"

// sampleToken uses only base64url characters (A-Za-z0-9-_), the alphabet
// sso.Service.MintToken emits via base64.RawURLEncoding. None of these need
// percent-encoding, so url.Values.Encode leaves the token byte-identical to
// the previous inline "?token=" + token concatenation.
const sampleToken = "AbC-dEf_012GhIjK"

func TestPhpMyAdminRedirect(t *testing.T) {
	base := "https://mx.jabali-panel.com:8443"
	tests := []struct {
		name string
		db   string
		want string
	}{
		{
			// Tenant + CLI phpMyAdmin: a real database in scope. Byte-identical
			// to the previous sso_phpmyadmin.go / db_sso_cmd.go output.
			name: "db-scoped",
			db:   "acme_wp",
			want: base + "/phpmyadmin/sso.php?db=acme_wp&token=" + sampleToken,
		},
		{
			// Privileged admin-all console (ssoAdminAllSentinel, empty dbName):
			// no database to encode. Byte-identical to databases_admin_ops.go:447.
			name: "admin-all",
			db:   "",
			want: base + "/phpmyadmin/sso.php?token=" + sampleToken,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PhpMyAdminRedirect(base, sampleToken, tc.db); got != tc.want {
				t.Errorf("PhpMyAdminRedirect(db=%q)\n got %q\nwant %q", tc.db, got, tc.want)
			}
		})
	}
}

func TestAdminerRedirect(t *testing.T) {
	base := "https://mx.jabali-panel.com:8443"
	tests := []struct {
		name   string
		db     string
		engine string
		want   string
	}{
		{
			// Tenant Adminer: db + engine. This is the reference shape the
			// browser-tested path already emits; the other Adminer doors align
			// to it. Keys are sorted by url.Values.Encode: db, engine, token.
			name:   "db-scoped",
			db:     "acme_pg",
			engine: "postgres",
			want:   base + "/jabali-adminer/?db=acme_pg&engine=postgres&token=" + sampleToken,
		},
		{
			// Privileged admin-all Adminer (ssoAdminAllSentinel, engine
			// "postgres"): no database, engine present. INTENTIONAL DELTA vs
			// databases_admin_ops.go:652, which emitted "?token=" only — engine
			// is now encoded so the engine scope is identical across adapters
			// (AC3). Cosmetic: the console reads only the token.
			name:   "admin-all",
			db:     "",
			engine: "postgres",
			want:   base + "/jabali-adminer/?engine=postgres&token=" + sampleToken,
		},
		{
			// CLI Adminer with db + engine. INTENTIONAL DELTA vs the old
			// db_sso_cmd.go, which emitted "?token=" only — it now matches the
			// tenant Adminer shape. Cosmetic: the console reads only the token.
			name:   "cli-aligned",
			db:     "user_pgdb",
			engine: "postgres",
			want:   base + "/jabali-adminer/?db=user_pgdb&engine=postgres&token=" + sampleToken,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AdminerRedirect(base, sampleToken, tc.db, tc.engine); got != tc.want {
				t.Errorf("AdminerRedirect(db=%q,engine=%q)\n got %q\nwant %q", tc.db, tc.engine, got, tc.want)
			}
		})
	}
}
