package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DomainOwnershipRepository writes the GH #1816 / ADR-0170 ownership-proof
// state of domains and web aliases, and reads the policy switch. It is the
// only writer of the ownership_* columns: the domain repository's generic
// Update is a Select allow-list that never names them.
//
// Every state change is a conditional UPDATE, so two writers cannot undo each
// other. A check result lands only while the row is still pending with the
// same token, so a ticker that raced an admin revoke (which rolls a new
// token) or an approve changes nothing. The bool results report whether the
// row changed.
type DomainOwnershipRepository interface {
	// MarkDomainVerified sets a pending domain verified by method. A non-empty
	// expectToken also requires the row to still hold that token (the TXT
	// proof path); an admin approval passes "".
	MarkDomainVerified(ctx context.Context, id, method, expectToken string, at time.Time) (bool, error)
	// MarkDomainPending puts a domain back to pending with a fresh token
	// (admin revoke, a rename to an uncovered name, a parent cascade). The
	// next check is due at once. fromVerified limits the change to a row that
	// is verified now (a revoke of an already-pending row changes nothing).
	// clearVerifiedAt marks the name as never verified, which makes it expire
	// like a new claim (a rename: the new name was never proven).
	MarkDomainPending(ctx context.Context, id, token string, at time.Time, fromVerified, clearVerifiedAt bool) (bool, error)
	// RecordDomainCheck stores a check result that did not verify the domain.
	RecordDomainCheck(ctx context.Context, id, expectToken, result string, checkedAt, nextCheckAt time.Time) (bool, error)
	// EnsureDomainToken stores token only when the row has none (a row an
	// older binary inserted after migration 000311 carries an empty token).
	EnsureDomainToken(ctx context.Context, id, token string) (bool, error)
	// MarkDomainExpiryNotified records that the expiry notice was sent.
	MarkDomainExpiryNotified(ctx context.Context, id string, at time.Time) error
	// ListDomainsDue returns pending domains whose next check is due, oldest
	// first, capped at limit.
	ListDomainsDue(ctx context.Context, now time.Time, limit int) ([]models.Domain, error)
	// ListPendingDomains returns every pending domain (the expiry sweep and
	// the admin pending list read it; the set stays small).
	ListPendingDomains(ctx context.Context) ([]models.Domain, error)
	// ListParentProvenUnder returns the verified domains whose proof is the
	// method 'parent' and whose name is a strict subdomain of name. A revoke
	// of name sends them back to pending (ADR-0170 section 5).
	ListParentProvenUnder(ctx context.Context, name string) ([]models.Domain, error)
	// DeleteExpiredDomain deletes a domain only while it is still an expired
	// claim: pending, never verified, pending since cutoff or earlier, and
	// neither the panel's own row nor a docker-app domain. A row that no
	// longer qualifies (a verification won the race with the expiry sweep)
	// is kept and ErrOwnershipChanged is returned.
	DeleteExpiredDomain(ctx context.Context, id string, cutoff time.Time) error

	// The alias equivalents of the domain methods above.
	MarkAliasVerified(ctx context.Context, id, method, expectToken string, at time.Time) (bool, error)
	MarkAliasPending(ctx context.Context, id, token string, at time.Time, fromVerified, clearVerifiedAt bool) (bool, error)
	RecordAliasCheck(ctx context.Context, id, expectToken, result string, checkedAt, nextCheckAt time.Time) (bool, error)
	EnsureAliasToken(ctx context.Context, id, token string) (bool, error)
	MarkAliasExpiryNotified(ctx context.Context, id string, at time.Time) error
	ListAliasesDue(ctx context.Context, now time.Time, limit int) ([]models.WebDomainAlias, error)
	ListPendingAliases(ctx context.Context) ([]models.WebDomainAlias, error)
	ListParentProvenAliasesUnder(ctx context.Context, name string) ([]models.WebDomainAlias, error)
	DeleteExpiredAlias(ctx context.Context, id string, cutoff time.Time) error
	// ListAliasesByDomain returns every alias of one domain (a parent
	// cascade re-checks whether they are still covered).
	ListAliasesByDomain(ctx context.Context, domainID string) ([]models.WebDomainAlias, error)

	// GetSettings returns the policy row. A missing row reads as
	// RequireProof=true: the requirement fails closed.
	GetSettings(ctx context.Context) (models.DomainOwnershipSettings, error)
	// SetRequireProof writes the policy switch (upsert of the id=1 row).
	SetRequireProof(ctx context.Context, require bool, updatedBy string, at time.Time) error
}

