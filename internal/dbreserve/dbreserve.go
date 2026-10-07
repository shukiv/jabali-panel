// Package dbreserve names this server's own MariaDB/PostgreSQL databases and
// database accounts: the engines' system schemas and accounts, and every
// jabali service one. No account owns them, so nothing done for an account —
// creating, granting, dropping, rotating a password, restoring a backup —
// may touch them.
//
// The panel and the agent both check names against it (single go module, same
// convention as internal/hostreserve), so the two lists can't drift.
package dbreserve

import "strings"

var (
	databases = map[string]bool{
		"mysql": true, "information_schema": true, "performance_schema": true, "sys": true,
		"crowdsec": true, "postgres": true, "template0": true, "template1": true, "jabali": true,
	}
	users = map[string]bool{
		"root": true, "mysql": true, "mariadb.sys": true, "crowdsec": true, "debian-sys-maint": true,
		"postgres": true, "jabali": true,
	}
)

// Database reports whether name is one of this server's own databases: a
// system schema or any jabali_* one.
func Database(name string) bool {
	l := strings.ToLower(name)
	return databases[l] || strings.HasPrefix(l, "jabali_")
}

// User reports whether name is one of this server's own database accounts: a
// system account, any jabali* service account, or a jb_s_* restore shadow
// account (the agent's scoped database loader).
func User(name string) bool {
	l := strings.ToLower(name)
	return users[l] || strings.HasPrefix(l, "jabali_") || strings.HasPrefix(l, "jabali-") || strings.HasPrefix(l, "jb_s_")
}
