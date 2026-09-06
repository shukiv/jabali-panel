package commands

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
)

const (
	sysApplyStagePrefix = "sysapply-"
	sysApplyStageTTL    = 24 * time.Hour
)

// system_fullbackup_apply_system.go — GH #1408 slice 3 (Option C).
//
// Restore the SYSTEM leg (panel_db / panel_config / tls) from an uploaded
// Full Server container, WITHOUT a restic repo. The existing `system.restore`
// verb only restores from restic; this one re-sources the very same apply
// phase (applySystemRestore) from the container's local `system.tar.zst`.
//
// It is CLI-triggered on purpose (`jabali system restore --from-tar`): a
// system apply loads the backup's Kratos identity DB, so driving it from the
// panel would log out the very admin who clicked it on the real cross-server
// DR path. The CLI stops jabali-panel before dispatch and restarts it after;
// jabali-agent (this process) stays up and owns the whole apply.
//
// The container's system.tar.zst is `<job_id>/<stage>/…` (exactly what the
// packer wrote — see packOneJob), so after a two-step extract the job dir is a
// materialized stage tree identical to the one systemRestoreHandler feeds
// applySystemRestore. The manifest is reconstructed FROM that tree (one
// panel_db stage per <db>.sql), so old containers work unchanged.

type systemFullbackupApplySystemParams struct {
	// ContainerPath is the uploaded Full Server container (the plain outer tar).
	ContainerPath string `json:"container_path"`
	// Apply=false is a RECON: extract + reconstruct the stage list and report
	// what WOULD be applied, touching nothing live. Apply=true runs the real
	// panel_db/panel_config/tls load.
	Apply bool `json:"apply"`
	// ApplyStages restricts the apply to named stages (empty = the auto set:
	// panel_db, panel_config, tls). Mirrors system.restore's apply_stages.
	ApplyStages []string `json:"apply_stages,omitempty"`
}

type systemFullbackupApplySystemResult struct {
	JobID          string   `json:"job_id"`
	StagesDetected []string `json:"stages_detected"`
	Applied        []string `json:"applied,omitempty"`
	ApplyWarnings  []string `json:"apply_warnings,omitempty"`
	Recon          bool     `json:"recon"`
}

// systemFullbackupApplySystemHandler applies (or recons) the system leg of an
// uploaded container. The container is confined under the uploads root exactly
// like restore_from_tar / extract_uploaded.
func systemFullbackupApplySystemHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p systemFullbackupApplySystemParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("invalid params: %v", err))
	}
	clean, aerr := fullContainerPathClean(p.ContainerPath)
	if aerr != nil {
		return nil, aerr
	}
	if err := hostreserve.CheckReserve("/var/lib/jabali-backups", 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: "system-restore staging is under the host disk reserve: " + err.Error()}
	}
	// A defer only reaps a live agent; the fleet agent auto-restarts nightly and a
	// dropped CLI leaks its staged copy — sweep older stages/copies at entry.
	evictStaleSysApplyStages()

	stage := filepath.Join(restoreUploadsRoot, sysApplyStagePrefix+randomULID())
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o750); err != nil {
		return nil, bkInternal("mkdir stage", err)
	}
	// Synchronous handler, agent-lifetime — safe to clean up on return.
	defer os.RemoveAll(stage)

	// 1. pull ONLY the system leg out of the container. The container also holds
	// every users/*.tar.zst inner; a full extract would write (then delete)
	// potentially hundreds of GB to reach a ~50MB system tree — the last thing a
	// disk-stressed DR box can afford.
	systemInner := filepath.Join(stage, "system.tar.zst")
	if err := extractSystemInnerFromContainer(clean, systemInner); err != nil {
		return nil, bkInvalidArg(err.Error())
	}

	// 2. inner system.tar.zst (zstd tar) → stage/system, yielding <job_id>/<stage>/…
	extractRoot := filepath.Join(stage, "system")
	if err := os.MkdirAll(extractRoot, 0o750); err != nil {
		return nil, bkInternal("mkdir extract root", err)
	}
	if _, err := safeExtractZstdTar(ctx, systemInner, extractRoot); err != nil {
		return nil, bkInvalidArg("system leg rejected: " + err.Error())
	}

	jobDir, jobID, jerr := soleJobDir(extractRoot)
	if jerr != nil {
		return nil, bkInvalidArg(jerr.Error())
	}

	stages, err := reconstructSystemStages(jobDir)
	if err != nil {
		return nil, bkInvalidArg(err.Error())
	}
	if len(stages) == 0 {
		return nil, bkInvalidArg("no recognizable system stages in the container")
	}

	out := systemFullbackupApplySystemResult{JobID: jobID, Recon: !p.Apply}
	for _, st := range stages {
		if len(st.Items) > 0 {
			out.StagesDetected = append(out.StagesDetected, st.Name+":"+strings.Join(st.Items, ","))
		} else {
			out.StagesDetected = append(out.StagesDetected, st.Name)
		}
	}

	if !p.Apply {
		return out, nil
	}

	applied, warnings := applySystemRestore(ctx, jobDir, stages, p.ApplyStages)
	out.Applied = applied
	out.ApplyWarnings = warnings

	// Post-apply MariaDB account password sync — the cross-host panel_db load
	// leaves service accounts on the destination's old passwords while
	// /etc/jabali-panel/<svc>-db-password now holds the source's. Same fix the
	// restic path applies (see systemRestoreHandler).
	dbResyncs, dbWarnings := resyncDBAccountPasswords(ctx)
	out.Applied = append(out.Applied, dbResyncs...)
	out.ApplyWarnings = append(out.ApplyWarnings, dbWarnings...)
	return out, nil
}

