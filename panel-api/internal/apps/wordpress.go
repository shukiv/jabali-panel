package apps

// WordPress is the descriptor for the existing one-click WordPress
// installer. It records the agent commands the API has been calling
// since M10 and the install-form fields that today live as ad-hoc
// JSON tags on the WP install handler. Step 3 hooks the generic
// /applications routes to this; step 5 wires the UI picker.
//
// Pattern is intentionally not set on admin_username — WordPress
// itself accepts a wide range of usernames and the agent's installer
// already rejects anything wp-cli refuses. Tightening here only adds
// a parallel rule to keep in sync.
var WordPress = App{
	Name:                 "wordpress",
	DisplayName:          "WordPress",
	Icon:                 "WordPressOutlined",
	Description:          "The most popular open-source CMS — blogs, marketing sites, online stores via WooCommerce.",
	Tags:                 []string{"CMS", "Blog", "E-commerce"},
	DefaultSubdirectory:  "",
	RequiresDB:           true,
	SupportedPHPVersions: nil,
	// M19 dispatcher commands (panel-agent/internal/commands/app_dispatch.go).
	// The legacy wordpress.* commands stay registered on the agent for one
	// release in case a stale panel build is rolled back; new traffic goes
	// through app.* with app_type carried in the params.
	AgentInstallCmd: "app.install",
	AgentDeleteCmd:  "app.delete",
	AgentCloneCmd:   "app.clone",
	// admin_username intentionally NOT in this schema — the API
	// generates a random 6-letter username server-side (per the
	// operator's directive: "admin username is a bad idea, should be
	// 6 letters auto generated"). The generator runs at install time
	// in api.applications.go's create handler; the response surfaces
	// the generated value in the reveal-once panel.
	InstallParamSchema: map[string]ParamSpec{
		"admin_username": AdminUsernameParam,
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Administrator email — used by WP for password resets and admin notices.",
		},
		"admin_password": {
			Type:        "password",
			Required:    false,
			Description: "Initial administrator password. Leave blank to have one generated; we surface it once on install.",
		},
		"site_title": {
			Type:        "string",
			Required:    true,
			Default:     "My WordPress Site",
			Description: "Public site title (editable from WP admin afterwards).",
		},
		"locale": {
			Type:        "string",
			Required:    false,
			Default:     "en_US",
			Description: "Two-part locale code (e.g. en_US, he_IL).",
		},
		// JAB-180: caller-chosen core version. Default "latest" is resolved by
		// wp-cli at install time; a concrete release (e.g. 6.7.1) is also
		// accepted. The agent validates the value and verify-checksums enforces
		// integrity against whatever landed.
		"version": {
			Type:        "string",
			Required:    false,
			Default:     "latest",
			Description: "WordPress version — \"latest\" (recommended) or a specific release like 6.7.1.",
		},
	},
}

// RegisterDefaults registers every first-party app descriptor on r.
// Called by app.NewWithDeps at startup. New apps are added by appending
// a Register call here.
func RegisterDefaults(r *Registry) error {
	if err := r.Register(WordPress); err != nil {
		return err
	}
	if err := r.Register(MediaWiki); err != nil {
		return err
	}
	if err := r.Register(Drupal); err != nil {
		return err
	}
	if err := r.Register(Joomla); err != nil {
		return err
	}
	if err := r.Register(PhpBB); err != nil {
		return err
	}
	if err := r.Register(OpenCart); err != nil {
		return err
	}
	if err := r.Register(PrestaShop); err != nil {
		return err
	}
	if err := r.Register(DokuWiki); err != nil {
		return err
	}
	if err := r.Register(Flarum); err != nil {
		return err
	}
	if err := r.Register(ITFlow); err != nil {
		return err
	}
	if err := r.Register(Moodle); err != nil {
		return err
	}
	return nil
}
