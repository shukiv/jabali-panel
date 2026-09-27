package appsecops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// exclusions.go — GH #1649. Add and remove an operator CRS exclusion from the
// admin panel, live.
//
// `jabali appsec exclusion add` only stores a row, and the change reaches the
// WAF when root next runs render-config. The panel applies at once through
// OperatorApplyVerb instead. If that apply fails, the row change is undone, so
// the list the operator sees is what the WAF runs: an exclusion that the
// operator saw fail must not go live later, at the next unrelated reload.

// ErrDuplicate means an exclusion with the same host, path and rule is stored.
var ErrDuplicate = errors.New("an exclusion for this host, path and rule already exists")

// ErrTooMany means the store holds as many exclusions as the renderer writes.
var ErrTooMany = fmt.Errorf("the limit of %d operator exclusions is reached", appseccfg.MaxOperatorExclusions)

// InvalidError is a ValidateExclusion refusal. Nothing was stored.
type InvalidError struct{ Err error }

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

// ApplyError means a row change was made but applying it to the WAF failed.
//
//   - RolledBack: the row change was undone. When it is false, RollbackErr says
//     why, and the stored rows still include the change.
//   - ReapplyErr: after the undo, writing the file back from the rows failed
//     too. The agent writes the file before it reloads crowdsec, so the file on
//     disk may still hold the change until the next successful apply.
type ApplyError struct {
	Err         error
	RolledBack  bool
	RollbackErr error
	ReapplyErr  error
}

func (e *ApplyError) Error() string {
	switch {
	case !e.RolledBack:
		return fmt.Sprintf("%v; undoing the change also failed: %v", e.Err, e.RollbackErr)
	case e.ReapplyErr != nil:
		return fmt.Sprintf("%v; the change was undone, but rewriting the WAF file failed: %v", e.Err, e.ReapplyErr)
	default:
		return fmt.Sprintf("%v; the change was undone", e.Err)
	}
}

func (e *ApplyError) Unwrap() error { return e.Err }

// ExclusionView is a stored exclusion. ManagedInstallID names the Flarum
// install a row was created for (see flarum.go); it is empty for a row an
// operator added.
type ExclusionView struct {
	models.CRSRuleExclusion
	ManagedInstallID string `json:"managed_install_id,omitempty"`
}

func viewOf(r models.CRSRuleExclusion) ExclusionView {
	return ExclusionView{CRSRuleExclusion: r, ManagedInstallID: managedFlarumInstallID(r)}
}

// applyTimeout bounds one apply. The agent verb allows itself 30s for the
// crowdsec reload and, if that fails, the restart.
const applyTimeout = 45 * time.Second

// revertTimeout bounds the single row write that undoes a change.
const revertTimeout = 10 * time.Second

// ListExclusions returns every stored exclusion, in render order.
func ListExclusions(ctx context.Context, d Deps) ([]ExclusionView, error) {
	if d.Exclusions == nil {
		return nil, ErrNotConfigured
	}
	rows, err := d.Exclusions.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list CRS exclusions: %w", err)
	}
	out := make([]ExclusionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, viewOf(r))
	}
	return out, nil
}

// AddExclusion stores e, normalised, and applies it. It refuses an invalid
// exclusion (*InvalidError), a duplicate (ErrDuplicate) and one past the
// render cap (ErrTooMany) before it stores anything. If the apply fails, the
// row is deleted again and the error is an *ApplyError.
func AddExclusion(ctx context.Context, d Deps, in appseccfg.Exclusion) (ExclusionView, appseccfg.OperatorApplyResult, error) {
	if d.Agent == nil || d.Exclusions == nil || d.HostModes == nil {
		return ExclusionView{}, appseccfg.OperatorApplyResult{}, ErrNotConfigured
	}
	e := appseccfg.NormalizeExclusion(in)
	if err := appseccfg.ValidateExclusion(e); err != nil {
		return ExclusionView{}, appseccfg.OperatorApplyResult{}, &InvalidError{Err: err}
	}
	if err := checkColumnLengths(e); err != nil {
		return ExclusionView{}, appseccfg.OperatorApplyResult{}, &InvalidError{Err: err}
	}

	mu.Lock()
	defer mu.Unlock()

	rows, err := d.Exclusions.List(ctx)
	if err != nil {
		return ExclusionView{}, appseccfg.OperatorApplyResult{}, fmt.Errorf("list CRS exclusions: %w", err)
	}
	if hasExclusion(rows, e) {
		return ExclusionView{}, appseccfg.OperatorApplyResult{}, ErrDuplicate
	}
	if len(rows) >= appseccfg.MaxOperatorExclusions {
		return ExclusionView{}, appseccfg.OperatorApplyResult{}, ErrTooMany
	}

	row := models.CRSRuleExclusion{
		ID: ids.NewULID(), Host: e.Host, URIPrefix: e.URIPrefix, RuleID: e.RuleID, Note: e.Note,
	}
	if err := d.Exclusions.Create(ctx, &row); err != nil {
		return ExclusionView{}, appseccfg.OperatorApplyResult{}, fmt.Errorf("save exclusion: %w", err)
	}
	res, err := applyDetached(ctx, d)
	if err != nil {
		return ExclusionView{}, appseccfg.OperatorApplyResult{}, undo(ctx, d, err, func(c context.Context) error {
			return d.Exclusions.DeleteByID(c, row.ID)
		})
	}
	return viewOf(row), res, nil
}

