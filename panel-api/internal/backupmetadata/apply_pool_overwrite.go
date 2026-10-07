package backupmetadata

import (
	"context"
	"fmt"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/phppoolops"
)

// GH #1993: a backup pool restored onto a PHP pool the account already has
// brings its PHP settings, and with OverwriteRows its process settings too,
// held to the checks the pool page runs.

// restoreExistingPool brings the backup's pool p onto existing, the account's
// own pool. When anything changes, the pool is marked pending so the
// reconciler applies it again; otherwise the change would not reach PHP.
func restoreExistingPool(ctx context.Context, d Deps, r *ApplyResult, userID string, existing *models.PHPPool, p internalbackup.MetadataPHPPool) {
	r.Skipped++
	tuned := d.OverwriteRows && overwritePoolTuning(ctx, d, r, userID, existing, p)
	ini := restorePoolIni(ctx, d, r, existing.ID, p.IniOverrides)
	if !tuned && !ini {
		return
	}
	existing.Status = "pending"
	existing.UpdatedAt = time.Now().UTC()
	if err := d.PHPPools.Update(ctx, existing); err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("php_pool %s: update: %v", p.ID, err))
	}
}

// overwritePoolTuning gives existing the backup's process settings, held to
// the pool page's limits and the account's package cap as a restored pool's
// are, and reports whether they changed. Settings the pool page would refuse,
// or whose cap can't be read, leave the pool's own in place.
func overwritePoolTuning(ctx context.Context, d Deps, r *ApplyResult, userID string, existing *models.PHPPool, p internalbackup.MetadataPHPPool) bool {
	if p.PmMode == existing.PmMode && p.PmMaxChildren == existing.PmMaxChildren && p.ProcessIdleTimeoutSeconds == existing.ProcessIdleTimeoutSeconds {
		return false
	}
	label := "php_pool " + p.ID
	limit, err := packageFPMCap(ctx, d, userID)
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("%s: process settings not updated: %v", label, err))
		return false
	}
	t, refused, notes, err := poolTuning(limit, p.PmMode, p.PmMaxChildren, p.ProcessIdleTimeoutSeconds)
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("%s: process settings not updated: %v", label, err))
		return false
	}
	if refused != "" {
		r.Errors = append(r.Errors, fmt.Sprintf("%s: process settings not updated (%s); the pool keeps its own", label, refused))
		return false
	}
	for _, n := range notes {
		r.Errors = append(r.Errors, label+": "+n)
	}
	if t.PmMode == existing.PmMode && t.PmMaxChildren == existing.PmMaxChildren &&
		t.ProcessIdleTimeoutSeconds == existing.ProcessIdleTimeoutSeconds && t.PmStartServers == existing.PmStartServers &&
		t.PmMinSpareServers == existing.PmMinSpareServers && t.PmMaxSpareServers == existing.PmMaxSpareServers {
		return false
	}
	setPoolTuning(existing, t)
	return true
}

// uploadedIniOverride is o's kind and value as the pool page would store
// them, or the page's reason to refuse it.
func uploadedIniOverride(o internalbackup.MetadataPHPPoolIniOverride) (kind, value, problem string) {
	if o.Directive == "" {
		return "", "", "directive is required"
	}
	kind = o.Kind
	if kind == "" {
		kind = "value"
	}
	if kind != "value" && kind != "flag" {
		return "", "", "kind must be 'value' or 'flag'"
	}
	value, problem = phppoolops.ValidIniOverrideValue(kind, o.Value)
	return kind, value, problem
}
