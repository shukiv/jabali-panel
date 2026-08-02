package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var logrotateSuRe = regexp.MustCompile(`(?m)^\s*su\s+(\S+)\s+(\S+)\s*$`)

// TestLogrotateNeverDropsToNonRootUser guards a failure that is silent in
// exactly the way that matters: logs growing without bound.
//
// The php-fpm block carried `su www-data www-data`. That glob covers per-tenant
// pool logs, each owned <tenant>:<tenant> mode 0640, so www-data is neither the
// owner nor in the group and logrotate could not open a single one:
//
//	error: error opening /var/log/php-fpm-cybe2e.log: Permission denied
//
// Consequences on a real host: every per-tenant php-fpm log never rotated —
// `maxsize 50M` never applied — and logrotate.service exited 1 on every daily
// run, leaving a permanently-failed unit that would mask any other rotation
// problem. Only php-fpm-pma.log rotated, because it happens to be www-data's.
//
// Verified by forcing a rotation both ways on that host: `su www-data www-data`
// produced Permission denied for every tenant log and rotated none of them,
// while `su root root` rotated them with no errors and left the live log still
// owned by the tenant, so php-fpm kept writing.
//
// The invariant is that the su USER stays root. logrotate's su exists to
// protect against a log DIRECTORY writable by a non-root user; everything this
// file manages lives under root:root 0755 /var/log, so root is both correct and
// no weaker. The group is not constrained — `su root adm` is fine, since it is
// the uid that decides whether open() succeeds.
func TestLogrotateNeverDropsToNonRootUser(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	path := filepath.Join(root, "install", "logrotate", "jabali")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	matches := logrotateSuRe.FindAllStringSubmatch(string(body), -1)
	if len(matches) == 0 {
		t.Fatal("found no su directives in install/logrotate/jabali — either " +
			"they were removed (every block needs one) or the pattern drifted " +
			"and this test is checking nothing")
	}

	for _, m := range matches {
		if m[1] != "root" {
			t.Errorf("su directive drops to user %q. Files under these globs can "+
				"be owned by individual tenants (php-fpm pool logs are "+
				"<tenant>:<tenant> 0640), so any non-root user hits Permission "+
				"denied, rotates nothing, and fails logrotate.service daily. "+
				"Use `su root %s`.", m[1], m[2])
		}
	}
}

// TestLogrotatePerTenantBlocksUseCopytruncate pins the other half of what makes
// rotating a tenant-owned log safe.
//
// With copytruncate the live file is never replaced, so it keeps its inode,
// mode and tenant ownership and php-fpm goes on writing to the same descriptor.
// Rotating these as root WITHOUT copytruncate would create the replacement as
// root:root and silently cut the tenant's pool off from its own log.
func TestLogrotatePerTenantBlocksUseCopytruncate(t *testing.T) {
	t.Parallel()

	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(repoRoot, "install", "logrotate", "jabali"))
	if err != nil {
		t.Fatalf("read logrotate config: %v", err)
	}

	const glob = "/var/log/php-fpm-*.log {"
	start := strings.Index(string(body), glob)
	if start < 0 {
		t.Fatalf("could not find the %q block", glob)
	}
	block := string(body)[start:]
	if end := strings.Index(block, "\n}"); end > 0 {
		block = block[:end]
	}

	if !strings.Contains(block, "copytruncate") {
		t.Error("the php-fpm block rotates tenant-owned logs as root; without " +
			"copytruncate the replacement file is created root:root and the " +
			"tenant's pool loses write access to its own log")
	}
}

// thirdPartyOwnedLogPaths are log files whose rotation is declared by a config
// file jabali does not own. maldet (LMD) installs /etc/logrotate.d/maldet
// naming these three explicitly.
var thirdPartyOwnedLogPaths = []string{
	"/var/log/maldet/event_log",
	"/var/log/maldet/clamscan_log",
	"/var/log/maldet/audit.log",
}

// logrotateDeclaredPaths extracts the log paths a logrotate config declares.
// A stanza is one or more path lines followed by a line opening `{`, so every
// non-comment, non-directive line up to the brace is a declared path.
func logrotateDeclaredPaths(body string) []string {
	var paths []string
	inBlock := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if inBlock {
			if line == "}" {
				inBlock = false
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if open := strings.Index(line, "{"); open >= 0 {
			inBlock = true
			line = strings.TrimSpace(line[:open])
			if line == "" {
				continue
			}
		}
		paths = append(paths, line)
	}
	return paths
}

// TestLogrotateDoesNotClaimThirdPartyLogs guards a whole-host outage mode that
// looks like a jabali-local mistake and is not.
//
// logrotate treats one path declared by two config files as a HARD error: it
// reports `duplicate log entry` and skips the ENTIRE second file, then exits
// non-zero. /etc/logrotate.d is read in name order, so `jabali` claiming a path
// that `maldet` also declares means maldet's whole config is discarded —
// event_log and clamscan_log stop rotating, and logrotate.service sits
// permanently failed, masking every unrelated rotation error behind noise.
//
// Reproduced on a box before the fix (JAB-181):
//
//	# logrotate -d /etc/logrotate.conf ; echo $?
//	error: maldet:1 duplicate log entry for /var/log/maldet/audit.log
//	error: found error in file maldet, skipping
//	1
//
// The cause was a `/var/log/maldet/*.log` glob here, which matches audit.log.
// Globs make this easy to reintroduce without noticing, so match them properly
// rather than comparing literal strings.
func TestLogrotateDoesNotClaimThirdPartyLogs(t *testing.T) {
	t.Parallel()

	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(repoRoot, "install", "logrotate", "jabali"))
	if err != nil {
		t.Fatalf("read logrotate config: %v", err)
	}

	declared := logrotateDeclaredPaths(string(body))
	if len(declared) == 0 {
		t.Fatal("parsed no log paths out of install/logrotate/jabali — the " +
			"config or this parser drifted, so the test is checking nothing")
	}

	for _, pattern := range declared {
		for _, owned := range thirdPartyOwnedLogPaths {
			match, err := filepath.Match(pattern, owned)
			if err != nil {
				t.Fatalf("malformed pattern %q in install/logrotate/jabali: %v", pattern, err)
			}
			if match {
				t.Errorf("install/logrotate/jabali declares %q, which claims %s — "+
					"that path is already declared by a config jabali does not own. "+
					"logrotate reports `duplicate log entry`, SKIPS that entire "+
					"config file and exits 1, so its other logs stop rotating and "+
					"logrotate.service stays failed host-wide (JAB-181). Declare "+
					"only paths the third-party config misses.", pattern, owned)
			}
		}
	}
}
