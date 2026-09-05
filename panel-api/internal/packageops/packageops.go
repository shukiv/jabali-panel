// Package packageops holds the transport-neutral Hosting Package invariants
// shared by every write adapter (the REST handler and the operator CLI).
//
// JAB-306: HTTP package create/update validated resource limits, the FPM
// children cap, backup retention/kinds and the nspawn image name before
// persisting, but the direct-DB CLI (`jabali package create|edit`) persisted
// its field subset with no validation at all — so `package create --cpu 999999`
// or `--memory-mb 2000000` stored a row the REST API rejects with 422. This
// package owns the one predicate both adapters run before Create/Update, so a
// value one adapter refuses can never be smuggled in through the other. It
// mirrors the existing REST+CLI-shared limits.ValidateOverrideBounds (JAB-309).
//
// Only VALIDATION lives here, not defaulting: create relies on the GORM
// `default:` column tags (FPM cap 20, worker mem 64, retention "reject",
// version-defaults "{}") to fill unset fields at INSERT, and update PATCHes a
// row that is already valid — so there is no shared defaulting step to extract,
// and applying create-defaults on an update would wrongly "heal" fields the
// caller never touched.
package packageops

import (
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/phppoolops"
)

// Sentinel errors let a caller that wants adapter-specific reporting map a
// failure to its own code; the CLI just fails closed on any non-nil error.
var (
	// ErrFpmCapTooHigh is returned when fpm_max_children_cap exceeds the
	// admin ceiling (phppoolops.AdminMaxChildrenCap).
	ErrFpmCapTooHigh = errors.New("fpm_max_children_cap exceeds the admin cap")
	// ErrInvalidBackupRetentionPolicy is returned for a retention policy that
	// is neither "reject", "prune", nor the empty string.
	ErrInvalidBackupRetentionPolicy = errors.New("invalid backup retention policy")
	// ErrInvalidNspawnImage is returned when nspawn_image_version is set but is
	// not the [a-z0-9-]+ shape.
	ErrInvalidNspawnImage = errors.New("invalid nspawn image version")
)

// Validate reports the first invariant a hosting package row violates, or nil
// when every adapter may safely persist it. It is the single authority for
// "what is a legal hosting_packages row" and MUST be called by every write
// path (REST create/update and CLI create/edit) immediately before the
// repository Create/Update.
//
// The bounded resource limits (cpu/memory/io/tasks) are the fields the CLI can
// actually set today, so they are the ones this closes in practice; the FPM
// cap, retention, backup-kinds and nspawn checks make the module the complete
// invariant set for when the CLI grows flags for those fields (and as
// defense-in-depth for the REST path, which already rejects them inline).
func Validate(p *models.HostingPackage) error {
	// Resource-limit bounds — identical to the REST handler's former
	// validatePackageLimits. Zero is always legal (unlimited); DiskQuotaMB is
	// intentionally unbounded (a filesystem-layer concern), matching
	// limits.EffectiveLimits.Validate.
	e := limits.EffectiveLimits{
		DiskQuotaMB:     p.DiskQuotaMB,
		CPUQuotaPercent: p.CPUQuotaPercent,
		MemoryLimitMB:   p.MemoryLimitMB,
		IOReadMbps:      p.IOReadMbps,
		IOWriteMbps:     p.IOWriteMbps,
		MaxTasks:        p.MaxTasks,
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if p.FpmMaxChildrenCap > phppoolops.AdminMaxChildrenCap {
		return fmt.Errorf("%w: must be <= %d", ErrFpmCapTooHigh, phppoolops.AdminMaxChildrenCap)
	}
	if !models.IsValidBackupRetentionPolicy(p.BackupRetentionPolicy) {
		return fmt.Errorf("%w: %q (must be reject or prune)", ErrInvalidBackupRetentionPolicy, p.BackupRetentionPolicy)
	}
	// NormalizeBackupKindsCSV rejects any unknown kind. It is idempotent on its
	// own output — a stored, already-normalised value re-normalises to itself
	// with no error — so running it here on a persisted row is a pure validity
	// check.
	if _, err := models.NormalizeBackupKindsCSV(p.AllowedBackupDestinationKinds); err != nil {
		return err
	}
	if p.NspawnImageVersion != nil && !isImageNamePattern(*p.NspawnImageVersion) {
		return fmt.Errorf("%w: %q (must match [a-z0-9-]+)", ErrInvalidNspawnImage, *p.NspawnImageVersion)
	}
	return nil
}

// isImageNamePattern matches the [a-z0-9-]+ shape. A private copy of the
// identical helper in internal/api and internal/settingsops — duplicated
// rather than cross-imported, matching the precedent those packages set (api
// imports settingsops, so settingsops can't import api; packageops is a leaf
// both import, so it keeps its own copy too).
func isImageNamePattern(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}
