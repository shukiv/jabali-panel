package cpanel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"git.linux-hosting.co.il/shukivaknin/jabali2/internal/cronvalidate"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/ids"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/models"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/repository"
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
func ImportCron(ctx context.Context, repo repository.CronJobRepository, parsed *ParsedTarball, targetUserID string) (*CronImportResult, error) {
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
			if _, vErr := cronvalidate.ValidateCommand(command, nil); vErr != nil {
				// curl/wget hitting a wp-cron.php URL is the single most
				// common cPanel cron (the WordPress scheduler). The raw
				// allowlist rejection ("binary_not_allowed: first token
				// must be 'wp' or 'php'") is correct but useless to the
				// operator. Emit an actionable remediation instead so
				// they know the WP scheduler was skipped + how to restore
				// it. (A true auto-rewrite to `php <docroot>/wp-cron.php`
				// can't run in v1 — migration passes empty ownedDocroots
				// to the validator, which rejects any --path, so the
				// rewritten command wouldn't validate either; tracked for
				// a follow-up that resolves the dest docroot first.)
				var ve *cronvalidate.ValidationError
				if errors.As(vErr, &ve) && ve.Code == cronvalidate.ErrCodeBinaryNotAllowed && isWPCronTrigger(command) {
					res.Skipped = append(res.Skipped, fmt.Sprintf(
						"%s:%d wp_scheduler_skipped: %q — WordPress cron. Re-add via the Cron page as 'php <docroot>/wp-cron.php', or leave WP's built-in pseudo-cron to run on page hits.",
						cronPath, lineNum, command))
					continue
				}
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s:%d command: %s", cronPath, lineNum, vErr.Error()))
				continue
			}

			row := &models.CronJob{
				ID:        ids.NewULID(),
				UserID:    targetUserID,
				Name:      fmt.Sprintf("imported-%d", res.Created+1),
				Command:   command,
				Schedule:  schedule,
				Enabled:   false, // operator reviews + flips
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}
			if err := repo.Create(ctx, row); err != nil {
				_ = f.Close()
				return res, fmt.Errorf("create cron_jobs row (line %d of %s): %w", lineNum, cronPath, err)
			}
			res.Created++
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

// wpCronTriggerRe matches a curl/wget command whose URL ends in
// wp-cron.php (optionally with a ?query). Used only to turn the
// allowlist rejection into an actionable "WordPress scheduler skipped"
// note — it does not rewrite or schedule anything.
var wpCronTriggerRe = regexp.MustCompile(`(?i)(?:curl|wget).*/wp-cron[.]php`)

// isWPCronTrigger reports whether command is a curl/wget WordPress-cron
// trigger.
func isWPCronTrigger(command string) bool {
	return wpCronTriggerRe.MatchString(command)
}
