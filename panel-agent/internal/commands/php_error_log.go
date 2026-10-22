package commands

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

// PHP error log files (tech-support report 2026-10-22, newaramapp).
//
// Debian's php.ini sets log_errors=On and no error_log, so under FPM every
// warning goes to FastCGI stderr. nginx reads the stderr records into the same
// buffer as the response headers (fastcgi_buffer_size). It discards them while
// no stdout has arrived, but once a read holds the end of the warnings and the
// start of the headers, the buffer can fill before the header block ends:
// "upstream sent too big header while reading response header from upstream"
// and a 502. A site that is noisy but works elsewhere (34 "Undefined array key"
// warnings per request from one plugin) breaks after migration. Box-drilled on
// .60: 59 and 118 warnings return 502 on every request, the counts around them
// return 200.
//
// So every PHP request logs to a file in the tenant's ~/logs:
//
//   - the vhost pins error_log=/home/<user>/logs/<domain>.php-error.log in its
//     PHP_VALUE, which every PHP location and the app snippets' php-pins file
//     already carry (a value sent once sticks on the reused worker, so every
//     domain sends its own);
//   - the pool template sets php_value[error_log] = /home/<user>/logs/php-error.log
//     as the fallback for a fresh worker that serves anything else.
//
// Both are user-level values: a tenant's .user.ini or ini_set (WP_DEBUG_LOG)
// still picks another file, as before (box-checked: .user.ini beats PHP_VALUE).
// PHP opens the file per message as the tenant, so the file needs no reload,
// and a missing logs dir only means the old stderr path. ensureUserLogsDir
// makes the dir; logrotate rotates the files as the tenant
// (buildPHPErrorLogrotate).

// phpErrorLogDomainRE bounds the domain before it becomes part of a path. The
// panel validates domain names; this is the agent's own check.
var phpErrorLogDomainRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// phpErrorLogPath is the per-domain PHP error log, "" when the username or the
// domain is not safe in a path (no pin is rendered then).
func phpErrorLogPath(username, domain string) string {
	if !phpPoolUsernameRegex.MatchString(username) || !phpErrorLogDomainRE.MatchString(domain) || strings.Contains(domain, "..") {
		return ""
	}
	return "/home/" + username + "/logs/" + domain + ".php-error.log"
}

// withPHPErrorLog appends the error_log pin to a PHP_VALUE body. An empty body
// is a non-PHP vhost and stays empty; an empty path adds nothing.
func withPHPErrorLog(phpValue, path string) string {
	if phpValue == "" || path == "" {
		return phpValue
	}
	return phpValue + "\nerror_log=" + path
}

// phpErrorLogrotateDir is the host logrotate.d directory; a var so tests can
// point it at a temp dir.
var phpErrorLogrotateDir = "/etc/logrotate.d"

const phpErrorLogrotatePrefix = "jabali-phperr-"

func phpErrorLogrotatePath(username string) string {
	return filepath.Join(phpErrorLogrotateDir, phpErrorLogrotatePrefix+username)
}

// buildPHPErrorLogrotate renders the logrotate stanza for one tenant's PHP
// error logs: the per-domain files and the pool fallback (*php-error.log
// matches both, and never a rotated .1 or .gz).
//
// su <user> <user>: ~/logs is tenant-owned, so logrotate must not work in it
// as root, where a planted symlink would point root's rename, copy or compress
// at another file. As the tenant, a link only reaches what the tenant can
// already write. That is also why this is one file per tenant and not a glob
// in install/logrotate/jabali: su takes one user.
//
// nocreate and no copytruncate: PHP opens the file for each message, so after
// the rename the next message creates a fresh file, owned by the tenant.
func buildPHPErrorLogrotate(username string) string {
	return fmt.Sprintf(`# Managed by jabali (php.pool.apply, domain.create): PHP error logs of %[1]s.
# Rewritten when it differs and removed with the account.
/home/%[1]s/logs/*php-error.log {
    su %[1]s %[1]s
    daily
    rotate 7
    compress
    delaycompress
    missingok
    notifempty
    nocreate
}
`, username)
}

// writePHPErrorLogrotate writes the tenant's stanza when it differs, and only
// then removes the stanzas of users that no longer exist: pool apply and every
// PHP vhost write call this, and a sweep with one passwd lookup per stanza on
// each call would cost N^2 lookups when all N accounts re-render. Written in
// place, like the docker-app snippets: logrotate parses every file in the dir,
// and a temp file left beside it would be parsed too.
func writePHPErrorLogrotate(username string) error {
	if !phpPoolUsernameRegex.MatchString(username) {
		return fmt.Errorf("invalid username %q", username)
	}
	path := phpErrorLogrotatePath(username)
	want := []byte(buildPHPErrorLogrotate(username))
	if filesMatch(path, want) {
		return nil
	}
	if err := os.WriteFile(path, want, 0o644); err != nil {
		return fmt.Errorf("write logrotate stanza: %w", err)
	}
	reapStalePHPErrorLogrotate()
	return nil
}

// removePHPErrorLogrotate deletes the tenant's stanza. Best-effort: a missing
// file is not an error.
func removePHPErrorLogrotate(username string) {
	if !phpPoolUsernameRegex.MatchString(username) {
		return
	}
	_ = os.Remove(phpErrorLogrotatePath(username))
}

// reapStalePHPErrorLogrotate removes the stanza of every user the system no
// longer knows. logrotate treats an unknown su user as a config error: it
// skips the whole file and logrotate.service fails, which hides every other
// rotation error behind a unit that is always red (JAB-181). user.delete
// removes the stanza; this catches an account removed some other way.
func reapStalePHPErrorLogrotate() {
	matches, err := filepath.Glob(filepath.Join(phpErrorLogrotateDir, phpErrorLogrotatePrefix+"*"))
	if err != nil {
		return
	}
	for _, path := range matches {
		name := strings.TrimPrefix(filepath.Base(path), phpErrorLogrotatePrefix)
		if !phpPoolUsernameRegex.MatchString(name) {
			continue
		}
		_, err := user.Lookup(name)
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			if rmErr := os.Remove(path); rmErr == nil {
				log.Printf("php error log: removed the logrotate stanza of %s, a user that no longer exists", name)
			}
		}
	}
}
