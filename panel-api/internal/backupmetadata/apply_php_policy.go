package backupmetadata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: an uploaded file was written by whoever made it, so a domain it
// brings takes a PHP setting only when the account's package lets a tenant set
// it (GH #1701), as the PHP settings page does for the tenant. open_basedir and
// allow_url_fopen need tenant_privileged, which an account without a package
// never has.

// phpLimitField is one per-domain PHP setting a backup carries, by the
// directive the package's PHP policy names. It covers every directive the PHP
// settings page writes (models.PHPPolicyDirectives).
type phpLimitField struct {
	directive string
	set       bool
	clear     func()
}

func phpLimitFields(row *models.Domain) []phpLimitField {
	return []phpLimitField{
		{"memory_limit", row.PHPMemoryLimit != nil, func() { row.PHPMemoryLimit = nil }},
		{"upload_max_filesize", row.PHPUploadMaxFilesize != nil, func() { row.PHPUploadMaxFilesize = nil }},
		{"post_max_size", row.PHPPostMaxSize != nil, func() { row.PHPPostMaxSize = nil }},
		{"max_input_vars", row.PHPMaxInputVars != nil, func() { row.PHPMaxInputVars = nil }},
		{"max_execution_time", row.PHPMaxExecutionTime != nil, func() { row.PHPMaxExecutionTime = nil }},
		{"max_input_time", row.PHPMaxInputTime != nil, func() { row.PHPMaxInputTime = nil }},
		{"display_errors", row.PHPDisplayErrors != nil, func() { row.PHPDisplayErrors = nil }},
		{"error_reporting", row.PHPErrorReporting != nil, func() { row.PHPErrorReporting = nil }},
		{"date.timezone", row.PHPTimezone != nil, func() { row.PHPTimezone = nil }},
		{"log_errors", row.PHPLogErrors != nil, func() { row.PHPLogErrors = nil }},
		{"file_uploads", row.PHPFileUploads != nil, func() { row.PHPFileUploads = nil }},
		{"short_open_tag", row.PHPShortOpenTag != nil, func() { row.PHPShortOpenTag = nil }},
		{"open_basedir", row.PHPOpenBasedir != nil, func() { row.PHPOpenBasedir = nil }},
		{"allow_url_fopen", row.PHPAllowURLFopen != nil, func() { row.PHPAllowURLFopen = nil }},
	}
}

// restoredPHPLimitsPolicy clears each PHP setting on row that the account's
// package lets only an administrator set, and returns a line for each. When
// the package can't be read, it clears every one: the domain then uses the
// server's defaults.
func restoredPHPLimitsPolicy(ctx context.Context, d Deps, userID string, row *models.Domain) []string {
	var set []phpLimitField
	for _, f := range phpLimitFields(row) {
		if f.set {
			set = append(set, f)
		}
	}
	if len(set) == 0 {
		return nil
	}
	pkg, err := accountPackage(ctx, d, userID)
	var notes []string
	for _, f := range set {
		switch {
		case err != nil:
			f.clear()
			notes = append(notes, fmt.Sprintf("PHP %s not restored: the account's package could not be read (%v); the server's default applies", f.directive, err))
		case !pkg.PHPSettingLevelFor(f.directive).TenantMaySet():
			f.clear()
			notes = append(notes, fmt.Sprintf("PHP %s not restored: the account's package lets only an administrator set it; the server's default applies", f.directive))
		}
	}
	return notes
}

// setBackupPHPSettings sets the PHP settings beyond the limits on dst to the
// backup's. open_basedir names the account's folders under the username the
// backup was made with; they move onto the account's username here, as the
// document root does, and the checks then hold every entry to that home.
func setBackupPHPSettings(dst *models.Domain, dm internalbackup.MetadataDomain, bundleUser, account string) {
	dst.PHPDisplayErrors, dst.PHPErrorReporting, dst.PHPTimezone = dm.PHPDisplayErrors, dm.PHPErrorReporting, dm.PHPTimezone
	dst.PHPLogErrors, dst.PHPFileUploads, dst.PHPShortOpenTag = dm.PHPLogErrors, dm.PHPFileUploads, dm.PHPShortOpenTag
	dst.PHPAllowURLFopen, dst.PHPOpenBasedir = dm.PHPAllowURLFopen, nil
	if dm.PHPOpenBasedir != nil {
		entries := strings.Split(*dm.PHPOpenBasedir, ":")
		for i, e := range entries {
			entries[i] = rehomePath(e, "/home", bundleUser, account)
		}
		v := strings.Join(entries, ":")
		dst.PHPOpenBasedir = &v
	}
}

// accountPackage is the account's hosting package, nil when it has none.
func accountPackage(ctx context.Context, d Deps, userID string) (*models.HostingPackage, error) {
	if d.Users == nil || d.Packages == nil {
		return nil, errors.New("the package checks are not wired")
	}
	u, err := d.Users.FindByID(ctx, userID)
	if err != nil || u == nil {
		return nil, fmt.Errorf("look up the account: %v", err)
	}
	if u.PackageID == nil || *u.PackageID == "" {
		return nil, nil
	}
	pkg, err := d.Packages.FindByID(ctx, *u.PackageID)
	if err != nil || pkg == nil {
		return nil, fmt.Errorf("look up the account's package: %v", err)
	}
	return pkg, nil
}
