package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
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

// uploadedData names the databases and docker app folders (effective slug)
// the agent restored into the account from an uploaded file. Apply registers
// database and docker app rows from the file only for these, or for ones the
// account already has.
type uploadedData struct {
	databases, dockerSlugs []string
}

// uploadedAccountRestore is what restoring one account from an uploaded file
// did: the agent's applied items and warnings from both passes, and the rows
// the metadata rebuild didn't restore.
type uploadedAccountRestore struct {
	Applied, Warnings, MetadataErrors []string
}

// restoreFromTarReply is the part of backup.restore_from_tar's result an
// upload restore reads.
type restoreFromTarReply struct {
	Applied                   []string        `json:"applied"`
	Warnings                  []string        `json:"warnings"`
	Metadata                  json.RawMessage `json:"metadata"`
	UploadConfinementEnforced bool            `json:"upload_confinement_enforced"`
	RestoredDatabases         []string        `json:"restored_databases"`
	RestoredDockerSlugs       []string        `json:"restored_docker_slugs"`
	Stages                    []struct {
		Name string `json:"name"`
	} `json:"stages"`
}

const unconfirmedRestoreDetail = "the agent did not confirm it confined the restore to this account; nothing else was applied — " + agentUpdateRequiredDetail

// restoreUploadedAccount restores the account targetID (username) from the
// uploaded account archive at tarPath. components selects stages (empty =
// all). The error is the admin-facing reason nothing past the agent's first
// pass was applied; what that pass applied is still returned.
//
// The agent restores mail only into the account's own domains, and a restore
// into a fresh account rebuilds those domains from the file's metadata after
// the agent ran. So the agent runs twice: everything but mail, then (after
// the metadata rebuild) mail, with the account's domains looked up again.
//
// report, when not nil, receives the restore's progress by step (GH #1993).
func (h *backupHandler) restoreUploadedAccount(ctx context.Context, tarPath, username, targetID string, components []string, report func(restoreProgress)) (uploadedAccountRestore, error) {
	var out uploadedAccountRestore
	mail := len(components) == 0 || containsStr(components, "mail")
	steps := 3 // files, rows, DNS records
	if mail {
		steps = 4
	}
	params := map[string]any{
		"job_id":          ids.NewULID(),
		"tar_path":        tarPath,
		"target_username": username,
		"components":      components,
	}
	if mail {
		params["skip_components"] = []string{"mail"}
	}
	first, err := h.restoreFromTarReporting(ctx, targetID, params,
		restoreProgress{Step: 1, Steps: steps, Label: restoreStepFilesLabel}, report)
	if err != nil {
		return out, err
	}
	out.Applied, out.Warnings = first.Applied, first.Warnings
	if !first.UploadConfinementEnforced {
		// The capability gate ran before the restore; an agent that still
		// didn't confine it (swapped mid-flight) must not get its metadata
		// applied too.
		return out, errors.New(unconfirmedRestoreDetail)
	}
	if report != nil {
		report(restoreProgress{Step: 2, Steps: steps, Label: restoreStepRowsLabel})
	}
	out.MetadataErrors = h.applyRestoreMetadataForUser(ctx, first.Metadata, targetID,
		uploadedData{databases: first.RestoredDatabases, dockerSlugs: first.RestoredDockerSlugs})

	// GH #1993: last, the domains' custom DNS records. RestoreBundleDNS has the
	// reconciler make the restored domains' zones and adds the records once
	// they exist.
	withDNS := func() (uploadedAccountRestore, error) {
		if report != nil {
			report(restoreProgress{Step: steps, Steps: steps, Label: restoreStepDNSLabel})
		}
		a, w := RestoreBundleDNS(ctx, h.restoreDNSDeps(true), first.Metadata, targetID)
		out.Applied = append(out.Applied, a...)
		out.Warnings = append(out.Warnings, w...)
		return out, nil
	}

	hasMail := false
	for _, st := range first.Stages {
		hasMail = hasMail || st.Name == "mail"
	}
	if !mail || !hasMail {
		return withDNS()
	}
	second, err := h.restoreFromTarReporting(ctx, targetID, map[string]any{
		"job_id":          ids.NewULID(),
		"tar_path":        tarPath,
		"target_username": username,
		"components":      []string{"mail"},
	}, restoreProgress{Step: 3, Steps: steps, Label: restoreStepMailLabel}, report)
	switch {
	case err != nil:
		out.Warnings = append(out.Warnings, "mail: "+err.Error())
	case !second.UploadConfinementEnforced:
		out.Warnings = append(out.Warnings, "mail: not restored: "+unconfirmedRestoreDetail)
	default:
		out.Applied = append(out.Applied, second.Applied...)
		out.Warnings = append(out.Warnings, second.Warnings...)
	}
	return withDNS()
}

// restoreFromTarReporting is restoreFromTar that reports step while the agent
// runs, with what the agent is doing as its detail. The watcher has stopped
// when it returns, so it never overwrites a later step.
func (h *backupHandler) restoreFromTarReporting(ctx context.Context, targetID string, params map[string]any, step restoreProgress, report func(restoreProgress)) (restoreFromTarReply, error) {
	var (
		reply restoreFromTarReply
		err   error
	)
	jobID, _ := params["job_id"].(string)
	withAgentRestoreProgress(ctx, h.cfg.Agent, jobID, step, report, func() {
		reply, err = h.restoreFromTar(ctx, targetID, params)
	})
	return reply, err
}

// restoreFromTar runs backup.restore_from_tar in mode=upload for the account
// targetID with params plus the account's lists.
func (h *backupHandler) restoreFromTar(ctx context.Context, targetID string, params map[string]any) (restoreFromTarReply, error) {
	var reply restoreFromTarReply
	if err := h.cfg.uploadRestoreParams(ctx, targetID, params); err != nil {
		return reply, fmt.Errorf("not restored: %w", err)
	}
	raw, err := h.cfg.Agent.Call(ctx, "backup.restore_from_tar", params)
	if err != nil {
		return reply, errors.New(restoreFailureDetail(err))
	}
	_ = json.Unmarshal(raw, &reply)
	return reply, nil
}
