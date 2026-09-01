package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
)

// system.fullbackup.pack — GH #1408 / #502. Package a whole Full Server backup
// RUN (the system job + one job per hosting user, all sharing a run_id) into a
// SINGLE downloadable container so an operator can move an entire server in one
// file. The container is a PLAIN tar of:
//
//	full-server-backup/
//	├── manifest.json
//	├── system.tar.zst              (the system leg, when present)
//	└── users/<username>.tar.zst    (one per user)
//
// Each inner <label>.tar.zst is BYTE-IDENTICAL to that job's normal per-account
// download (materialize → tar -C <downloads> <job_id> | zstd), so the restore
// side feeds each straight into backup.restore_from_tar with no special-casing.
// The outer tar is uncompressed — the inners are already zstd, so wrapping them
// again would burn CPU on GBs for ~0 gain.
//
// Disk: each job is materialized (≈ its data size), tarred, then its tree is
// DELETED before the next — so peak usage is ~one account's data + the growing
// container, not the whole server at once. A host-reserve check gates each job.

const (
	fullpkgTmpRoot   = "/var/lib/jabali-backups/fullpkg-tmp"
	fullpkgStageRoot = "/var/lib/jabali-backups/fullpkg-stage"
	fullpkgOutRoot   = downloadRoot // /var/lib/jabali-backups/downloads (jabali-readable)
)

var fullpkgLabelRE = regexp.MustCompile(`^(system|users/[a-z][a-z0-9_-]{0,31})$`)

type fullpkgJob struct {
	JobID          string            `json:"job_id"`
	Label          string            `json:"label"` // "system" or "users/<username>"
	RepoURL        string            `json:"repo_url,omitempty"`
	PasswordFile   string            `json:"password_file,omitempty"`
	CredentialsRef string            `json:"credentials_ref,omitempty"`
	SFTP           *backupSFTPInputs `json:"sftp,omitempty"`
}

type systemFullbackupPackParams struct {
	RunID string       `json:"run_id"`
	Jobs  []fullpkgJob `json:"jobs"`
}

type systemFullbackupPackResult struct {
	Path    string   `json:"path"`
	Bytes   int64    `json:"bytes"`
	Packed  []string `json:"packed"`  // labels successfully packed
	Skipped []string `json:"skipped"` // labels skipped (no snapshot / restore error)
}

func systemFullbackupPackHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p systemFullbackupPackParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid_arg: %w", err)
	}
	if !jobIDRE.MatchString(p.RunID) {
		return nil, fmt.Errorf("invalid_arg: run_id must be a 26-char ULID")
	}
	if len(p.Jobs) == 0 {
		return nil, fmt.Errorf("invalid_arg: no jobs to pack")
	}

	tmpRoot := filepath.Join(fullpkgTmpRoot, p.RunID)
	stage := filepath.Join(fullpkgStageRoot, p.RunID)
	_ = os.RemoveAll(tmpRoot)
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(filepath.Join(stage, "users"), 0o750); err != nil {
		return nil, fmt.Errorf("mkdir stage: %w", err)
	}
	defer os.RemoveAll(tmpRoot)
	defer os.RemoveAll(stage)

	out := systemFullbackupPackResult{}
	type manifestUser struct {
		Username string `json:"username,omitempty"`
		Label    string `json:"label"`
		JobID    string `json:"job_id"`
	}
	var manifestEntries []manifestUser

	for _, j := range p.Jobs {
		if !jobIDRE.MatchString(j.JobID) || !fullpkgLabelRE.MatchString(j.Label) {
			out.Skipped = append(out.Skipped, j.Label)
			continue
		}
		// Gate each job on the host reserve so a big run can't wedge the box.
		if err := hostreserve.CheckReserve("/var/lib/jabali-backups", 0); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: "packaging paused: under host disk reserve: " + err.Error()}
		}
		innerTar := filepath.Join(stage, j.Label+".tar.zst")
		if err := os.MkdirAll(filepath.Dir(innerTar), 0o750); err != nil {
			return nil, fmt.Errorf("mkdir inner: %w", err)
		}
		if err := packOneJob(ctx, j, filepath.Join(tmpRoot, j.JobID), innerTar); err != nil {
			// One account failing must not sink the whole server package.
			out.Skipped = append(out.Skipped, j.Label)
			_ = os.RemoveAll(filepath.Join(tmpRoot, j.JobID))
			continue
		}
		_ = os.RemoveAll(filepath.Join(tmpRoot, j.JobID)) // delete-as-you-go
		out.Packed = append(out.Packed, j.Label)
		username := ""
		if len(j.Label) > len("users/") && j.Label[:len("users/")] == "users/" {
			username = j.Label[len("users/"):]
		}
		manifestEntries = append(manifestEntries, manifestUser{Username: username, Label: j.Label, JobID: j.JobID})
	}

	manifest := map[string]any{"run_id": p.RunID, "schema": 1, "entries": manifestEntries}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), mb, 0o640); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	// Outer container: a PLAIN tar of the stage (inners already zstd). Land it in
	// the jabali-readable downloads dir so panel-api can stream it.
	if err := os.MkdirAll(fullpkgOutRoot, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir out: %w", err)
	}
	containerPath := filepath.Join(fullpkgOutRoot, "fullpkg-"+p.RunID+".tar")
	_ = os.Remove(containerPath)
	tarCmd := execCommandContext(ctx, "tar", "-cf", containerPath, "-C", stage, ".")
	if o, err := tarCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build container: %v: %s", err, bytesTrim(o))
	}
	// Hand read access to the jabali user (panel-api) for streaming.
	_ = execCommand("chgrp", "jabali", containerPath).Run()
	_ = os.Chmod(containerPath, 0o640)
	if fi, err := os.Stat(containerPath); err == nil {
		out.Bytes = fi.Size()
	}
	out.Path = containerPath
	return out, nil
}

