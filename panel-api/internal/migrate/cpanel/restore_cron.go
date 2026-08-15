package cpanel

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/shlex"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// CronImportResult is returned by ImportCron for restore-stage
// progress reporting + manifest update. Imported rows land disabled
// (Enabled=false); operator reviews + flips on after migration.
type CronImportResult struct {
	Created int
	Skipped []string // parse / allowlist failures with reason
}

// ImportCron walks each cPanel crontab file in the parsed tarball
// and inserts one cron_jobs row per parseable line. Lines that fail
// the cronvalidate command allowlist (cPanel users routinely run
// /usr/bin/php scripts at absolute paths or wget cron-trigger URLs;
// jabali's allowlist is wp / php only) are recorded in Skipped so
// the operator can manually re-enter them via the UI after
// migration.
//
// All inserted rows are Enabled=false — reconciler never schedules
// them until the operator reviews + flips. This is the safety
// gate: a malicious crontab in a compromised cPanel tarball can't
// land an active job by reaching restore.
//
// targetUserID + targetUsername are the destination jabali identity
// the restore stage created moments earlier. cronvalidate needs
// the username's owned-docroots list to validate `--path` args;
// for migration v1 we pass an empty list (rejects every wp/php
// command requiring a docroot path) and rely on the operator
// re-entering the cron after migration when the docroots are
// final. Trade-off: import skips most cPanel crons up front,
// avoids the false-positive of inserting a cron that points at a
// path that doesn't exist yet.
func ImportCron(ctx context.Context, repo repository.CronJobRepository, parsed *ParsedTarball, targetUserID, targetUsername string) (*CronImportResult, error) {
	if repo == nil {
		return nil, fmt.Errorf("ImportCron: repo nil")
	}
	if parsed == nil {
		return nil, fmt.Errorf("ImportCron: parsed nil")
	}
	if targetUserID == "" {
		return nil, fmt.Errorf("ImportCron: targetUserID empty")
	}
	res := &CronImportResult{}

	// GH #429 E2E finding: import re-runs are a designed flow ("re-run
	// after fixing, or pass --allow-degraded"), and every re-run used to
	// duplicate the whole imported cron set. Dedupe against the target
	// user's existing rows by (schedule, command) — the identity of an
	// imported line — so a re-run is a clean noop for already-imported
	// crons.
	existing := map[string]bool{}
	if rows, lErr := repo.ListByUserID(ctx, targetUserID); lErr == nil {
		for _, r := range rows {
			existing[r.Schedule+"\x00"+r.Command] = true
		}
	}

	// The account's own domains + their DEST docroots. Used by the
	// curl/wget self-target rewrite for (a) the same-origin host gate
	// and (b) resolving the URL path to a php file under the docroot.
	// ImportCron runs AFTER ImportHomeSplit + ImportDomains (the caller
	// reorders it there) so these dest paths exist on disk.
	docroots := accountDocroots(parsed, targetUsername)
	ownedDocroots := make([]string, 0, len(docroots))
	for _, dr := range docroots {
		ownedDocroots = append(ownedDocroots, dr)
	}

	for _, cronPath := range parsed.CronFiles {
		f, err := os.Open(cronPath)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("open %s: %v", cronPath, err))
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 4096), 64*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// Crontab environment-assignment lines (MAILTO="",
			// SHELL=/bin/bash, PATH=…) are valid crontab syntax, not
			// malformed cron entries. Recognise + skip them with an
			// info note instead of flagging "malformed". jabali's
			// systemd-user-timer cron model has no per-job env knob,
			// so the values are dropped — recorded so the operator
			// knows (e.g. a custom PATH a script relied on).
			if isCronEnvLine(line) {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s:%d env_ignored: %s", cronPath, lineNum, line))
				continue
			}
			schedule, command, ok := splitCronLine(line)
			if !ok {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s:%d malformed", cronPath, lineNum))
				continue
			}
			// Schedule + command pass through the same validator
			// the REST handler runs. Empty docroots → strict mode
			// (any --path arg rejected). v1 trade-off captured in
			// the function comment.
			if vErr := cronvalidate.ValidateSchedule(schedule); vErr != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s:%d schedule: %s", cronPath, lineNum, vErr.Error()))
				continue
			}

			// Strip a trailing output-discard redirect (>/dev/null,
			// >/dev/null 2>&1, &>/dev/null, …). Real-world crons almost
			// always carry one, and cronvalidate's metachar guard rejects
			// any '>' before it ever reaches the binary check — so without
			// this even a clean `php …/wp-cron.php >/dev/null 2>&1` fails.
			// systemd-user routes stdout/stderr to journald regardless, so
			// dropping the discard is behaviour-preserving. Only /dev/null
			// + fd-dup targets are stripped; a redirect to a real file is
			// left intact (it'll fail validation → disabled-import, which
			// is correct — we don't support file-output crons).
			origCommand := command
			command = stripOutputRedirect(command)

			// curl/wget self-targeting the account's own site is the most
			// common cPanel cron (WordPress scheduler etc.). Rewrite it to
			// a local `php <docroot>/<file>.php` exec when it provably
			// targets one of THIS account's domains + a real .php under the
			// docroot. Converting to a local exec also eliminates the SSRF
			// surface (no network call survives). Anything that doesn't
			// qualify is imported DISABLED with the original command + a
			// note — never silently dropped.
			if firstToken := firstCronToken(command); firstToken == "curl" || firstToken == "wget" {
				rewritten, ok, reason := rewriteSelfCurlCron(command, docroots)
				if !ok {
					res.Skipped = append(res.Skipped, fmt.Sprintf(
						"%s:%d cron_disabled_import (curl_not_rewritable: %s): %s",
						cronPath, lineNum, reason, origCommand))
					if cErr := createCronRowDeduped(ctx, repo, targetUserID, schedule, origCommand, res, existing, cronPath, lineNum); cErr != nil {
						_ = f.Close()
						return res, cErr
					}
					continue
				}
				// Defense-in-depth: never trust the rewrite — re-run it
				// through the same validator the REST handler uses, now
				// with the account's real docroots so the php path passes.
				if _, vErr := cronvalidate.ValidateCommand(rewritten, ownedDocroots); vErr != nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf(
						"%s:%d cron_disabled_import (rewrite_revalidate_failed: %s): %s",
						cronPath, lineNum, vErr.Error(), origCommand))
					if cErr := createCronRowDeduped(ctx, repo, targetUserID, schedule, origCommand, res, existing, cronPath, lineNum); cErr != nil {
						_ = f.Close()
						return res, cErr
					}
					continue
				}
				res.Skipped = append(res.Skipped, fmt.Sprintf(
					"%s:%d cron rewritten curl→php: %s → %s", cronPath, lineNum, origCommand, rewritten))
				if cErr := createCronRowDeduped(ctx, repo, targetUserID, schedule, rewritten, res, existing, cronPath, lineNum); cErr != nil {
					_ = f.Close()
					return res, cErr
				}
				continue
			}

			// Non-curl/wget: validate against the account's docroots.
			// Pass → row. Fail → disabled-import (preserve original + note).
			if _, vErr := cronvalidate.ValidateCommand(command, ownedDocroots); vErr != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf(
					"%s:%d cron_disabled_import (command: %s): %s",
					cronPath, lineNum, vErr.Error(), origCommand))
				if cErr := createCronRowDeduped(ctx, repo, targetUserID, schedule, origCommand, res, existing, cronPath, lineNum); cErr != nil {
					_ = f.Close()
					return res, cErr
				}
				continue
			}
			if cErr := createCronRowDeduped(ctx, repo, targetUserID, schedule, command, res, existing, cronPath, lineNum); cErr != nil {
				_ = f.Close()
				return res, cErr
			}
		}
		if err := scanner.Err(); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("scan %s: %v", cronPath, err))
		}
		_ = f.Close()
	}
	return res, nil
}

