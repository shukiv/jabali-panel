// Phase 3 (GH #954) — the Path-Y transport half of the Jabali→Jabali importer.
//
// Both sides are Jabali, so instead of re-implementing resource recreation we
// reuse the account backup/restore engine (the same one #331 DR uses). This file
// drives the SOURCE's own `jabali backup …` CLI over the existing SSH session to
// produce a restic account_backup of one user, then streams the repo (and the
// source box's restic password) back to the dest staging dir. The dest-side
// import arm (migrate_run_cmd.go) then restores it via the DR account-restore
// path — reading the foreign-password repo through backup.restore's password_file
// field (no rekey, no repo mutation).
//
// Everything here is scoped to a per-job throwaway destination/schedule/repo on
// the source that CleanupSource removes afterwards; the account's own data and
// schedules are never touched.
package jabali

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// sourceBoxPasswordPath is the source box's box-wide restic password. Local
// backup destinations encrypt their repo with it; it is root-readable over SSH
// and carried to the dest so the import can open the pulled repo.
const sourceBoxPasswordPath = "/etc/jabali-panel/restic-repo.password"

// SourceBackup records the throwaway artifacts RunSourceBackup created on the
// source, so the caller can pull the repo and then remove them via CleanupSource.
type SourceBackup struct {
	DestName   string // backup destination name created on the source
	DestID     string // its ULID (best-effort; parsed from --json)
	ScheduleID string // account_backup schedule ULID
	RepoDir    string // absolute restic repo dir on the source
}

// shortID lowercases the first 8 chars of a ULID for use in per-job source
// destination/schedule names + the repo dir. 8 chars is unique enough for a
// throwaway name and keeps the shell argument short + shape-safe.
func shortID(jobID string) string {
	s := strings.ToLower(jobID)
	if len(s) > 8 {
		s = s[:8]
	}
	if s == "" {
		s = "job"
	}
	return s
}

// RunSourceBackup drives the source's OWN jabali CLI to back up one account into
// a fresh local restic destination, then waits for the manifest snapshot to land.
// Read+write on the source, but every write is a per-job throwaway CleanupSource
// removes. Returns the SourceBackup even on error so the caller can still clean
// up whatever got created.
func RunSourceBackup(ctx context.Context, raw migrate.Session, sourceUser, jobID string) (*SourceBackup, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("jabali.RunSourceBackup: wrong session type")
	}
	if !usernameRe.MatchString(sourceUser) {
		return nil, fmt.Errorf("jabali.RunSourceBackup: invalid source user %q", sourceUser)
	}
	short := shortID(jobID)
	sb := &SourceBackup{
		DestName: "migrate-" + short,
		RepoDir:  "/var/lib/jabali-migrate-src/" + short,
	}

	// 1. Create the repo dir (jabali-owned, 0750 — matching what the CLI's
	//    local-dest preflight expects) + register a local destination over it.
	mk := "install -d -o jabali -g jabali -m 0750 " + shellSingleQuote(sb.RepoDir)
	if _, err := s.run(ctx, s.commandTimeout, mk); err != nil {
		return sb, fmt.Errorf("create source repo dir %s: %w", sb.RepoDir, err)
	}
	destCreate := "jabali backup destination create --name " + shellSingleQuote(sb.DestName) +
		" --kind local --url " + shellSingleQuote(sb.RepoDir) + " --json"
	out, err := s.run(ctx, s.commandTimeout, destCreate)
	if err != nil {
		return sb, fmt.Errorf("source backup destination create: %w", err)
	}
	var destRow struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(out, &destRow) // id is best-effort; cleanup can use the name
	sb.DestID = destRow.ID

	// 2. Create an account_backup schedule restricted to just this user → dest.
	schedCreate := "jabali backup schedule create --kind account_backup --user " +
		shellSingleQuote(sourceUser) + " --destination " + shellSingleQuote(sb.DestName) +
		" --cron '0 3 * * *' --json"
	out, err = s.run(ctx, s.commandTimeout, schedCreate)
	if err != nil {
		return sb, fmt.Errorf("source schedule create: %w", err)
	}
	var schedRow struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(out, &schedRow); e != nil || schedRow.ID == "" {
		return sb, fmt.Errorf("source schedule create: could not read schedule id from output %q", strings.TrimSpace(string(out)))
	}
	sb.ScheduleID = schedRow.ID

	// 3. Fire it now, then tick + poll until the manifest snapshot appears.
	if _, err := s.run(ctx, s.commandTimeout, "jabali backup schedule run-now "+shellSingleQuote(sb.ScheduleID)); err != nil {
		return sb, fmt.Errorf("source schedule run-now: %w", err)
	}
	if err := s.waitForManifest(ctx, sb.RepoDir); err != nil {
		return sb, err
	}
	return sb, nil
}

// manifestPollInterval / manifestMaxWait bound the tick-and-poll loop. Big
// accounts back up for many minutes; the pull command's 90m context is the hard
// ceiling, this is the softer per-account budget. Vars (not consts) so tests can
// shrink the interval without waiting real seconds.
var (
	manifestPollInterval = 15 * time.Second
	manifestMaxWait      = 75 * time.Minute
)

