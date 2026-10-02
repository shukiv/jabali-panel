package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// `jabali backup retention verify` reads every backup's manifest and checks
// that each stage snapshot it names is still in the repository. Report-only:
// it changes nothing and takes no repository lock.
//
// Why: before whole-job retention (backup_retention_jobs.go) the sweep kept
// or forgot each stage on its own, so a backup could keep its manifest while
// its home or meta snapshot was forgotten. The panel still lists such a
// backup, and a restore of it fails. The manifest is the only record of which
// stages a backup wrote: the agent writes it even when every stage failed, so
// the snapshot tags alone cannot tell a broken backup from a whole one.

const verifyCallTimeout = 10 * time.Minute

type verifyMissing struct {
	Stage      string `json:"stage"`
	SnapshotID string `json:"snapshot_id"`
}

// verifyBackup is one backup that failed the check.
type verifyBackup struct {
	JobID   string          `json:"job_id"`
	Series  string          `json:"series"`
	Time    time.Time       `json:"time"`
	Missing []verifyMissing `json:"missing,omitempty"`
	// NoData: the manifest lists no stage that wrote a snapshot.
	NoData bool `json:"no_data,omitempty"`
	// Error: the manifest could not be read or parsed.
	Error string `json:"error,omitempty"`
}

type verifyDestReport struct {
	DestinationID   string         `json:"destination_id"`
	DestinationName string         `json:"destination_name"`
	Checked         int            `json:"checked"`
	Broken          []verifyBackup `json:"broken"`
	Unreadable      []verifyBackup `json:"unreadable"`
	// Error: the destination could not be checked at all.
	Error string `json:"error,omitempty"`
}

