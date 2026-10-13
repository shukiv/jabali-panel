package apps

// Flarum is the descriptor for Flarum — a modern, lightweight forum
// platform (the successor to esoTalk/FluxBB). RequiresDB=true.
//
// Unlike phpBB/Drupal which ship a rooted tarball, Flarum is normally
// installed via `composer create-project`. We sidestep that by pulling
// the upstream "no-public-dir" prebuilt archive (vendor/ pre-bundled,
// extension-manager included) and running Flarum's headless CLI
// installer (`php flarum install -f <config.yml>`). See the agent's
// flarum_install.go for the download pin + install flow.
//
// Flarum's default layout serves from a public/ subdirectory; the
// no-public-dir variant flattens that so the install root IS the
// webroot. That keeps it inside jabali's docroot+subdir model, but it
// also exposes config.php / storage/ / vendor/ to the web — the agent
// installer writes an nginx deny snippet to block them (the rules from
// Flarum's own .nginx.conf).
//
// Clone is intentionally empty: a clean Flarum clone needs a duplicate
// DB + an assets/ + storage/ rsync + a config.php url rewrite. No live
// ask yet, so AgentCloneCmd stays empty (the Clone button is hidden).
// Delete reuses the generic app.delete (rm -rf the install path) and
// the install row's db_id drives the DB drop, same as phpBB.
var Flarum = App{
	Name:                 "flarum",
	DisplayName:          "Flarum",
	Icon:                 "MessageOutlined",
	Description:          "Modern, fast and free forum software — simple, extensible discussion platform with a clean mobile-first UI.",
	Tags:                 []string{"Forum"},
	DefaultSubdirectory:  "forum",
	RequiresDB:           true,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	// admin_username intentionally NOT in this schema — the API
	// generates a random 6-letter username server-side, same as the
	// other apps. The generated value surfaces once in the reveal
	// panel on install.
	InstallParamSchema: map[string]ParamSpec{
		"admin_username": adminUsernameParamMatching(`^([a-zA-Z0-9][a-zA-Z0-9_-]{2,29})?$`,
			"3 to 30 letters, digits, underscores or hyphens, starting with a letter or digit"),
		"site_title": {
			Type:        "string",
			Required:    true,
			Default:     "My Forum",
			Description: "Public forum title shown in the header; passed to Flarum's installer as settings.forum_title.",
		},
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Administrator email — Flarum uses it for password resets and admin notices.",
		},
		"admin_password": adminPasswordParam(`^(.{8,})?$`,
			"Initial admin password, at least 8 characters. Leave blank to have one generated."),
	},
}
