package apps

// OpenCart is the descriptor for OpenCart 4 — the 11th app in the
// catalog and the first e-commerce platform. RequiresDB=true; install
// runs `php install/cli_install.php install` with the DB + admin args
// so the user never sees the web installer.
//
// Clone is intentionally empty for now: a clean OpenCart clone needs
// duplicate DB + image/* rsync + admin URL rewrite. Defer.
var OpenCart = App{
	Name:                 "opencart",
	DisplayName:          "OpenCart",
	Icon:                 "ShoppingCartOutlined",
	Description:          "Open-source e-commerce platform — product catalog, carts, checkout, order management, and a marketplace of contributed extensions.",
	Tags:                 []string{"E-commerce"},
	DefaultSubdirectory:  "shop",
	RequiresDB:           true,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	InstallParamSchema: map[string]ParamSpec{
		"admin_username": adminUsernameParamMatching(`^([a-zA-Z0-9][a-zA-Z0-9_-]{2,19})?$`,
			"3 to 20 letters, digits, underscores or hyphens, starting with a letter or digit"),
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Administrator email — used by OpenCart for password resets and as the default From-address for transactional emails.",
		},
		"admin_password": adminPasswordParam(`^(.{5,20})?$`,
			"Initial admin password, 5 to 20 characters (OpenCart's limit). Leave blank to have one generated."),
	},
}
