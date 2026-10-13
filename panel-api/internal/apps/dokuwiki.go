package apps

// DokuWiki is the descriptor for DokuWiki — a flat-file wiki with no
// database. It's the first RequiresDB=false app: the framework skips the
// MariaDB chain (provisionDBChain) and the agent installer writes
// DokuWiki's config headlessly (conf/local.php + conf/users.auth.php +
// conf/acl.auth.php) then deletes install.php so the web installer can't
// be re-run.
//
// Cloning is a flat directory copy, but there's no live ask yet, so
// AgentCloneCmd stays empty (the Clone button is hidden). Delete reuses
// the generic app.delete (rm -rf the install path; no DB to drop).
var DokuWiki = App{
	Name:                 "dokuwiki",
	DisplayName:          "DokuWiki",
	Icon:                 "BookOutlined",
	Description:          "Simple flat-file wiki — no database, versioned plain-text pages, ACLs and plugins. Ideal for docs and knowledge bases.",
	Tags:                 []string{"Wiki"},
	DefaultSubdirectory:  "wiki",
	RequiresDB:           false,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	// admin_username is optional (GH #228): leave it blank and the API
	// generates a 6-letter username; fill it to choose one.
	InstallParamSchema: map[string]ParamSpec{
		"admin_username": adminUsernameParamMatching(`^([a-z0-9][a-z0-9_]{1,31})?$`,
			"2 to 32 lowercase letters, digits or underscores, starting with a letter or digit"),
		"site_title": {
			Type:        "string",
			Required:    true,
			Default:     "My Wiki",
			Description: "Wiki title shown in the page header. Stored as $conf['title'].",
		},
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Administrator email — stored on the admin account for password resets.",
		},
		"admin_password": {
			Type:        "password",
			Required:    false,
			Description: "Initial admin password. Leave blank to have one generated and shown once.",
		},
	},
}