// ErrOwnershipChanged means a conditional ownership write found the row in a
// different state than the caller expected, so nothing was changed.
var ErrOwnershipChanged = errors.New("repository: ownership state changed")

type domainOwnershipRepo struct{ db *gorm.DB }

// NewDomainOwnershipRepository returns the gorm-backed ownership repository.
func NewDomainOwnershipRepository(db *gorm.DB) DomainOwnershipRepository {
	// A nil db yields a nil interface, never a repository that panics on
	// first use: every reader treats a nil store as "proof required".
	if db == nil {
		return nil
	}
	return &domainOwnershipRepo{db: db}
}

// ownershipTable names the two tables that carry ownership state. The value
// is a fixed constant, never input.
type ownershipTable string

const (
	ownershipDomains ownershipTable = "domains"
	ownershipAliases ownershipTable = "web_domain_aliases"
)

func (r *domainOwnershipRepo) markVerified(ctx context.Context, t ownershipTable, id, method, expectToken string, at time.Time) (bool, error) {
	q := r.db.WithContext(ctx).Table(string(t)).
		Where("id = ? AND ownership_status = ?", id, models.OwnershipPending)
	if expectToken != "" {
		q = q.Where("ownership_token = ?", expectToken)
	}
	res := q.Updates(map[string]any{
		"ownership_status":             models.OwnershipVerified,
		"ownership_method":             method,
		"ownership_verified_at":        at,
		"ownership_checked_at":         at,
		"ownership_next_check_at":      nil,
		"ownership_last_result":        models.OwnershipResultVerified,
		"ownership_expiry_notified_at": nil,
		"updated_at":                   at,
	})
	if res.Error != nil {
		return false, translate(res.Error)
	}
	return res.RowsAffected > 0, nil
}

func (r *domainOwnershipRepo) markPending(ctx context.Context, t ownershipTable, id, token string, at time.Time, fromVerified, clearVerifiedAt bool) (bool, error) {
	if token == "" {
		return false, errors.New("repository: ownership token is required")
	}
	q := r.db.WithContext(ctx).Table(string(t)).Where("id = ?", id)
	if fromVerified {
		q = q.Where("ownership_status = ?", models.OwnershipVerified)
	}
	upd := map[string]any{
		"ownership_status":             models.OwnershipPending,
		"ownership_method":             "",
		"ownership_token":              token,
		"ownership_pending_since":      at,
		"ownership_checked_at":         nil,
		"ownership_next_check_at":      at,
		"ownership_last_result":        "",
		"ownership_expiry_notified_at": nil,
		"updated_at":                   at,
	}
	if clearVerifiedAt {
		upd["ownership_verified_at"] = nil
	}
	res := q.Updates(upd)
	if res.Error != nil {
		return false, translate(res.Error)
	}
	return res.RowsAffected > 0, nil
}

func (r *domainOwnershipRepo) recordCheck(ctx context.Context, t ownershipTable, id, expectToken, result string, checkedAt, next time.Time) (bool, error) {
	res := r.db.WithContext(ctx).Table(string(t)).
		Where("id = ? AND ownership_status = ? AND ownership_token = ?", id, models.OwnershipPending, expectToken).
		Updates(map[string]any{
			"ownership_checked_at":    checkedAt,
			"ownership_next_check_at": next,
			"ownership_last_result":   result,
		})
	if res.Error != nil {
		return false, translate(res.Error)
	}
	return res.RowsAffected > 0, nil
}

