package apps

// PrivateBin is the descriptor for PrivateBin — a minimalist, zero-knowledge
// encrypted pastebin (GH #631). Everything is encrypted client-side; the
// server never sees plaintext, and there are NO user accounts at all, so the
// install schema carries no admin_username/email/password. RequiresDB=false:
// the default Filesystem model stores pastes on disk — the agent installer
// points it at a data directory OUTSIDE the docroot so pastes are never
// directly fetchable through nginx.
var PrivateBin = App{
	Name:                 "privatebin",
	DisplayName:          "PrivateBin",
	Icon:                 "LockOutlined",
	Description:          "Zero-knowledge encrypted pastebin — pastes are encrypted in the browser, the server never sees plaintext. No accounts, no database.",
	Tags:                 []string{"Utility"},
	DefaultSubdirectory:  "paste",
	RequiresDB:           false,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	InstallParamSchema: map[string]ParamSpec{
		"site_title": {
			Type:        "string",
			Required:    true,
			Default:     "PrivateBin",
			Description: "Instance name shown in the page header ([main] name in cfg/conf.php).",
		},
	},
}
