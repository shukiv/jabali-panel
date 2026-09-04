package apps

// ITFlow is the descriptor for ITFlow — an open-source MSP ERP (clients,
// ticketing, invoicing, documentation). RequiresDB=true.
//
// First git-clone app: ITFlow ships only via git (it self-updates along
// `master` via git pull from its own admin UI), so the agent clones the
// repo and runs ITFlow's headless `scripts/setup_cli.php` (which writes
// config.php, imports db.sql, and creates the first admin). See the
// agent's itflow_install.go.
//
// ITFlow logs in by EMAIL (not a username); admin_name is the admin's
// display name. Three cron jobs (cron.php daily, mail_queue.php +
// ticket_email_parser.php per-minute) are auto-created at install time by
// the API kicker and torn down on delete.
//
// Clone (duplicate install) is intentionally empty — AgentCloneCmd stays
// blank so the Clone button is hidden. Delete reuses app.delete (a
// dedicated itflow deleter) and the install row's db_id drives the DB drop.
var ITFlow = App{
	Name:                 "itflow",
	DisplayName:          "ITFlow",
	Icon:                 "AppstoreOutlined",
	Description:          "Open-source MSP ERP — client management, ticketing, invoicing, documentation and assets for IT businesses.",
	Tags:                 []string{"CRM", "Project management"},
	DefaultSubdirectory:  "",
	RequiresDB:           true,
	InstallNotice:        "ITFlow's in-app updater runs git via PHP exec functions (exec / shell_exec), which PHP-FPM blocks by default. The base install and its domain-expiry lookups work fully without them — as of the 2026-09 stable, expiry uses RDAP + native DNS, no exec. Only the in-app updater is affected, and it now degrades gracefully: it shows the installed version from the local .git and reports that updates can't be fetched, instead of erroring. To enable in-app updates, an admin assigns this site's owner a hosting package with 'Allow PHP exec functions' turned on (Admin → Packages).",
	EmailLogin:           true,
	RootOnly:             true,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	InstallParamSchema: map[string]ParamSpec{
		"company_name": {
			Type:        "string",
			Required:    true,
			Default:     "My Company",
			Description: "Your company name — seeds ITFlow's first company record.",
		},
		"admin_name": {
			Type:        "string",
			Required:    true,
			Default:     "Administrator",
			Description: "Administrator full name (ITFlow logs in by email; this is the display name).",
		},
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Administrator email — this is the ITFlow login.",
		},
		"admin_password": {
			Type:        "password",
			Required:    false,
			Description: "Initial admin password. Leave blank to have one generated. ITFlow requires ≥8 chars; the generator satisfies that.",
		},
		"repo_branch": {
			Type:     "enum",
			Required: false,
			Default:  "master",
			Values:   []string{"master", "develop"},
			// master is ITFlow's stable release (develop->master on release);
			// we install a security-reviewed pinned master commit (GH #455).
			// develop is their active dev branch: bleeding-edge and NOT
			// security-reviewed. Both are pinned for reproducibility; only
			// pick develop if you accept running unreviewed upstream code.
			Description: "Install branch. 'master' = ITFlow's stable release (recommended). 'develop' = their active development branch — bleeding-edge and NOT security-reviewed; choose only if you accept running unreviewed code.",
		},
	},
}