func (r *domainOwnershipRepo) ensureToken(ctx context.Context, t ownershipTable, id, token string) (bool, error) {
	if token == "" {
		return false, errors.New("repository: ownership token is required")
	}
	res := r.db.WithContext(ctx).Table(string(t)).
		Where("id = ? AND ownership_token = ?", id, "").
		Updates(map[string]any{"ownership_token": token})
	if res.Error != nil {
		return false, translate(res.Error)
	}
	return res.RowsAffected > 0, nil
}

func (r *domainOwnershipRepo) markExpiryNotified(ctx context.Context, t ownershipTable, id string, at time.Time) error {
	return translate(r.db.WithContext(ctx).Table(string(t)).
		Where("id = ?", id).
		Updates(map[string]any{"ownership_expiry_notified_at": at}).Error)
}

func (r *domainOwnershipRepo) dueQuery(ctx context.Context, t ownershipTable, now time.Time, limit int) *gorm.DB {
	return r.db.WithContext(ctx).Table(string(t)).
		Where("ownership_status = ? AND (ownership_next_check_at IS NULL OR ownership_next_check_at <= ?)", models.OwnershipPending, now).
		Order("ownership_next_check_at IS NOT NULL, ownership_next_check_at ASC").
		Limit(limit)
}

func (r *domainOwnershipRepo) MarkDomainVerified(ctx context.Context, id, method, expectToken string, at time.Time) (bool, error) {
	return r.markVerified(ctx, ownershipDomains, id, method, expectToken, at)
}

func (r *domainOwnershipRepo) MarkDomainPending(ctx context.Context, id, token string, at time.Time, fromVerified, clearVerifiedAt bool) (bool, error) {
	return r.markPending(ctx, ownershipDomains, id, token, at, fromVerified, clearVerifiedAt)
}

func (r *domainOwnershipRepo) RecordDomainCheck(ctx context.Context, id, expectToken, result string, checkedAt, nextCheckAt time.Time) (bool, error) {
	return r.recordCheck(ctx, ownershipDomains, id, expectToken, result, checkedAt, nextCheckAt)
}

func (r *domainOwnershipRepo) EnsureDomainToken(ctx context.Context, id, token string) (bool, error) {
	return r.ensureToken(ctx, ownershipDomains, id, token)
}

func (r *domainOwnershipRepo) MarkDomainExpiryNotified(ctx context.Context, id string, at time.Time) error {
	return r.markExpiryNotified(ctx, ownershipDomains, id, at)
}

func (r *domainOwnershipRepo) ListDomainsDue(ctx context.Context, now time.Time, limit int) ([]models.Domain, error) {
	var rows []models.Domain
	if err := r.dueQuery(ctx, ownershipDomains, now, limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *domainOwnershipRepo) ListPendingDomains(ctx context.Context) ([]models.Domain, error) {
	var rows []models.Domain
	if err := r.db.WithContext(ctx).
		Where("ownership_status <> ?", models.OwnershipVerified).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *domainOwnershipRepo) ListParentProvenUnder(ctx context.Context, name string) ([]models.Domain, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil, nil
	}
	var rows []models.Domain
	// name is a validated FQDN (letters, digits, dots, hyphens), so it holds
	// no LIKE metacharacters; the suffix is re-checked below regardless.
	if err := r.db.WithContext(ctx).
		Where("ownership_status = ? AND ownership_method = ? AND name LIKE ?",
			models.OwnershipVerified, models.OwnershipMethodParent, "%."+name).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, d := range rows {
		if strings.HasSuffix(strings.ToLower(d.Name), "."+name) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *domainOwnershipRepo) DeleteExpiredDomain(ctx context.Context, id string, cutoff time.Time) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND ownership_status = ? AND ownership_verified_at IS NULL AND ownership_pending_since <= ? AND is_panel_primary = ? AND managed_by <> ?",
			id, models.OwnershipPending, cutoff, false, models.DomainManagedByDockerApp).
		Delete(&models.Domain{})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrOwnershipChanged
	}
	return nil
}

