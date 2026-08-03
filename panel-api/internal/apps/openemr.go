package apps

// OpenEMR is the descriptor for OpenEMR (GH #631) — a large open-source
// electronic health records + medical practice management system (PHP +
// MariaDB). Unlike the Laravel apps it serves from the install root (no
// public/ flatten) and installs headlessly via its own
// contrib/util/installScripts/InstallerAuto.php, which loads the schema and
// creates the admin against the jabali-provisioned database + user
// (no_root_db_access mode — no MySQL root needed).
//
// admin_username is offered (OpenEMR logs in by username); default "admin".
var OpenEMR = App{
	Name:                 "openemr",
	DisplayName:          "OpenEMR",
	Icon:                 "MedicineBoxOutlined",
	Description:          "Free, ONC-certified electronic health records and medical practice management — scheduling, billing, e-prescribing, patient portal. Large PHP + MariaDB application.",
	Tags:                 []string{"Healthcare", "EHR"},
	DefaultSubdirectory:  "openemr",
	RequiresDB:           true,
	SupportedPHPVersions: nil,
	AgentInstallCmd:      "app.install",
	AgentDeleteCmd:       "app.delete",
	AgentCloneCmd:        "",
	InstallParamSchema: map[string]ParamSpec{
		"admin_username": AdminUsernameParam,
		"site_title": {
			Type:        "string",
			Required:    true,
			Default:     "OpenEMR",
			Description: "Practice/site name — OpenEMR's initial group name.",
		},
		"admin_email": {
			Type:        "email",
			Required:    true,
			Description: "Administrator email, stored on the admin account.",
		},
		"admin_password": {
			Type:        "password",
			Required:    false,
			Description: "Initial admin password. Leave blank to generate one and show it once.",
		},
	},
}
