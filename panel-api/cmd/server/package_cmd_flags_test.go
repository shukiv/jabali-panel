package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/packageops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/phppoolops"
)

// packageFieldFlag maps each operator-editable hosting_packages field (by its
// json tag — the wire name the REST create/update request uses) to the CLI flag
// that sets it. This is the concrete form of JAB-306 AC3 ("new entitlement
// fields cannot be omitted by one adapter"): the parity test below requires
// every editable model field to appear here AND every flag to be registered on
// both `package create` and `package edit`.
var packageFieldFlag = map[string]string{
	"name":                             "name",
	"disk_quota_mb":                    "disk-mb",
	"cpu_quota_percent":                "cpu",
	"memory_limit_mb":                  "memory-mb",
	"io_read_mbps":                     "io-read-mbps",
	"io_write_mbps":                    "io-write-mbps",
	"max_tasks":                        "max-tasks",
	"bandwidth_quota_mb":               "bw-mb",
	"max_domains":                      "domains",
	"max_email_accounts":               "emails",
	"max_databases":                    "databases",
	"max_database_users":               "max-db-users",
	"max_docker_apps":                  "max-docker-apps",
	"max_python_apps":                  "max-python-apps",
	"max_ftp_accounts":                 "max-ftp-accounts",
	"max_backups":                      "max-backups",
	"scheduled_backups_enabled":        "scheduled-backups",
	"max_backup_schedules":             "max-backup-schedules",
	"allowed_backup_destination_kinds": "backup-kinds",
	"backup_retention_policy":          "backup-retention",
	"ssh_enabled":                      "ssh",
	"cgi_enabled":                      "cgi",
	"php_exec_enabled":                 "php-exec",
	"fpm_max_children_cap":             "fpm-max-children",
	"fpm_worker_mem_mb":                "fpm-worker-mem-mb",
	"fpm_user_can_edit":                "fpm-user-can-edit",
	"fpm_advanced_mode":                "fpm-advanced",
	"fpm_version_defaults":             "fpm-version-defaults",
	"docker_app_slugs":                 "docker-app-slugs",
	"nspawn_image_version":             "nspawn-image",
}

// packageNonEditableJSON are hosting_packages columns that are identity /
// bookkeeping, not operator-editable entitlements — they legitimately have no
// CLI flag.
var packageNonEditableJSON = map[string]bool{
	"id":         true,
	"created_at": true,
	"updated_at": true,
}

// TestPackageCLI_EntitlementFlagParity is the AC3 guarantee: the operator CLI
// exposes every hosting_packages entitlement the REST adapter does, so a field
// can never be set through one adapter but silently defaulted through the other.
// It is grounded in the model struct (the source of truth), so a new column
// added without a matching create/edit flag fails here.
func TestPackageCLI_EntitlementFlagParity(t *testing.T) {
	// 1. Every editable model field maps to a flag (catches a new column with no
	//    CLI flag mapping).
	rt := reflect.TypeOf(models.HostingPackage{})
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" || packageNonEditableJSON[tag] {
			continue
		}
		if _, ok := packageFieldFlag[tag]; !ok {
			t.Errorf("hosting_packages.%s has no CLI flag — add one to package create/edit (JAB-306 AC3), or mark it non-editable", tag)
		}
	}

	// 2. Every mapped flag is registered on BOTH create and edit (catches a
	//    dropped flag registration in either adapter).
	create := newPackageCreateCmd()
	edit := newPackageEditCmd()
	for tag, flag := range packageFieldFlag {
		if create.Flags().Lookup(flag) == nil {
			t.Errorf("create: field %q → --%s not registered", tag, flag)
		}
		if edit.Flags().Lookup(flag) == nil {
			t.Errorf("edit: field %q → --%s not registered", tag, flag)
		}
	}
}

// --- create builder transforms (mirror internal/api/packages.go create) ---

func TestBuildPackageFromCreateFlags_AppliesFpmAndRetentionDefaults(t *testing.T) {
	p, err := buildPackageFromCreateFlags(packageCreateFlags{name: "x"})
	require.NoError(t, err)
	require.Equal(t, uint32(20), p.FpmMaxChildrenCap, "unset FPM cap defaults to 20")
	require.Equal(t, uint32(64), p.FpmWorkerMemMb, "unset FPM worker mem defaults to 64")
	require.Equal(t, "{}", p.FpmVersionDefaults, "unset FPM version defaults to {}")
	require.Equal(t, models.BackupRetentionReject, p.BackupRetentionPolicy, "unset retention defaults to reject")
	require.Nil(t, p.NspawnImageVersion, "unset nspawn image stays NULL (server default)")
}

func TestBuildPackageFromCreateFlags_AdvancedImpliesCanEdit(t *testing.T) {
	p, err := buildPackageFromCreateFlags(packageCreateFlags{name: "x", fpmAdvanced: true})
	require.NoError(t, err)
	require.True(t, p.FpmUserCanEdit, "--fpm-advanced must imply --fpm-user-can-edit")
}

func TestBuildPackageFromCreateFlags_NormalizesBackupKinds(t *testing.T) {
	p, err := buildPackageFromCreateFlags(packageCreateFlags{name: "x", backupKinds: " s3 , S3 , local "})
	require.NoError(t, err)
	require.Equal(t, "s3,local", p.AllowedBackupDestinationKinds, "kinds are lower-cased, trimmed, deduped, order-preserving")
}

func TestBuildPackageFromCreateFlags_PinsNspawnImage(t *testing.T) {
	p, err := buildPackageFromCreateFlags(packageCreateFlags{name: "x", nspawnImage: "  debian-12  "})
	require.NoError(t, err)
	require.NotNil(t, p.NspawnImageVersion)
	require.Equal(t, "debian-12", *p.NspawnImageVersion, "nspawn image is trimmed and pinned")
}