// soleJobDir returns the single <job_id>/ directory the packer wrote under the
// extract root. The container's system leg always tars exactly one job dir.
func soleJobDir(extractRoot string) (dir, jobID string, err error) {
	ents, rerr := os.ReadDir(extractRoot)
	if rerr != nil {
		return "", "", fmt.Errorf("read extracted system leg: %v", rerr)
	}
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 {
		return "", "", fmt.Errorf("expected exactly one job dir in the system leg, found %d", len(dirs))
	}
	return filepath.Join(extractRoot, dirs[0]), dirs[0], nil
}

// knownSystemStages is the set of stage dir names the packer can write. Only
// panel_db/panel_config/tls are auto-applied by applySystemRestore; the rest
// are reconstructed so an explicit --apply-stage can reach them and so
// panel_config's ownership-normalization can read os_users.
func knownSystemStages() map[string]bool {
	return map[string]bool{
		backup.StagePanelDB:       true,
		backup.StagePanelConfig:   true,
		backup.StageServiceConfig: true,
		backup.StageMailState:     true,
		backup.StageTLS:           true,
		backup.StageSecurity:      true,
		backup.StageOSUsers:       true,
		backup.StageDataState:     true,
	}
}

// reconstructSystemStages rebuilds the manifest stage list from the extracted
// job tree. panel_db yields one stage per <db>.sql (applyPanelDBStage loads
// st.Items[0]); every other recognized stage dir yields a single stage.
func reconstructSystemStages(jobDir string) ([]backup.ManifestStage, error) {
	ents, err := os.ReadDir(jobDir)
	if err != nil {
		return nil, fmt.Errorf("read job dir: %v", err)
	}
	known := knownSystemStages()
	var stages []backup.ManifestStage
	for _, e := range ents {
		if !e.IsDir() || !known[e.Name()] {
			continue
		}
		if e.Name() == backup.StagePanelDB {
			dbs, derr := panelDBNames(filepath.Join(jobDir, e.Name()))
			if derr != nil {
				return nil, derr
			}
			for _, db := range dbs {
				stages = append(stages, backup.ManifestStage{Name: backup.StagePanelDB, Items: []string{db}})
			}
			continue
		}
		stages = append(stages, backup.ManifestStage{Name: e.Name()})
	}
	return stages, nil
}

// panelDBNames lists the database names in a panel_db stage dir — one per
// <db>.sql file the dump produced.
func panelDBNames(panelDBDir string) ([]string, error) {
	ents, err := os.ReadDir(panelDBDir)
	if err != nil {
		return nil, fmt.Errorf("read panel_db dir: %v", err)
	}
	var dbs []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if n := strings.TrimSuffix(e.Name(), ".sql"); n != e.Name() && n != "" {
			dbs = append(dbs, n)
		}
	}
	sort.Strings(dbs)
	if len(dbs) == 0 {
		return nil, fmt.Errorf("panel_db stage has no <db>.sql files")
	}
	return dbs, nil
}

// extractSystemInnerFromContainer streams ONLY the top-level `system.tar.zst`
// member out of the plain outer container to destPath — never the users/*
// inners. Bounded to the member's own tar-header size (the outer is an
// uncompressed tar, so no decompression bomb).
func extractSystemInnerFromContainer(containerPath, destPath string) error {
	f, err := os.Open(containerPath)
	if err != nil {
		return fmt.Errorf("open container: %v", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read container: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Clean(hdr.Name) != "system.tar.zst" {
			continue
		}
		out, oerr := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if oerr != nil {
			return bkInternal("open system inner dest", oerr)
		}
		if _, cerr := io.CopyN(out, tr, hdr.Size); cerr != nil {
			out.Close()
			return fmt.Errorf("extract system.tar.zst: %v", cerr)
		}
		return out.Close()
	}
	return fmt.Errorf("container has no system.tar.zst member — it holds no system backup leg")
}

// evictStaleSysApplyStages removes leftover sysapply-* stages and the CLI's
// sysrestore-*.tar staged copies older than the TTL. Mirrors the fullrestore-*
// eviction; runs at handler entry.
func evictStaleSysApplyStages() {
	cutoff := time.Now().Add(-sysApplyStageTTL)
	for _, pat := range []string{sysApplyStagePrefix + "*", "sysrestore-*.tar"} {
		matches, _ := filepath.Glob(filepath.Join(restoreUploadsRoot, pat))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.ModTime().Before(cutoff) {
				_ = os.RemoveAll(m)
			}
		}
	}
}

func init() {
	Default.Register("system.fullbackup.apply_system_from_tar", systemFullbackupApplySystemHandler)
}
