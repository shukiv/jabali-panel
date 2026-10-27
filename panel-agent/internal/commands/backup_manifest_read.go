// backup_manifest_read.go — GH #267 (tenant selective restore, Wave 1).
//
// Read-only preview: dump the manifest.json out of an account_backup's manifest
// snapshot and return its stages + items so the panel can show a restore picker
// WITHOUT materializing any data. Mirrors the manifest read the restore handler
// already does (backup_restore.go), but as a standalone read-only command the
// tenant /me/backups/:id/manifest endpoint can call.
//
// The read targets the repo the caller names (repo_url + credentials +
// per-destination password file, same wire contract as backup.create);
// empty = the legacy default local repo. It originally hardcoded the
// default repo with "remote-destination preview is a follow-up" — the
// follow-up that never happened, so the restore drawer 502'd on every
// per-destination backup (GH #1044 E2E finding). The manifest snapshot's
// stdin filename is "manifest.json" (backup_create.go).
package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

type backupManifestReadParams struct {
	ManifestSnapshotID string            `json:"manifest_snapshot_id"`
	RepoURL            string            `json:"repo_url,omitempty"`
	CredentialsRef     string            `json:"credentials_ref,omitempty"`
	PasswordFile       string            `json:"password_file,omitempty"`
	SFTP               *backupSFTPInputs `json:"sftp,omitempty"`
}

type backupManifestStageDTO struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Items  []string `json:"items,omitempty"`
}

type backupManifestReadResult struct {
	Kind       string                   `json:"kind"`
	UserID     string                   `json:"user_id"`
	Username   string                   `json:"username"`
	Stages     []backupManifestStageDTO `json:"stages"`
	DNSDomains []string                 `json:"dns_domains"` // GH #267: domains with captured custom DNS records
	// Domains are all the account's domains in the backup (GH #2303): the
	// restore picker offers each one's files, and lists its DNS greyed out
	// when it has no custom records to restore.
	Domains []string `json:"domains"`
}

func backupManifestReadHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupManifestReadParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	if p.ManifestSnapshotID == "" {
		return nil, bkInvalidArg("manifest_snapshot_id is required")
	}

	cfg, cerr := bkResticConfigWithPassword(p.RepoURL, p.CredentialsRef, p.PasswordFile, p.SFTP)
	if cerr != nil {
		return nil, bkInternal("restic config", cerr)
	}
	c := backup.New(cfg)

	manifestBytes, err := c.Dump(ctx, p.ManifestSnapshotID, "manifest.json")
	if err != nil {
		return nil, bkInternal("dump manifest", err)
	}
	m, err := backup.AccountManifestFromBytes(manifestBytes)
	if err != nil {
		return nil, bkInternal("parse manifest", err)
	}

	out := backupManifestReadResult{
		Kind:     m.Kind,
		UserID:   m.User.ID,
		Username: m.User.Username,
		Stages:   make([]backupManifestStageDTO, 0, len(m.Stages)),
	}
	for _, st := range m.Stages {
		out.Stages = append(out.Stages, backupManifestStageDTO{
			Name:   st.Name,
			Status: st.Status,
			Items:  st.Items,
		})
	}

	// GH #267: surface which domains have captured custom DNS records (from the
	// meta stage) so the restore UI can offer per-domain DNS restore, and
	// (GH #2303) every domain of the backup, for its files.
	out.DNSDomains, out.Domains = []string{}, []string{}
	for _, st := range m.Stages {
		if st.Name != backup.StageMeta || st.Status != backup.StageStatusOK || st.SnapshotID == "" {
			continue
		}
		if metaBytes, derr := c.Dump(ctx, st.SnapshotID, "metadata.json"); derr == nil {
			var meta backup.AccountMetadata
			if json.Unmarshal(metaBytes, &meta) == nil {
				out.Domains, out.DNSDomains = manifestDomainLists(&meta)
			}
		}
		break
	}
	return out, nil
}

// manifestDomainLists returns the backup's domains, and those of them with
// custom DNS records captured.
func manifestDomainLists(meta *backup.AccountMetadata) (all, withDNS []string) {
	all, withDNS = []string{}, []string{}
	for _, d := range meta.Domains {
		all = append(all, d.Name)
		if len(d.DNSRecords) > 0 {
			withDNS = append(withDNS, d.Name)
		}
	}
	return all, withDNS
}

func init() {
	Default.Register("backup.manifest_read", backupManifestReadHandler)
}
