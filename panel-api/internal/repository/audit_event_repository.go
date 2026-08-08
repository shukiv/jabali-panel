package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// AuditEventRepository covers the append-only audit_events table
// (migration 000137, ADR-0105, M49). Deliberately exposes NO Update or
// Delete of event content — append-only is enforced by the absence of
// a mutation path. The single controlled exception is SetHashes, which
// the chain consumer uses to back-fill the hash columns of a row the
// Redis-down DB fallback inserted with NULLs; it is gated server-side
// with `row_hash IS NULL` so a sealed row can never be rewritten.
//
// ListBySubject is the per-user /me/activity scope: it is the ONLY
// way the user view reads, and the subject filter is applied here
// (server-side), never via a client parameter — the IDOR scar.
type AuditEventRepository interface {
	// Create appends one event. Used by the chain consumer (the
	// authoritative writer) and by the Redis-down DB fallback.
	Create(ctx context.Context, e *models.AuditEvent) error

	FindByID(ctx context.Context, id string) (*models.AuditEvent, error)

	// ListAll is the admin forensics view (every row, raw).
	ListAll(ctx context.Context, opts ListOptions) ([]models.AuditEvent, int64, error)

	// ListBySubject is the per-user view. subjectUserID is the
	// session identity, resolved server-side by the handler — NEVER a
	// client filter. Rows with a NULL subject_user_id are structurally
	// excluded (safe-fail), so server-internal events never leak into
	// a user's activity feed.
	ListBySubject(ctx context.Context, subjectUserID string, opts ListOptions) ([]models.AuditEvent, int64, error)

	// ListByActorOrSubject is the per-user "Account Activity" scope:
	// rows the user ACTED (actor_user_id) OR rows about their account
	// (subject_user_id). The generic recorder only subject-tags
	// /api/v1/me/* routes, so a subject-only filter showed the user an
	// empty feed even though they had clearly acted — this also matches
	// the original intent ("a unified log so he can see all his last
	// actions"). userID is the session identity, resolved server-side
	// by the handler — NEVER a client filter (the IDOR scar). A blank
	// userID matches nothing: actor/subject are NULL or a ULID, never
	// the empty string.
	ListByActorOrSubject(ctx context.Context, userID string, opts ListOptions) ([]models.AuditEvent, int64, error)

	// LatestRowHash returns the row_hash of the most recent sealed
	// (chained) row, or "" when the chain has no sealed row yet
	// (genesis). The chain consumer feeds this in as the next row's
	// prev_hash.
	LatestRowHash(ctx context.Context) (string, error)

	// SetHashes back-fills prev_hash/row_hash for a fallback row that
	// was inserted with NULL hashes. Gated with `row_hash IS NULL`:
	// returns ErrNotFound (effectively "already sealed / not found")
	// rather than ever overwriting a sealed row. Consumer-only.
	SetHashes(ctx context.Context, id, prevHash, rowHash string) error

	// ListUnsealed returns rows with a NULL row_hash (inserted by the
	// recorder's Redis-down DB fallback), oldest-first and capped at
	// limit — the chain consumer's back-fill work queue. Read-only;
	// append-only-safe.
	ListUnsealed(ctx context.Context, limit int) ([]models.AuditEvent, error)

	// AllForVerify returns every row in total chain order (ts ASC,
	// id ASC) for `jabali audit verify`. v1 is unbounded — fine at
	// panel scale; a very large log would need chunked streaming
	// verification (future, tracked in ADR-0106 §risks).
	AllForVerify(ctx context.Context) ([]models.AuditEvent, error)
	// EachForVerify streams the chain in batches (see implementation).
	EachForVerify(ctx context.Context, batchSize int, fn func([]models.AuditEvent) bool) error

	// PruneOlderThan deletes rows with ts < cutoff and returns the
	// count. ADR-0106's eventual target is a whole-partition DROP
	// (audit_events isn't partitioned in 000138); this bounded DELETE
	// is the honest v1. The prune itself is recorded as an audit
	// event by the caller (never a silent selective delete).
	PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error)

	// PruneOlderThanWithAnchor is the JAB-105 chain-safe prune: it captures the
	// row_hash of the newest SEALED row being deleted, deletes rows with
	// ts < cutoff, and persists that hash as server_settings.audit_chain_anchor
	// (all in one transaction) so `audit verify` can resume the chain from the
	// anchor instead of genesis. Returns the deleted count.
	PruneOlderThanWithAnchor(ctx context.Context, cutoff time.Time) (int64, error)

	// ChainAnchor returns server_settings.audit_chain_anchor ("" when never
	// pruned) — the prev_hash VerifyChain should start from.
	ChainAnchor(ctx context.Context) (string, error)
}

// Column allowlists for the audit_events list views. Empty-key-proof
// (see ListCols doc): Sort is whitelist-matched, so Sort/Search names
// can't be an injection vector.
var auditEventListCols = ListCols{
	Search:      []string{"action", "target_id", "actor_kind", "result"},
	Sort:        []string{"ts", "action", "actor_kind", "result", "actor_user_id", "subject_user_id"},
	DefaultSort: "ts",
}

type auditEventRepo struct{ db *gorm.DB }

func NewAuditEventRepository(db *gorm.DB) AuditEventRepository {
	return &auditEventRepo{db: db}
}

func (r *auditEventRepo) Create(ctx context.Context, e *models.AuditEvent) error {
	if err := r.db.WithContext(ctx).Create(e).Error; err != nil {
		return err
	}
	return nil
}

func (r *auditEventRepo) FindByID(ctx context.Context, id string) (*models.AuditEvent, error) {
	var e models.AuditEvent
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&e).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &e, nil
}

