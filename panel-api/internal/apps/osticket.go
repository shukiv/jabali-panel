package apps

// OSTicket is the descriptor for osTicket — a widely-used open-source
// support-ticket system (GH #962, JAB-231). RequiresDB=true.
//
// osTicket has no official headless installer, so the agent drives its OWN
// setup code from a CLI shim (setup/install-cli.php → Installer->install()):
// this runs osTicket's real schema import + admin/config seeding, so we never
// reimplement version-sensitive seeding. See the agent's osticket_install.go.
//
// osTicket is a front-controller / PATH_INFO app: /scp/ajax.php/... and the
// client-side ajax route through /script.php/extra/path URLs, which the
// generated vhost's end-anchored `location ~ \.php$` never matches (GH #962).
// The installer therefore writes a per-install nginx drop-in that adds the
// PATH_INFO handler (the same block the per-domain nginx_safe_options.path_info
// toggle emits) plus deny blocks for /include and /setup. Removed on delete.
//
// Admin logs in by USERNAME at /scp/ (EmailLogin=false). Docroot-only for now
// (RootOnly=true) — a subdir install would need the PATH_INFO/deny blocks
// re-scoped under the subdir prefix.
var OSTicket = App{
	Name:                 "osticket",
	DisplayName:          "osTicket",
	Icon:                 "CustomerServiceOutlined",
	Description:          "Open-source support-ticket system — email + web ticketing, help topics, SLAs, agents and departments for customer support.",
	Tags:                 []string{"Helpdesk", "Support"},
	DefaultSubdirectory:  "",
	RequiresDB:           true,
	EmailLogin:           false,
	RootOnly:             true,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	InstallParamSchema: map[string]ParamSpec{
		"helpdesk_name": {
			Type:        "string",
			Required:    false,
			Default:     "Support",
			Description: "Help-desk name — shown in the page title and outgoing email.",
		},
		"helpdesk_email": {
			Type:     "email",
			Required: true,
			// osTicket refuses install if the system email equals the admin
			// email, so this must differ from admin_email.
			Description: "Default system email (the address tickets are sent from). Must be different from the admin email.",
		},
		"admin_first_name": {
			Type:        "string",
			Required:    false,
			Default:     "Admin",
			Description: "Administrator first name.",
		},
		"admin_last_name": {
			Type:        "string",
			Required:    false,
			Default:     "User",
			Description: "Administrator last name.",
		},
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Administrator email (used for alerts). Must differ from the system email above.",
		},
		"admin_username": {
			Type:     "string",
			Required: false,
			Default:  "sysadmin",
			// osTicket bans admin/admins/username/osticket as the login name.
			Description: "Administrator login username for the agent panel (/scp). Cannot be 'admin', 'admins', 'username' or 'osticket'.",
		},
		"admin_password": {
			Type:        "password",
			Required:    false,
			Description: "Initial admin password. Leave blank to have one generated. osTicket requires a strong password; the generator satisfies that.",
		},
	},
}