// checkColumnLengths refuses what ValidateExclusion lets through but the
// crs_rule_exclusions columns cannot hold (rule_id varchar(16), note
// varchar(512)), so the operator gets a refusal instead of a failed insert.
// Host and path lengths are already inside ValidateExclusion's limits.
func checkColumnLengths(e appseccfg.Exclusion) error {
	if len(e.RuleID) > 16 {
		return fmt.Errorf("rule-id %q is too long", e.RuleID)
	}
	if utf8.RuneCountInString(e.Note) > 512 {
		return errors.New("note is longer than 512 characters")
	}
	return nil
}

// RemoveExclusion deletes the exclusion with this id and applies the result.
// An unknown id is repository.ErrNotFound. If the apply fails, the row is
// stored again, with the same id, and the error is an *ApplyError.
func RemoveExclusion(ctx context.Context, d Deps, id string) (appseccfg.OperatorApplyResult, error) {
	if d.Agent == nil || d.Exclusions == nil || d.HostModes == nil {
		return appseccfg.OperatorApplyResult{}, ErrNotConfigured
	}

	mu.Lock()
	defer mu.Unlock()

	rows, err := d.Exclusions.List(ctx)
	if err != nil {
		return appseccfg.OperatorApplyResult{}, fmt.Errorf("list CRS exclusions: %w", err)
	}
	var row *models.CRSRuleExclusion
	for i := range rows {
		if rows[i].ID == id {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return appseccfg.OperatorApplyResult{}, repository.ErrNotFound
	}

	if err := d.Exclusions.DeleteByID(ctx, id); err != nil {
		return appseccfg.OperatorApplyResult{}, fmt.Errorf("delete exclusion: %w", err)
	}
	res, err := applyDetached(ctx, d)
	if err != nil {
		restore := *row
		return appseccfg.OperatorApplyResult{}, undo(ctx, d, err, func(c context.Context) error {
			return d.Exclusions.Create(c, &restore)
		})
	}
	return res, nil
}

// applyDetached runs applyLocked on a context that the client going away does
// not cancel. The agent keeps working on a call the panel abandons, so waiting
// for its answer is what keeps the rows and the file in step.
func applyDetached(ctx context.Context, d Deps) (appseccfg.OperatorApplyResult, error) {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), applyTimeout)
	defer cancel()
	return applyLocked(actx, d)
}

// undo reverses a row change after applyErr, then applies again. The agent
// writes the file before it reloads crowdsec, so a failed reload leaves the new
// file on disk, to go live at the next reload of any kind. The second apply
// writes the file back from the reverted rows.
func undo(ctx context.Context, d Deps, applyErr error, revert func(context.Context) error) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revertTimeout)
	rbErr := revert(rctx)
	cancel()
	if rbErr != nil {
		slog.ErrorContext(ctx, "appsec: undoing an exclusion change after a failed apply failed — the stored rows keep the change",
			"apply_err", applyErr, "err", rbErr)
		return &ApplyError{Err: applyErr, RollbackErr: rbErr}
	}
	if _, err := applyDetached(ctx, d); err != nil {
		slog.WarnContext(ctx, "appsec: rewriting the operator WAF file after an undo failed — it may hold the undone change until the next apply",
			"apply_err", applyErr, "err", err)
		return &ApplyError{Err: applyErr, RolledBack: true, ReapplyErr: err}
	}
	return &ApplyError{Err: applyErr, RolledBack: true}
}
