package backupmetadata

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: an uploaded file was written by whoever made it, so a domain it
// brings takes a PHP limit only when the account's package lets a tenant set
// it (GH #1701), as the PHP settings page does for the tenant.

// phpLimitField is one per-domain PHP limit a backup carries, by the directive
// the package's PHP policy names.
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
	}
}

// restoredPHPLimitsPolicy clears each PHP limit on row that the account's
// package lets only an administrator set, and returns a line for each. When
// the package can't be read, it clears every limit: the domain then uses the
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
