package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// phpFlagPinParams returns the domain.create params for the three PHP flags
// the agent pins on every PHP vhost (GH #1701 Slice 2): log_errors,
// file_uploads and short_open_tag. Each is the domain's own value, else the
// bound pool's flag override. A flag with neither is left out, and the agent
// pins the box php.ini baseline for it.
//
// The pool override matters because the agent's PHP_VALUE overrides even a
// pool's php_admin_flag: pinning the php.ini baseline over an admin's pool
// file_uploads=off would quietly turn uploads back on. An override counts
// whether it was saved as a flag or a value, and is read the way PHP reads a
// boolean ("1", "yes" and "true" are on too). When the pool's overrides cannot
// be read, or two overrides disagree on one flag, php_flags_inherit_unknown
// tells the agent not to pin the inherited value this pass (only the domain's
// own values), rather than pin a baseline that may be wrong.
func (r *Reconciler) phpFlagPinParams(ctx context.Context, domain *models.Domain, poolID string) map[string]any {
	flags := []struct {
		param, directive string
		v                *bool
	}{
		{"php_log_errors", "log_errors", domain.PHPLogErrors},
		{"php_file_uploads", "file_uploads", domain.PHPFileUploads},
		{"php_short_open_tag", "short_open_tag", domain.PHPShortOpenTag},
	}
	tracked := map[string]bool{}
	for _, f := range flags {
		tracked[f.directive] = true
	}
	out := map[string]any{}
	poolFlags := map[string]bool{}
	conflict := map[string]bool{}
	switch {
	case poolID == "" || r.phpPoolIniOverrides == nil:
		// Not expected for a PHP domain (it always has a bound pool), but the
		// pool's flags are then unknown, not absent.
		out["php_flags_inherit_unknown"] = true
	default:
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ovs, err := r.phpPoolIniOverrides.ListByPool(lctx, poolID)
		cancel()
		if err != nil {
			r.log.Warn("php flag pins: pool ini overrides unreadable; pinning only the domain's own values",
				"domain_id", domain.ID, "pool_id", poolID, "err", err)
			out["php_flags_inherit_unknown"] = true
			break
		}
		for _, o := range ovs {
			if !tracked[o.Directive] {
				continue
			}
			on := models.PHPIniBoolOn(o.Value)
			if prev, seen := poolFlags[o.Directive]; seen && prev != on {
				conflict[o.Directive] = true
				r.log.Warn("php flag pins: pool has conflicting overrides for one flag; pinning only the domain's own values",
					"domain_id", domain.ID, "pool_id", poolID, "directive", o.Directive)
				out["php_flags_inherit_unknown"] = true
			}
			poolFlags[o.Directive] = on
		}
	}
	for _, f := range flags {
		if f.v != nil {
			out[f.param] = *f.v
			continue
		}
		if pv, ok := poolFlags[f.directive]; ok && !conflict[f.directive] {
			out[f.param] = pv
		}
	}
	return out
}