func newBackupRetentionVerifyCmd() *cobra.Command {
	var destFlag string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check that every backup's stage snapshots are still in its repository (read-only)",
		Long: `Reads the manifest of every backup in each enabled destination (or only
--destination) and checks that each stage snapshot the manifest lists as
written is still in the repository. A backup missing any of them cannot be
restored. Older retention sweeps, which kept or forgot each stage on its own,
could leave backups like that behind.

Read-only: nothing is forgotten or changed, and the repository is read
without a lock. Delete a broken backup from Admin → Backups, which forgets
every snapshot of that backup.

Exits non-zero when it finds a broken backup, a manifest it cannot read, or a
destination it cannot check. --json prints the full report.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}
			if err := assertResticEnvironment(); err != nil {
				return err
			}
			var dests []models.BackupDestination
			if destFlag != "" {
				d, err := resolveBackupDestination(ctx, destFlag)
				if err != nil {
					return err
				}
				dests = []models.BackupDestination{*d}
			} else {
				all, err := repository.NewBackupDestinationRepository(sharedDB).List(ctx)
				if err != nil {
					return fmt.Errorf("list backup_destinations: %w", err)
				}
				for _, d := range all {
					if d.Enabled {
						dests = append(dests, d)
					}
				}
			}
			pw := newDestPasswords()
			defer pw.close()
			reports := make([]verifyDestReport, 0, len(dests))
			for i := range dests {
				d := &dests[i]
				r, err := pw.repo(d)
				if err != nil {
					reports = append(reports, verifyDestReport{DestinationID: d.ID, DestinationName: d.Name, Error: err.Error()})
					continue
				}
				reports = append(reports, verifyDestination(ctx, r))
			}
			return reportVerify(cmd, reports)
		},
	}
	cmd.Flags().StringVar(&destFlag, "destination", "", "check only this destination (id or name)")
	return cmd
}

// verifyDestination checks every manifest in one destination's repository.
func verifyDestination(ctx context.Context, r resticRepo) verifyDestReport {
	rep := verifyDestReport{DestinationID: r.ID, DestinationName: r.Name, Broken: []verifyBackup{}, Unreadable: []verifyBackup{}}
	raw, err := runResticRead(ctx, r, append(r.args(), "--no-lock", "snapshots", "--json"))
	if err != nil {
		rep.Error = fmt.Sprintf("list snapshots: %v", err)
		return rep
	}
	var snaps []resticSnapshot
	if err := json.Unmarshal(bytes.TrimSpace(raw), &snaps); err != nil {
		rep.Error = fmt.Sprintf("parse restic snapshots: %v", err)
		return rep
	}
	ids := make(map[string]bool, len(snaps))
	var manifests []resticSnapshot
	for _, s := range snaps {
		ids[s.ID] = true
		if tagValue(s.Tags, internalbackup.TagKeyStage) == internalbackup.StageManifest &&
			tagValue(s.Tags, internalbackup.TagKeyJobID) != "" {
			manifests = append(manifests, s)
		}
	}
	sort.SliceStable(manifests, func(a, b int) bool { return manifests[a].Time.Before(manifests[b].Time) })

	for _, m := range manifests {
		rep.Checked++
		b := verifyBackup{JobID: tagValue(m.Tags, internalbackup.TagKeyJobID), Series: seriesOf(m), Time: m.Time}
		body, err := runResticRead(ctx, r, append(r.args(), "--no-lock", "dump", m.ID, manifestFile(m)))
		if err != nil {
			b.Error = fmt.Sprintf("read manifest snapshot %s: %v", snapPrefix(m.ID), err)
			rep.Unreadable = append(rep.Unreadable, b)
			continue
		}
		var mf struct {
			Stages []internalbackup.ManifestStage `json:"stages"`
		}
		if err := json.Unmarshal(body, &mf); err != nil {
			b.Error = fmt.Sprintf("parse manifest snapshot %s: %v", snapPrefix(m.ID), err)
			rep.Unreadable = append(rep.Unreadable, b)
			continue
		}
		written := 0
		for _, st := range mf.Stages {
			if st.Status != internalbackup.StageStatusOK || st.SnapshotID == "" {
				continue
			}
			written++
			if !snapshotPresent(ids, st.SnapshotID) {
				b.Missing = append(b.Missing, verifyMissing{Stage: st.Name, SnapshotID: st.SnapshotID})
			}
		}
		b.NoData = written == 0
		if len(b.Missing) > 0 || b.NoData {
			rep.Broken = append(rep.Broken, b)
		}
	}
	return rep
}

// manifestFile is the file a manifest snapshot holds: its one path, or the
// name the agent writes for that backup kind.
func manifestFile(s resticSnapshot) string {
	if len(s.Paths) == 1 {
		return s.Paths[0]
	}
	if tagValue(s.Tags, internalbackup.TagKeyKind) == internalbackup.KindSystemBackup {
		return "/system_manifest.json"
	}
	return "/manifest.json"
}

// snapshotPresent matches a manifest's snapshot ID, full or abbreviated,
// against the repository's full snapshot IDs.
func snapshotPresent(ids map[string]bool, id string) bool {
	if ids[id] {
		return true
	}
	if len(id) >= 64 {
		return false
	}
	for full := range ids {
		if strings.HasPrefix(full, id) {
			return true
		}
	}
	return false
}

// snapPrefix is the 8-character form restic prints for a snapshot ID.
func snapPrefix(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// runResticRead runs a read-only restic command and returns its stdout. It
// never runs `restic unlock`: verify takes no lock, so it has none to recover.
func runResticRead(ctx context.Context, r resticRepo, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, verifyCallTimeout)
	defer cancel()
	env := append(os.Environ(), destEnv(r.BackupDestination)...)
	var out, errBuf bytes.Buffer
	if err := retentionExec(ctx, env, &out, &errBuf, "restic", args...); err != nil {
		if msg := strings.TrimSpace(errBuf.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out.Bytes(), nil
}

func reportVerify(cmd *cobra.Command, reports []verifyDestReport) error {
	broken, unreadable, failed := 0, 0, 0
	for _, r := range reports {
		broken += len(r.Broken)
		unreadable += len(r.Unreadable)
		if r.Error != "" {
			failed++
		}
	}
	if jsonOutput {
		if err := printJSON(map[string]any{"destinations": reports}); err != nil {
			return err
		}
	} else {
		out := cmd.OutOrStdout()
		for _, r := range reports {
			if r.Error != "" {
				fmt.Fprintf(out, "dest %s (%s): not checked: %s\n", r.DestinationID, r.DestinationName, r.Error)
				continue
			}
			fmt.Fprintf(out, "dest %s (%s): %d backup(s) checked, %d broken, %d unreadable\n",
				r.DestinationID, r.DestinationName, r.Checked, len(r.Broken), len(r.Unreadable))
			for _, b := range r.Broken {
				fmt.Fprintf(out, "  broken: job %s (%s, %s): %s\n", b.JobID, b.Series, b.Time.UTC().Format(time.RFC3339), brokenReason(b))
			}
			for _, b := range r.Unreadable {
				fmt.Fprintf(out, "  unreadable: job %s (%s, %s): %s\n", b.JobID, b.Series, b.Time.UTC().Format(time.RFC3339), b.Error)
			}
		}
		if len(reports) == 0 {
			fmt.Fprintln(out, "no enabled backup destinations to check")
		}
		if broken > 0 {
			fmt.Fprintln(out, "A broken backup cannot be restored. Delete it from Admin → Backups, which forgets every snapshot of that backup.")
		}
	}
	if broken > 0 || unreadable > 0 || failed > 0 {
		return fmt.Errorf("verify found %d broken backup(s), %d unreadable manifest(s), %d destination(s) not checked", broken, unreadable, failed)
	}
	return nil
}

func brokenReason(b verifyBackup) string {
	if b.NoData {
		return "its manifest lists no stage that wrote data"
	}
	parts := make([]string, 0, len(b.Missing))
	for _, m := range b.Missing {
		parts = append(parts, fmt.Sprintf("%s (snapshot %s)", m.Stage, snapPrefix(m.SnapshotID)))
	}
	return "missing " + strings.Join(parts, ", ")
}