// splitCronLine cuts a standard 5-field crontab line into
// '<min hr dom mon dow>' + '<command>'. Returns (schedule, command,
// true) on success; (_, _, false) when the line has fewer than 6
// whitespace-separated fields.
//
// cPanel allows the @reboot / @daily / @hourly aliases too. We
// accept those by detecting a leading '@' and treating the first
// token as the entire schedule.
func splitCronLine(line string) (schedule, command string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", "", false
	}
	if strings.HasPrefix(fields[0], "@") {
		if len(fields) < 2 {
			return "", "", false
		}
		return fields[0], strings.Join(fields[1:], " "), true
	}
	if len(fields) < 6 {
		return "", "", false
	}
	return strings.Join(fields[:5], " "), strings.Join(fields[5:], " "), true
}

// cronEnvLineRe matches a crontab environment-assignment line:
// NAME=value or NAME = value, where NAME is a shell-style identifier.
// vixie-cron treats any line whose first token is `NAME =` (optional
// spaces around =) as an env setting, not a job.
var cronEnvLineRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*[ \t]*=`)

// isCronEnvLine reports whether line is a crontab environment
// assignment (MAILTO, SHELL, PATH, HOME, …) rather than a cron job.
func isCronEnvLine(line string) bool {
	return cronEnvLineRe.MatchString(line)
}

// createCronRow inserts one imported cron_jobs row (always Enabled=false
// — the operator reviews + flips after migration) and bumps res.Created.
// createCronRowDeduped skips (with a note) when an identical
// (schedule, command) row already exists for the user — see the re-run
// dedupe in ImportCron.
func createCronRowDeduped(ctx context.Context, repo repository.CronJobRepository, userID, schedule, command string, res *CronImportResult, existing map[string]bool, cronPath string, lineNum int) error {
	key := schedule + "\x00" + command
	if existing[key] {
		res.Skipped = append(res.Skipped, fmt.Sprintf("%s:%d already_imported (re-run dedupe): %s", cronPath, lineNum, command))
		return nil
	}
	if err := createCronRow(ctx, repo, userID, schedule, command, res); err != nil {
		return err
	}
	existing[key] = true
	return nil
}

func createCronRow(ctx context.Context, repo repository.CronJobRepository, userID, schedule, command string, res *CronImportResult) error {
	row := &models.CronJob{
		ID:        ids.NewULID(),
		UserID:    userID,
		Name:      fmt.Sprintf("imported-%d", res.Created+1),
		Command:   command,
		Schedule:  schedule,
		Enabled:   false,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, row); err != nil {
		return fmt.Errorf("create cron_jobs row: %w", err)
	}
	res.Created++
	return nil
}

// accountDocroots maps each of the account's own domains (lowercased) to
// its DEST docroot. Mirrors ImportDomains' docRootFor: a DocRoots
// override wins, else the jabali convention
// /home/<user>/domains/<domain>/public_html. Domains come from the BIND
// zone basenames when present, else DomainNames.
func accountDocroots(parsed *ParsedTarball, targetUsername string) map[string]string {
	out := map[string]string{}
	add := func(name string) {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			return
		}
		dr := filepath.Join("/home", targetUsername, "domains", name, "public_html")
		if parsed.DocRoots != nil {
			if o, ok := parsed.DocRoots[name]; ok && o != "" {
				dr = o
			}
		}
		out[name] = dr
	}
	if len(parsed.ZoneFiles) > 0 {
		for _, z := range parsed.ZoneFiles {
			add(strings.TrimSuffix(filepath.Base(z), ".db"))
		}
	}
	for _, n := range parsed.DomainNames {
		add(n)
	}
	return out
}

// firstCronToken returns the basename of the first shell token of a
// command (so "/usr/bin/curl" → "curl"). Empty on parse failure.
func firstCronToken(command string) string {
	argv, err := shlex.Split(command)
	if err != nil || len(argv) == 0 {
		return ""
	}
	return filepath.Base(argv[0])
}

// outputRedirectRe matches a trailing run of output-DISCARD redirects:
// >/dev/null, >>/dev/null, 2>/dev/null, 2>&1, 1>&2, &>/dev/null, … in any
// order at the end of the command. Redirects to a real file are NOT
// matched (left intact so they fail validation → disabled-import).
var outputRedirectRe = regexp.MustCompile(`(?:\s*(?:[12]?>>?\s*/dev/null|[12]?>>?\s*&[12]|&>>?\s*/dev/null))+\s*$`)

// stripOutputRedirect removes a trailing output-discard redirect.
func stripOutputRedirect(command string) string {
	return strings.TrimSpace(outputRedirectRe.ReplaceAllString(command, ""))
}

// benignCurlFlags is the tiny allow-list of curl/wget flags the rewrite
// tolerates — silence + fail-fast only. Anything else (writes a file,
// uploads, sets a proxy, follows redirects, sends data) disqualifies the
// rewrite so we never convert a data-moving cron into a blind php exec.
var benignCurlFlags = map[string]bool{
	"-s": true, "--silent": true,
	"-f": true, "--fail": true,
	"-q": true,
}

// rewriteSelfCurlCron converts a curl/wget command that self-targets one
// of the account's own domains into a local `php <docroot>/<file>.php`
// exec. Returns (rewritten, true, "") on success, or ("", false, reason)
// when any rule fails (caller imports the original DISABLED). All rules
// in the spec must hold:
//  1. shlex-parse; first token curl/wget (already checked by caller).
//  2. exactly one http(s):// URL; scheme http/https only.
//  3. URL host is one of the account's own domains (same-origin / SSRF gate).
//  4. no query string (php-CLI ignores $_GET — silent behaviour change).
//  5. path resolves under the docroot, ends .php, no traversal, exists.
//  6. only benign flags; -o/-O permitted only with /dev/null.
func rewriteSelfCurlCron(command string, docroots map[string]string) (string, bool, string) {
	argv, err := shlex.Split(command)
	if err != nil || len(argv) == 0 {
		return "", false, "shlex_parse_failed"
	}
	bin := filepath.Base(argv[0])
	if bin != "curl" && bin != "wget" {
		return "", false, "not_curl_wget"
	}

	var rawURL string
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://"):
			if rawURL != "" {
				return "", false, "multiple_urls"
			}
			rawURL = a
		case a == "-o" || a == "-O":
			// output flag — only the /dev/null discard form is allowed.
			if i+1 >= len(argv) || argv[i+1] != "/dev/null" {
				return "", false, "danger_flag:" + a
			}
			i++ // consume the /dev/null value
		case strings.HasPrefix(a, "-"):
			if !benignCurlFlags[a] {
				return "", false, "danger_flag:" + a
			}
		default:
			return "", false, "unexpected_arg:" + a
		}
	}
	if rawURL == "" {
		return "", false, "no_url"
	}

	u, perr := url.Parse(rawURL)
	if perr != nil {
		return "", false, "url_parse_failed"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false, "bad_scheme:" + u.Scheme
	}
	if u.RawQuery != "" {
		return "", false, "has_query_string"
	}
	host := strings.ToLower(u.Hostname())
	docroot, ok := docroots[host]
	if !ok {
		return "", false, "host_not_owned:" + host
	}

	upath := u.Path
	if upath == "" || upath == "/" {
		return "", false, "empty_path"
	}
	if strings.Contains(upath, "..") {
		return "", false, "path_traversal"
	}
	full := filepath.Join(docroot, filepath.Clean("/"+upath))
	// Containment: the cleaned path must stay under the docroot.
	if full != docroot && !strings.HasPrefix(full, docroot+string(filepath.Separator)) {
		return "", false, "escapes_docroot"
	}
	if !strings.HasSuffix(full, ".php") {
		return "", false, "not_php"
	}
	if st, serr := os.Stat(full); serr != nil || st.IsDir() {
		return "", false, "file_not_found"
	}
	return "php " + full, true, ""
}
