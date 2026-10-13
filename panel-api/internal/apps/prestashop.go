package apps

// PrestaShop is the descriptor for PrestaShop 8 — the 13th app in the
// catalog and the third e-commerce platform. RequiresDB=true; install
// runs `php install/index_cli.php` after the agent unwraps the
// upstream double-zip distribution (PrestaShop ships an outer zip
// containing prestashop.zip + an Install_PrestaShop.html landing).
//
// Clone is intentionally empty for now: a clean PrestaShop clone
// needs duplicate DB + image rsync + parameters.php domain rewrite +
// admin URL rewrite. Defer.
var PrestaShop = App{
	Name:                 "prestashop",
	DisplayName:          "PrestaShop",
	Icon:                 "ShoppingCartOutlined",
	Description:          "Open-source e-commerce platform with a strong EU/SMB following — multi-store, multi-currency, large module marketplace.",
	Tags:                 []string{"E-commerce"},
	DefaultSubdirectory:  "shop",
	RequiresDB:           true,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	InstallParamSchema: map[string]ParamSpec{
		"site_title": {
			Type:        "string",
			Required:    true,
			Default:     "My Shop",
			Description: "Public store name; passed as --name to PrestaShop's CLI installer.",
		},
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Administrator email — also the admin login. PrestaShop requires email-format usernames.",
		},
		"admin_password": adminPasswordParam(`^(.{8,})?$`,
			"Initial admin password, at least 8 characters. Leave blank to have one generated."),
		"admin_first_name": {
			Type:        "string",
			Required:    false,
			Default:     "Site",
			Description: "First name for the admin profile (shown in admin UI top-right).",
		},
		"admin_last_name": {
			Type:        "string",
			Required:    false,
			Default:     "Owner",
			Description: "Last name for the admin profile.",
		},
		"country": {
			Type:        "string",
			Required:    false,
			Default:     "us",
			Description: "Default store country — two-letter ISO 3166-1 alpha-2 code (e.g. us, gb, de, il). Drives default tax/currency/shipping rules.",
		},
		"language": {
			Type:        "string",
			Required:    false,
			Default:     "en",
			Description: "Default storefront/admin language (en, fr, de, …).",
		},
	},
}
