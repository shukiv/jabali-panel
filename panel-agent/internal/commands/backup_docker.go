// backup.docker agent command: one restic snapshot per docker app data
// tree, tagged stage=docker,app=<slug>, into the ACCOUNT's destination
// repo.
//
// Why this stage exists (GH #954): docker app data lives at
// /var/lib/jabali/docker-apps/<slug>, outside the user home, so the home
// stage never walked it. An account with a docker app therefore backed up
// without its app data — and because the account backup is also the
// transport for Jabali→Jabali migration and the DR standby, the app
// silently failed to arrive on the destination.
//
// docker_app.backup already snapshots the same trees, but into the
// operator's standalone repo (JABALI_RESTIC_REPO) rather than the
// account's destination. That is why the gap stayed invisible: the data
// looked backed up, just not anywhere the account restore could reach.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

type backupDockerParams struct {
	JobID      string `json:"job_id"`
	UserID     string `json:"user_id"`
	Username   string `json:"username"`
	ScheduleID string `json:"schedule_id,omitempty"`
	// DockerApps are the slugs owned by this account. Panel-side
	// resolution keeps the agent from having to know who owns what.
	DockerApps     []string          `json:"docker_apps"`
	RepoURL        string            `json:"repo_url,omitempty"`
	PasswordFile   string            `json:"password_file,omitempty"`
	CredentialsRef string            `json:"credentials_ref,omitempty"`
	SFTP           *backupSFTPInputs `json:"sftp,omitempty"`
	Compression    string            `json:"compression,omitempty"`
}

type backupDockerResult struct {
	Snapshots []backupDockerStageSnapshot `json:"snapshots"`
}

type backupDockerStageSnapshot struct {
	App        string `json:"app"`
	SnapshotID string `json:"snapshot_id"`
	BytesAdded uint64 `json:"bytes_added"`
	BytesTotal uint64 `json:"bytes_total"`
	Error      string `json:"error,omitempty"`
}

func backupDockerHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req backupDockerParams
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, bkInvalidArg("malformed JSON body")
	}
	if !ulidRE.MatchString(req.JobID) {
		return nil, bkInvalidArg("job_id must be a 26-char ULID")
	}
	if !ulidRE.MatchString(req.UserID) {
		return nil, bkInvalidArg("user_id must be a 26-char ULID")
	}
	// Slugs are interpolated into a filesystem path, so validate every one
	// before touching disk — validateSlug is the same gate the docker_app
	// commands use.
	for _, slug := range req.DockerApps {
		if err := validateSlug(slug); err != nil {
			return nil, bkInvalidArg(fmt.Sprintf("docker app slug invalid: %s", slug))
		}
	}

	if _, err := bkResticBin(); err != nil {
		return nil, bkInternal("restic missing", err)
	}

	cfg, cerr := bkResticConfigWithPassword(req.RepoURL, req.CredentialsRef, req.PasswordFile, req.SFTP)
	if cerr != nil {
		return nil, bkInternal("restic config", cerr)
	}
	cfg.Compression = req.Compression
	c := backup.New(cfg)

	out := backupDockerResult{Snapshots: make([]backupDockerStageSnapshot, 0, len(req.DockerApps))}
	for _, slug := range req.DockerApps {
		snap, err := snapshotOneDockerApp(ctx, c, req.JobID, req.UserID, req.ScheduleID, slug)
		if err != nil {
			out.Snapshots = append(out.Snapshots, backupDockerStageSnapshot{App: slug, Error: err.Error()})
			continue
		}
		out.Snapshots = append(out.Snapshots, *snap)
	}
	return out, nil
}

// snapshotOneDockerApp backs up one app's data tree. The containers are
// left running: stopping a tenant's app to take a nightly backup trades a
// visible outage for a marginally cleaner tree, and the app-consistent
// path already exists as the pre-update snapshot in docker_app.update.
func snapshotOneDockerApp(ctx context.Context, c *backup.Client, jobID, userID, scheduleID, slug string) (*backupDockerStageSnapshot, error) {
	dir := filepath.Join(dockerAppDataRoot, slug)
	if _, err := os.Stat(dir); err != nil {
		// The panel row says the app exists but its tree does not. Report
		// it rather than skipping quietly: that mismatch is drift worth
		// seeing in the manifest.
		return nil, fmt.Errorf("data dir missing: %s", dir)
	}

	tags := backup.AccountBackupTags(jobID, userID, scheduleID, backup.StageDocker)
	tags = append(tags, backup.MakeTag(backup.TagKeyApp, slug))

	summary, err := c.Backup(ctx, backup.BackupOpts{
		Paths: []string{dir},
		Tags:  tags,
	})
	if err != nil {
		return nil, fmt.Errorf("restic backup %s: %w", dir, err)
	}
	return &backupDockerStageSnapshot{
		App:        slug,
		SnapshotID: summary.SnapshotID,
		BytesAdded: summary.DataAdded,
		BytesTotal: summary.TotalBytesProcessed,
	}, nil
}

func init() {
	Default.Register("backup.docker", backupDockerHandler)
}