func (r *domainOwnershipRepo) MarkAliasVerified(ctx context.Context, id, method, expectToken string, at time.Time) (bool, error) {
	return r.markVerified(ctx, ownershipAliases, id, method, expectToken, at)
}

func (r *domainOwnershipRepo) MarkAliasPending(ctx context.Context, id, token string, at time.Time, fromVerified, clearVerifiedAt bool) (bool, error) {
	return r.markPending(ctx, ownershipAliases, id, token, at, fromVerified, clearVerifiedAt)
}

func (r *domainOwnershipRepo) RecordAliasCheck(ctx context.Context, id, expectToken, result string, checkedAt, nextCheckAt time.Time) (bool, error) {
	return r.recordCheck(ctx, ownershipAliases, id, expectToken, result, checkedAt, nextCheckAt)
}

func (r *domainOwnershipRepo) EnsureAliasToken(ctx context.Context, id, token string) (bool, error) {
	return r.ensureToken(ctx, ownershipAliases, id, token)
}

func (r *domainOwnershipRepo) MarkAliasExpiryNotified(ctx context.Context, id string, at time.Time) error {
	return r.markExpiryNotified(ctx, ownershipAliases, id, at)
}

func (r *domainOwnershipRepo) ListAliasesDue(ctx context.Context, now time.Time, limit int) ([]models.WebDomainAlias, error) {
	var rows []models.WebDomainAlias
	if err := r.dueQuery(ctx, ownershipAliases, now, limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *domainOwnershipRepo) ListPendingAliases(ctx context.Context) ([]models.WebDomainAlias, error) {
	var rows []models.WebDomainAlias
	if err := r.db.WithContext(ctx).
		Where("ownership_status <> ?", models.OwnershipVerified).
		Order("hostname ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *domainOwnershipRepo) ListParentProvenAliasesUnder(ctx context.Context, name string) ([]models.WebDomainAlias, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil, nil
	}
	var rows []models.WebDomainAlias
	// See ListParentProvenUnder: name is a validated FQDN, and the suffix is
	// re-checked on the label boundary below.
	if err := r.db.WithContext(ctx).
		Where("ownership_status = ? AND ownership_method = ? AND hostname LIKE ?",
			models.OwnershipVerified, models.OwnershipMethodParent, "%."+name).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, a := range rows {
		if strings.HasSuffix(strings.ToLower(a.Hostname), "."+name) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *domainOwnershipRepo) DeleteExpiredAlias(ctx context.Context, id string, cutoff time.Time) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND ownership_status = ? AND ownership_verified_at IS NULL AND ownership_pending_since <= ?",
			id, models.OwnershipPending, cutoff).
		Delete(&models.WebDomainAlias{})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrOwnershipChanged
	}
	return nil
}

func (r *domainOwnershipRepo) ListAliasesByDomain(ctx context.Context, domainID string) ([]models.WebDomainAlias, error) {
	var rows []models.WebDomainAlias
	if err := r.db.WithContext(ctx).
		Where("domain_id = ?", domainID).
		Order("hostname ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *domainOwnershipRepo) GetSettings(ctx context.Context) (models.DomainOwnershipSettings, error) {
	var s models.DomainOwnershipSettings
	err := r.db.WithContext(ctx).Where("id = ?", 1).Take(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.DomainOwnershipSettings{ID: 1, RequireProof: true}, nil
	}
	if err != nil {
		return models.DomainOwnershipSettings{}, err
	}
	return s, nil
}

func (r *domainOwnershipRepo) SetRequireProof(ctx context.Context, require bool, updatedBy string, at time.Time) error {
	return r.db.WithContext(ctx).Exec(
		"INSERT INTO domain_ownership_settings (id, require_proof, updated_by, updated_at) VALUES (1, ?, ?, ?) "+
			"ON DUPLICATE KEY UPDATE require_proof = VALUES(require_proof), updated_by = VALUES(updated_by), updated_at = VALUES(updated_at)",
		require, updatedBy, at).Error
}
