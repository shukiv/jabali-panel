package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: an admin restore from an uploaded backup file (one account, or
// each account of a whole-server container) runs the agent's
// backup.restore_from_tar in mode=upload. Whoever made the file chose every
// name in it, so the agent may only write into the target account's own
// databases, domains and docker apps, or create new ones in the account's own
// namespace. These helpers give the agent what it needs to tell them apart,
// and refuse an agent that predates the mode before anything runs.

// capRestoreUploadConfinement is the agent.version capability for mode=upload.
const capRestoreUploadConfinement = "restore_upload_confinement"

// agentUpdateRequiredDetail is shown when the agent predates mode=upload.
const agentUpdateRequiredDetail = "the server agent must be updated before a backup file can be restored safely"

// agentHasCapability reports whether the agent's agent.version lists name. An
// agent that predates capabilities lists none; an error counts as no.
func agentHasCapability(ctx context.Context, ag agent.AgentInterface, name string) bool {
	if ag == nil {
		return false
	}
	raw, err := ag.Call(ctx, "agent.version", nil)
	if err != nil {
		return false
	}
	var v struct {
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	for _, c := range v.Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

// uploadRestoreParams adds mode=upload and its lists for the account targetID
// to the backup.restore_from_tar params p. A lookup that fails refuses the
// restore rather than send a partial list.
func (cfg BackupHandlerConfig) uploadRestoreParams(ctx context.Context, targetID string, p map[string]any) error {
	if targetID == "" || cfg.Databases == nil || cfg.Domains == nil || cfg.DockerApps == nil {
		return errors.New("the restore can't check what this account owns on this server")
	}
	dbs, _, err := cfg.Databases.List(ctx, repository.ListOptions{})
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}
	ownedDBs, foreignDBs := []string{}, []string{}
	for _, d := range dbs {
		if d.UserID == targetID {
			ownedDBs = append(ownedDBs, d.Name)
		} else {
			foreignDBs = append(foreignDBs, d.Name)
		}
	}
	doms, _, err := cfg.Domains.ListByUserID(ctx, targetID, repository.ListOptions{})
	if err != nil {
		return fmt.Errorf("list the account's domains: %w", err)
	}
	ownedDomains := []string{}
	for _, d := range doms {
		ownedDomains = append(ownedDomains, d.Name)
	}
	apps, err := cfg.DockerApps.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list docker apps: %w", err)
	}
	ownedApps, foreignApps := []string{}, []string{}
	for _, a := range apps {
		if a == nil {
			continue
		}
		if a.UserID != nil && *a.UserID == targetID {
			ownedApps = append(ownedApps, a.EffectiveSlug())
		} else {
			foreignApps = append(foreignApps, a.EffectiveSlug())
		}
	}
	p["mode"] = "upload"
	p["allowed_db_names"] = ownedDBs
	p["foreign_db_names"] = foreignDBs
	p["allowed_mail_domains"] = ownedDomains
	p["owned_docker_slugs"] = ownedApps
	p["foreign_docker_slugs"] = foreignApps
	return nil
}
