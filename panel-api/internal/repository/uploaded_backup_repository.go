package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ErrUploadedBackupBusy is returned when an uploaded backup is being restored,
// so it can't be restored again or deleted until that restore finishes.
var ErrUploadedBackupBusy = errors.New("repository: uploaded backup is being restored")

// UploadedBackupRepository persists the account backups admins uploaded from
// another server (GH #1993).
//
// A restore claims its row (status restoring) so two restores of the same
// archive never run at once and the archive isn't deleted under one. A
// restoring row whose restore started before staleBefore counts as idle: the
// panel restarted mid-restore and nothing will finish it.
type UploadedBackupRepository interface {
	Create(ctx context.Context, b *models.UploadedBackup) error
	FindByID(ctx context.Context, id string) (*models.UploadedBackup, error)
	List(ctx context.Context, opts ListOptions) ([]models.UploadedBackup, int64, error)
	// ListAll returns every row (the sweeper matches files to rows).
	ListAll(ctx context.Context) ([]models.UploadedBackup, error)
	// ListExpired returns the rows whose expires_at is before now.
	ListExpired(ctx context.Context, now time.Time) ([]models.UploadedBackup, error)
	// ClaimRestore marks the row restoring into target. ErrUploadedBackupBusy
	// when another restore holds it, ErrNotFound when there is no such row.
	ClaimRestore(ctx context.Context, id, target string, now, staleBefore time.Time) error
	// FinishRestore records the claimed restore's outcome; a non-nil expiresAt
	// also sets when the sweeper removes the row.
	FinishRestore(ctx context.Context, id, status, result string, now time.Time, expiresAt *time.Time) error
	// DeleteIfIdle deletes the row unless a restore holds it
	// (ErrUploadedBackupBusy). ErrNotFound when there is no such row.
	DeleteIfIdle(ctx context.Context, id string, staleBefore time.Time) error
}

type uploadedBackupRepo struct{ db *gorm.DB }

func NewUploadedBackupRepository(db *gorm.DB) UploadedBackupRepository {
	return &uploadedBackupRepo{db: db}
}

var uploadedBackupListCols = ListCols{
	Search:      []string{"account_username", "file_name"},
	Sort:        []string{"created_at", "size_bytes", "account_username"},
	DefaultSort: "created_at",
}

// uploadedBackupIdleClause matches a row no restore holds.
const uploadedBackupIdleClause = "(restore_status <> ? OR restore_started_at IS NULL OR restore_started_at < ?)"

func (r *uploadedBackupRepo) Create(ctx context.Context, b *models.UploadedBackup) error {
	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	return r.db.WithContext(ctx).Create(b).Error
}

func (r *uploadedBackupRepo) FindByID(ctx context.Context, id string) (*models.UploadedBackup, error) {
	var b models.UploadedBackup
	err := r.db.WithContext(ctx).First(&b, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *uploadedBackupRepo) List(ctx context.Context, opts ListOptions) ([]models.UploadedBackup, int64, error) {
	var (
		rows  []models.UploadedBackup
		total int64
	)
	base := r.db.WithContext(ctx).Model(&models.UploadedBackup{})
	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, uploadedBackupListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "desc"
	}
	q := applyListOptions(base.Session(&gorm.Session{}), opts, uploadedBackupListCols)
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *uploadedBackupRepo) ListAll(ctx context.Context) ([]models.UploadedBackup, error) {
	var rows []models.UploadedBackup
	if err := r.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *uploadedBackupRepo) ListExpired(ctx context.Context, now time.Time) ([]models.UploadedBackup, error) {
	var rows []models.UploadedBackup
	err := r.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at < ?", now).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *uploadedBackupRepo) ClaimRestore(ctx context.Context, id, target string, now, staleBefore time.Time) error {
	res := r.db.WithContext(ctx).Model(&models.UploadedBackup{}).
		Where("id = ? AND "+uploadedBackupIdleClause, id, models.UploadedBackupRestoring, staleBefore).
		Updates(map[string]any{
			"restore_status":     models.UploadedBackupRestoring,
			"restore_started_at": now,
			"restore_target":     target,
			"restore_result":     nil,
			"updated_at":         now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return r.busyOrMissing(ctx, id)
	}
	return nil
}

func (r *uploadedBackupRepo) FinishRestore(ctx context.Context, id, status, result string, now time.Time, expiresAt *time.Time) error {
	set := map[string]any{
		"restore_status": status,
		"restored_at":    now,
		"restore_result": result,
		"updated_at":     now,
	}
	if expiresAt != nil {
		set["expires_at"] = *expiresAt
	}
	res := r.db.WithContext(ctx).Model(&models.UploadedBackup{}).Where("id = ?", id).Updates(set)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *uploadedBackupRepo) DeleteIfIdle(ctx context.Context, id string, staleBefore time.Time) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND "+uploadedBackupIdleClause, id, models.UploadedBackupRestoring, staleBefore).
		Delete(&models.UploadedBackup{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return r.busyOrMissing(ctx, id)
	}
	return nil
}

// busyOrMissing tells a held row from a missing one after a conditional write
// matched nothing.
func (r *uploadedBackupRepo) busyOrMissing(ctx context.Context, id string) error {
	if _, err := r.FindByID(ctx, id); err != nil {
		return err
	}
	return ErrUploadedBackupBusy
}
