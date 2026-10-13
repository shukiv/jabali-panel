package apps

// Moodle is the descriptor for the Moodle LMS (GH #310) — a PHP + MariaDB
// learning platform. Shipped as a native PHP 1-click rather than a Docker
// catalog entry: there's no clean official production image, and Moodle runs
// fine on the per-tenant PHP-FPM pool the panel already provisions.
//
// The agent installer runs Moodle's headless `admin/cli/install.php` with
// pre-supplied credentials, creates a moodledata directory outside the web
// root, and writes config.php. admin_username is omitted from the schema so a
// lowercase one is generated (Moodle lowercases usernames). Clone is a poor
// fit (DB + moodledata + config.php), so AgentCloneCmd stays empty.
var Moodle = App{
	Name:                 "moodle",
	DisplayName:          "Moodle",
	Icon:                 "BookOutlined",
	Description:          "The world's most widely used open-source LMS — courses, quizzes, grading, and a huge plugin ecosystem.",
	Tags:                 []string{"Education", "LMS"},
	DefaultSubdirectory:  "moodle",
	RequiresDB:           true,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	InstallParamSchema: map[string]ParamSpec{
		"site_title": {
			Type:        "string",
			Required:    true,
			Default:     "My Moodle",
			Description: "Full site name shown to learners; Moodle's --fullname.",
		},
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Site administrator email — used by Moodle for notifications + password resets.",
		},
		"admin_password": adminPasswordParam(`^(.{8,})?$`,
			"Initial admin password, at least 8 characters. Leave blank to generate one. Moodle's default policy also wants upper and lower case, a digit and a symbol for later password changes."),
		"language": {
			Type:        "string",
			Required:    false,
			Default:     "en",
			Description: "Moodle language pack (e.g. en, fr, es, pt_br).",
		},
	},
}
