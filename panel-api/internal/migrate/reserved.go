package migrate

import "strings"

// reservedAccounts are system / service logins that must never be treated as a
// migratable hosting account or be auto-created as a destination jabali user.
// The canonical failure this guards (GH #429): a Plesk/DirectAdmin migration
// run as the SSH principal `root` had no subscription-selection step, so
// job.SourceUser stayed "root"; the runner then defaulted the destination user
// to "root" and auto-created a bogus `root` jabali user while importing nothing.
var reservedAccounts = map[string]struct{}{
	"root": {}, "admin": {}, "administrator": {}, "daemon": {}, "bin": {},
	"sys": {}, "sync": {}, "games": {}, "man": {}, "lp": {}, "mail": {},
	"news": {}, "uucp": {}, "proxy": {}, "www-data": {}, "backup": {},
	"list": {}, "irc": {}, "gnats": {}, "nobody": {}, "sshd": {}, "clp": {},
	"mysql": {}, "postgres": {}, "systemd-network": {}, "systemd-resolve": {},
}

// IsReservedAccount reports whether name is a system / service login that must
// never be migrated as a hosting account or auto-created as a destination user.
// Case-insensitive; also treats any `systemd-*` login as reserved.
func IsReservedAccount(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	if _, ok := reservedAccounts[n]; ok {
		return true
	}
	return strings.HasPrefix(n, "systemd-")
}
