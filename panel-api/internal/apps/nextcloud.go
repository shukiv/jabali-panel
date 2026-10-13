package apps

// Nextcloud is the descriptor for Nextcloud — self-hosted file sync and share
// with calendars and contacts (Discussion #1032). RequiresDB=true.
//
// It runs on the tenant's own PHP-FPM pool and MariaDB like every other PHP
// app. The agent installs the pinned, SHA-256-checked release with Nextcloud's
// own `occ maintenance:install`, keeps the data directory beside the docroot
// (never under it), switches background jobs to cron, and writes a per-install
// nginx drop-in: the PATH_INFO front controllers (/index.php/…, /remote.php/…,
// /ocs/…), CalDAV/CardDAV discovery, the .mjs type, and deny blocks for the
// code and config trees. Removed on delete.
//
// Nextcloud's Files app, sync clients and CalDAV/CardDAV speak WebDAV, which
// CRS 911100 blocks. Each install therefore carries a managed, scoped CRS
// exclusion for its WebDAV endpoints (appsecops/nextcloud.go).
//
// Docroot-only (RootOnly=true): Nextcloud answers on one canonical host, and
// the drop-in and the WAF exclusion are scoped to the domain's root. Nextcloud
// 35 needs PHP 8.3 to 8.5; the agent refuses an older or newer pool before it
// downloads anything.
var Nextcloud = App{
	Name:                 "nextcloud",
	DisplayName:          "Nextcloud",
	Icon:                 "CloudOutlined",
	Description:          "Self-hosted file sync and share — files, calendars and contacts, with desktop and mobile sync clients.",
	Tags:                 []string{"Files", "Collaboration"},
	DefaultSubdirectory:  "",
	RequiresDB:           true,
	EmailLogin:           false,
	RootOnly:             true,
	SupportedPHPVersions: []string{"8.3", "8.4", "8.5"},
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	InstallParamSchema: map[string]ParamSpec{
		"admin_username": AdminUsernameParam,
		"admin_email": {
			Type:        "email",
			Required:    false,
			Description: "Administrator email — used for password resets and notifications. Optional.",
		},
		"admin_password": adminPasswordParam(`^[^\x00\r\n]*$`,
			"Initial admin password, without line breaks. Leave blank to have one generated."),
	},
}