// The four Validate branches the prior slice noted were "not settable from CLI
// today" are now reachable from live CLI input — prove each rejects.

func TestBuildPackageFromCreateFlags_ValidateRejectsFpmCapOverMax(t *testing.T) {
	p, err := buildPackageFromCreateFlags(packageCreateFlags{name: "x", fpmMaxChildren: phppoolops.AdminMaxChildrenCap + 1})
	require.NoError(t, err)
	require.ErrorIs(t, packageops.Validate(p), packageops.ErrFpmCapTooHigh)
}

func TestBuildPackageFromCreateFlags_RejectsUnknownBackupKind(t *testing.T) {
	_, err := buildPackageFromCreateFlags(packageCreateFlags{name: "x", backupKinds: "not-a-real-kind"})
	require.Error(t, err, "an unknown backup kind is rejected at build time")
}

func TestBuildPackageFromCreateFlags_ValidateRejectsBadRetention(t *testing.T) {
	p, err := buildPackageFromCreateFlags(packageCreateFlags{name: "x", backupRetention: "delete-everything"})
	require.NoError(t, err)
	require.ErrorIs(t, packageops.Validate(p), packageops.ErrInvalidBackupRetentionPolicy)
}

func TestBuildPackageFromCreateFlags_ValidateRejectsBadNspawn(t *testing.T) {
	p, err := buildPackageFromCreateFlags(packageCreateFlags{name: "x", nspawnImage: "Bad/Image!"})
	require.NoError(t, err)
	require.ErrorIs(t, packageops.Validate(p), packageops.ErrInvalidNspawnImage)
}

// --- edit builder transforms (mirror internal/api/packages.go update) ---

// changedSet returns a cmd.Flags().Changed stand-in that reports the named flags
// as set — lets us drive applyPackageEditFlags without cobra/DB plumbing.
func changedSet(flags ...string) func(string) bool {
	set := map[string]bool{}
	for _, f := range flags {
		set[f] = true
	}
	return func(s string) bool { return set[s] }
}

func TestApplyPackageEditFlags_OnlyNamedFieldsChange(t *testing.T) {
	// A field the operator did not name must never be healed — the trap the
	// per-field gating exists to avoid.
	p := &models.HostingPackage{MaxDomains: 5, BackupRetentionPolicy: "prune"}
	changed, err := applyPackageEditFlags(changedSet("disk-mb"), p, packageEditFlags{diskMB: 100})
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, uint32(100), p.DiskQuotaMB)
	require.Equal(t, uint32(5), p.MaxDomains, "unnamed field must not change")
	require.Equal(t, "prune", p.BackupRetentionPolicy, "unnamed retention must not be healed to reject")
}

func TestApplyPackageEditFlags_NoFlagsIsNoChange(t *testing.T) {
	p := &models.HostingPackage{}
	changed, err := applyPackageEditFlags(changedSet(), p, packageEditFlags{})
	require.NoError(t, err)
	require.False(t, changed, "no flags named ⇒ no change")
}

func TestApplyPackageEditFlags_AdvancedImpliesCanEdit(t *testing.T) {
	p := &models.HostingPackage{FpmUserCanEdit: false}
	changed, err := applyPackageEditFlags(changedSet(), p, packageEditFlags{fpmAdvanced: "true"})
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, p.FpmAdvancedMode)
	require.True(t, p.FpmUserCanEdit, "--fpm-advanced=true implies can-edit on edit too")
}

func TestApplyPackageEditFlags_NspawnEmptyClearsPin(t *testing.T) {
	v := "debian-12"
	p := &models.HostingPackage{NspawnImageVersion: &v}
	changed, err := applyPackageEditFlags(changedSet("nspawn-image"), p, packageEditFlags{nspawnImage: ""})
	require.NoError(t, err)
	require.True(t, changed)
	require.Nil(t, p.NspawnImageVersion, "empty --nspawn-image clears the pin (back to server default)")
}

func TestApplyPackageEditFlags_NormalizesBackupKinds(t *testing.T) {
	p := &models.HostingPackage{}
	changed, err := applyPackageEditFlags(changedSet("backup-kinds"), p, packageEditFlags{backupKinds: " GCS , gcs , b2 "})
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "gcs,b2", p.AllowedBackupDestinationKinds)
}

func TestApplyPackageEditFlags_RejectsUnknownBackupKind(t *testing.T) {
	p := &models.HostingPackage{}
	_, err := applyPackageEditFlags(changedSet("backup-kinds"), p, packageEditFlags{backupKinds: "nope"})
	require.Error(t, err)
}

func TestApplyPackageEditFlags_TriStateFalseClearsToggle(t *testing.T) {
	p := &models.HostingPackage{SSHEnabled: true}
	changed, err := applyPackageEditFlags(changedSet(), p, packageEditFlags{sshEnabled: "false"})
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, p.SSHEnabled, "--ssh=false disables")
}

func TestApplyPackageEditFlags_TriStateGarbageIsError(t *testing.T) {
	// A garbage toggle value fails loud instead of silently no-opping a security
	// toggle (php-exec here) — and nothing is persisted.
	p := &models.HostingPackage{PHPExecEnabled: false}
	changed, err := applyPackageEditFlags(changedSet(), p, packageEditFlags{phpExec: "yes"})
	require.Error(t, err)
	require.False(t, changed)
	require.False(t, p.PHPExecEnabled, "row untouched on a bad toggle value")
}