// packOneJob restores every snapshot tagged with the job into tmpDir/<stage>/…
// (the same layout materialize produces) and tars it to innerTar as
// `<job_id>/<stage>/…` — byte-identical to that job's per-account download.
func packOneJob(ctx context.Context, j fullpkgJob, tmpDir, innerTar string) error {
	cfg, cerr := bkResticConfigWithPassword(j.RepoURL, j.CredentialsRef, j.PasswordFile, j.SFTP)
	if cerr != nil {
		return fmt.Errorf("restic config: %w", cerr)
	}
	c := backup.New(cfg)
	snaps, err := c.Snapshots(ctx, []backup.Tag{backup.MakeTag(backup.TagKeyJobID, j.JobID)})
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	if len(snaps) == 0 {
		return fmt.Errorf("no snapshots for job %s", j.JobID)
	}
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return err
	}
	for _, s := range snaps {
		stage := stageFromTags(s.Tags)
		if !materializeStageNameRe.MatchString(stage) {
			stage = s.ID[:8]
		}
		target := filepath.Join(tmpDir, stage)
		if err := os.MkdirAll(target, 0o750); err != nil {
			return err
		}
		if err := c.Restore(ctx, backup.RestoreOpts{SnapshotID: s.ID, Target: target}); err != nil {
			return fmt.Errorf("restore %s: %w", stage, err)
		}
	}
	// tar -C <parent> <job_id> | zstd -> innerTar  (prefix = <job_id>/, exactly
	// what the per-account download and restore_from_tar expect). Piped in Go —
	// no shell, no attacker-derived args (all paths are agent-built), no
	// intermediate uncompressed copy on disk.
	parent := filepath.Dir(tmpDir)
	jobDir := filepath.Base(tmpDir)
	tarCmd := execCommandContext(ctx, "tar", "-cf", "-", "-C", parent, jobDir)
	zstdCmd := execCommandContext(ctx, "zstd", "-q", "-o", innerTar, "-f")
	pr, perr := tarCmd.StdoutPipe()
	if perr != nil {
		return fmt.Errorf("pipe: %w", perr)
	}
	zstdCmd.Stdin = pr
	if err := zstdCmd.Start(); err != nil {
		return fmt.Errorf("zstd start: %w", err)
	}
	tarErr := tarCmd.Run() // closes the pipe's write end on return
	zstdErr := zstdCmd.Wait()
	if tarErr != nil {
		return fmt.Errorf("tar: %w", tarErr)
	}
	if zstdErr != nil {
		return fmt.Errorf("zstd: %w", zstdErr)
	}
	return nil
}

func init() {
	Default.Register("system.fullbackup.pack", systemFullbackupPackHandler)
}