func (r *auditEventRepo) ListAll(ctx context.Context, opts ListOptions) ([]models.AuditEvent, int64, error) {
	var (
		rows  []models.AuditEvent
		total int64
	)
	base := r.db.WithContext(ctx).Model(&models.AuditEvent{})

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, auditEventListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, auditEventListCols)
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *auditEventRepo) ListBySubject(ctx context.Context, subjectUserID string, opts ListOptions) ([]models.AuditEvent, int64, error) {
	var (
		rows  []models.AuditEvent
		total int64
	)
	// Subject scope applied HERE, server-side, from the session
	// identity — never a client-supplied filter (IDOR scar). A blank
	// subjectUserID would match no rows (safe-fail) rather than
	// returning everything.
	base := r.db.WithContext(ctx).Model(&models.AuditEvent{}).Where("subject_user_id = ?", subjectUserID)

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, auditEventListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, auditEventListCols)
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *auditEventRepo) ListByActorOrSubject(ctx context.Context, userID string, opts ListOptions) ([]models.AuditEvent, int64, error) {
	var (
		rows  []models.AuditEvent
		total int64
	)
	// Scope applied HERE, server-side, from the session identity —
	// never a client-supplied filter (IDOR scar). actor OR subject so
	// the user sees actions they performed AND actions taken on their
	// account. A blank userID matches nothing (actor/subject are NULL
	// or a ULID, never "").
	base := r.db.WithContext(ctx).Model(&models.AuditEvent{}).
		Where("actor_user_id = ? OR subject_user_id = ?", userID, userID)

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, auditEventListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, auditEventListCols)
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *auditEventRepo) LatestRowHash(ctx context.Context) (string, error) {
	var e models.AuditEvent
	err := r.db.WithContext(ctx).
		Where("row_hash IS NOT NULL").
		Order("ts DESC, id DESC").
		Limit(1).
		First(&e).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil // genesis: no sealed row yet
		}
		return "", err
	}
	if e.RowHash == nil {
		return "", nil
	}
	return *e.RowHash, nil
}

func (r *auditEventRepo) SetHashes(ctx context.Context, id, prevHash, rowHash string) error {
	res := r.db.WithContext(ctx).
		Model(&models.AuditEvent{}).
		Where("id = ? AND row_hash IS NULL", id).
		Updates(map[string]any{"prev_hash": prevHash, "row_hash": rowHash})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Either no such id, or it is already sealed — both mean
		// "nothing to back-fill"; never overwrite a sealed row.
		return ErrNotFound
	}
	return nil
}

func (r *auditEventRepo) ListUnsealed(ctx context.Context, limit int) ([]models.AuditEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []models.AuditEvent
	err := r.db.WithContext(ctx).
		Where("row_hash IS NULL").
		Order("ts ASC, id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// EachForVerify walks the whole chain in id/ts order, handing the caller one
// batch at a time. fn returns false to stop early (a broken row is found).
//
// AllForVerify below loads the entire append-only table into memory; on a busy
// box that is hundreds of thousands of rows and O(table) memory for a single
// admin request. The chain state is scalar, so verification does not need the
// rows all at once.
func (r *auditEventRepo) EachForVerify(ctx context.Context, batchSize int, fn func([]models.AuditEvent) bool) error {
	if batchSize <= 0 {
		batchSize = 1000
	}
	var batch []models.AuditEvent
	return r.db.WithContext(ctx).
		Order("ts ASC, id ASC").
		FindInBatches(&batch, batchSize, func(tx *gorm.DB, _ int) error {
			if !fn(batch) {
				// Returning an error is how FindInBatches stops; translate it
				// back to "clean stop" in the caller.
				return errVerifyStop
			}
			return nil
		}).Error
}

// errVerifyStop signals an intentional early exit from EachForVerify.
var errVerifyStop = errors.New("verify: stop")

// IsVerifyStop reports whether an EachForVerify error is the intentional
// early-exit sentinel rather than a real failure.
func IsVerifyStop(err error) bool { return errors.Is(err, errVerifyStop) }

func (r *auditEventRepo) AllForVerify(ctx context.Context) ([]models.AuditEvent, error) {
	var rows []models.AuditEvent
	err := r.db.WithContext(ctx).
		Order("ts ASC, id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *auditEventRepo) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("ts < ?", cutoff).
		Delete(&models.AuditEvent{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (r *auditEventRepo) PruneOlderThanWithAnchor(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Anchor = row_hash of the newest sealed row in the to-be-deleted tail.
		// That hash is exactly the prev_hash of the first sealed survivor, so
		// verify can resume from it. If the deleted range has no sealed row,
		// leave the anchor unchanged.
		var newest models.AuditEvent
		e := tx.Where("ts < ? AND row_hash IS NOT NULL", cutoff).
			Order("ts DESC, id DESC").Limit(1).First(&newest).Error
		anchor := ""
		if e == nil && newest.RowHash != nil {
			anchor = *newest.RowHash
		} else if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		res := tx.Where("ts < ?", cutoff).Delete(&models.AuditEvent{})
		if res.Error != nil {
			return res.Error
		}
		n = res.RowsAffected
		if anchor != "" {
			if e := tx.Table("server_settings").Where("id = ?", 1).
				Update("audit_chain_anchor", anchor).Error; e != nil {
				return e
			}
		}
		return nil
	})
	return n, err
}

func (r *auditEventRepo) ChainAnchor(ctx context.Context) (string, error) {
	var row struct{ AuditChainAnchor *string }
	err := r.db.WithContext(ctx).Table("server_settings").
		Select("audit_chain_anchor").Where("id = ?", 1).Scan(&row).Error
	if err != nil {
		return "", err
	}
	if row.AuditChainAnchor == nil {
		return "", nil
	}
	return *row.AuditChainAnchor, nil
}