// waitForManifest ticks the source scheduler (so the due account_backup actually
// runs) and polls the fresh repo for a stage=manifest snapshot — the last
// snapshot an account_backup writes, so its presence means the backup finished.
// The repo carries only this one account, so any manifest snapshot is ours.
func (s *session) waitForManifest(ctx context.Context, repoDir string) error {
	listCmd := "restic -r " + shellSingleQuote(repoDir) +
		" --password-file " + shellSingleQuote(sourceBoxPasswordPath) +
		" snapshots --json 2>/dev/null"
	deadline := time.Now().Add(manifestMaxWait)
	ticks := 0
	for {
		// Tick advances the due schedule; harmless to repeat (once fired,
		// next_run_at moves to the next cron slot until 03:00).
		_, _ = s.run(ctx, s.commandTimeout, "jabali backup scheduler tick")
		ticks++
		if out, err := s.run(ctx, s.commandTimeout, listCmd); err == nil {
			var snaps []struct {
				Tags []string `json:"tags"`
			}
			if json.Unmarshal(out, &snaps) == nil {
				for _, sn := range snaps {
					for _, t := range sn.Tags {
						if strings.Contains(t, "manifest") {
							return nil
						}
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("source account_backup: no manifest snapshot after %d ticks (~%s) — the account may be very large or the source scheduler is not running", ticks, manifestMaxWait)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("source account_backup: context cancelled before the manifest snapshot landed: %w", ctx.Err())
		case <-time.After(manifestPollInterval):
		}
	}
}

// ReadBoxPassword returns the source box's restic password (root-readable over
// SSH). Carried to the dest so the import can open the pulled repo.
func ReadBoxPassword(ctx context.Context, raw migrate.Session) (string, error) {
	s, ok := raw.(*session)
	if !ok {
		return "", errors.New("jabali.ReadBoxPassword: wrong session type")
	}
	out, err := s.run(ctx, s.commandTimeout, "cat "+shellSingleQuote(sourceBoxPasswordPath))
	if err != nil {
		return "", fmt.Errorf("read source restic password: %w", err)
	}
	pw := strings.TrimRight(string(out), "\r\n")
	if pw == "" {
		return "", errors.New("source restic password file is empty")
	}
	return pw, nil
}

// PullRepo streams the source restic repo dir back as a gzip tarball to localTar.
// It uses a streamed `tar czf -` (never buffering the repo in memory — a real
// account's repo is GBs), mirroring wordpressssh.PullFilesTarball. The caller
// extracts localTar to obtain the repo tree.
func PullRepo(ctx context.Context, raw migrate.Session, repoDir, localTar string) error {
	s, ok := raw.(*session)
	if !ok {
		return errors.New("jabali.PullRepo: wrong session type")
	}
	cmd := "tar czf - -C " + shellSingleQuote(repoDir) + " . 2>/dev/null"
	return s.streamToFile(ctx, manifestMaxWait, cmd, localTar)
}

// streamToFile runs cmd on the source and writes its stdout straight to
// localPath (streamed, not buffered). Mirrors wordpressssh's helper.
func (s *session) streamToFile(ctx context.Context, timeout time.Duration, cmd, localPath string) error {
	subctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sess, err := s.client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh new session: %w", err)
	}
	defer sess.Close()
	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer f.Close()
	sess.Stdout = f
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("ssh stream %q: %w", cmd, err)
		}
		return nil
	case <-subctx.Done():
		return fmt.Errorf("ssh stream %q: timed out after %s", cmd, timeout)
	}
}

// CleanupSource removes the throwaway destination/schedule/repo RunSourceBackup
// created on the source. Order matters: schedule FIRST (a surviving daily
// schedule pointed at a deleted repo dir would fail forever on the source), then
// the destination, then the repo dir. Best-effort — returns every error so the
// caller can warn without aborting.
func CleanupSource(ctx context.Context, raw migrate.Session, sb *SourceBackup) []error {
	s, ok := raw.(*session)
	if !ok || sb == nil {
		return nil
	}
	var errs []error
	if sb.ScheduleID != "" {
		if _, err := s.run(ctx, s.commandTimeout, "jabali backup schedule delete "+shellSingleQuote(sb.ScheduleID)+" --force"); err != nil {
			errs = append(errs, fmt.Errorf("delete source schedule %s: %w", sb.ScheduleID, err))
		}
	}
	if sb.DestName != "" {
		if _, err := s.run(ctx, s.commandTimeout, "jabali backup destination delete "+shellSingleQuote(sb.DestName)+" --force"); err != nil {
			errs = append(errs, fmt.Errorf("delete source destination %s: %w", sb.DestName, err))
		}
	}
	if sb.RepoDir != "" {
		if _, err := s.run(ctx, s.commandTimeout, "rm -rf -- "+shellSingleQuote(sb.RepoDir)); err != nil {
			errs = append(errs, fmt.Errorf("remove source repo dir %s: %w", sb.RepoDir, err))
		}
	}
	return errs
}
