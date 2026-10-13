package apps

// MediaWiki is the descriptor for the MediaWiki CMS — the platform
// behind Wikipedia. Picked as the third app specifically because it
// exercises a heavier per-app install flow than WordPress's wp-cli:
// the agent installer runs MediaWiki's own `php maintenance/install.php`
// CLI with pre-supplied credentials so the user never sees the web
// installer wizard, then drops the generated LocalSettings.php into
// place.
//
// Cloning a MediaWiki install means duplicating the database AND the
// images/ directory AND the LocalSettings.php — the wp-clone shape
// doesn't translate. Defer until a real ask shows up; AgentCloneCmd
// stays empty so the Clone button is hidden in the UI.
var MediaWiki = App{
	Name:                 "mediawiki",
	DisplayName:          "MediaWiki",
	Icon:                 "BookOutlined",
	Description:          "The wiki engine behind Wikipedia — full revision history, namespaces, extensions, scales to encyclopaedias.",
	Tags:                 []string{"Wiki"},
	DefaultSubdirectory:  "wiki",
	RequiresDB:           true,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	// admin_username is optional (GH #228): blank → the API generates a
	// 6-letter username; either way the first letter is uppercased to
	// satisfy MediaWiki's username-must-start-with-uppercase rule.
	InstallParamSchema: map[string]ParamSpec{
		"admin_username": adminUsernameParamMatching(`^([a-zA-Z][a-zA-Z0-9_]{1,31})?$`,
			"2 to 32 letters, digits or underscores, starting with a letter (MediaWiki capitalizes it)"),
		"site_title": {
			Type:        "string",
			Required:    true,
			Default:     "My MediaWiki",
			Description: "Public wiki title; passed as the first positional argument to MediaWiki's CLI installer.",
		},
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Administrator email — used by MediaWiki for password resets.",
		},
		"admin_password": adminPasswordParam(`^(.{10,})?$`,
			"Initial admin password, at least 10 characters. Leave blank to have one generated."),
		"language": {
			Type:        "string",
			Required:    false,
			Default:     "en",
			Description: "Wiki content language (ISO 639 code, e.g. en, he, fr).",
		},
	},
}
