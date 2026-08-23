package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// LogAccessStreamRepository defines data access for log access streams.
type LogAccessStreamRepository interface {
	Create(ctx context.Context, stream *models.LogAccessStream) error
	// ReserveWithinCap atomically enforces the per-user active-stream cap while
	// inserting, so concurrent creates cannot both pass a check-then-create gap
	// and exceed the cap (JAB-347). Returns ErrLogStreamCapExceeded at the cap.
	ReserveWithinCap(ctx context.Context, stream *models.LogAccessStream, max int) error
	FindByStreamKey(ctx context.Context, streamKey string) (*models.LogAccessStream, error)
	DeleteByID(ctx context.Context, id string) error
	CleanupExpired(ctx context.Context) (int64, error)
}

type logAccessStreamRepo struct{ db *gorm.DB }

func NewLogAccessStreamRepository(db *gorm.DB) LogAccessStreamRepository {
	return &logAccessStreamRepo{db: db}
}

func (r *logAccessStreamRepo) Create(ctx context.Context, stream *models.LogAccessStream) error {
	if err := r.db.WithContext(ctx).Create(stream).Error; err != nil {
		return translate(err)
	}
	return nil
}

func (r *logAccessStreamRepo) FindByStreamKey(ctx context.Context, streamKey string) (*models.LogAccessStream, error) {
	var stream models.LogAccessStream
	if err := r.db.WithContext(ctx).Where("stream_key = ? AND expires_at > ?", streamKey, time.Now()).First(&stream).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &stream, nil
}

func (r *logAccessStreamRepo) DeleteByID(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Delete(&models.LogAccessStream{}, "id = ?", id)
	if err := result.Error; err != nil {
		return err
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *logAccessStreamRepo) CleanupExpired(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).Delete(&models.LogAccessStream{}, "expires_at <= ?", time.Now())
	if err := result.Error; err != nil {
		return 0, err
	}
	return result.RowsAffected, nil
}

// ReserveWithinCap runs the count-then-insert as one transaction gated by a
// FOR UPDATE lock on the beneficiary's users row — a stable per-user
// serialization point that exists even at zero streams (a COUNT ... FOR UPDATE
// would lock nothing there and let two first-creates both pass). The row lock
// releases on commit/rollback (no GET_LOCK connection-pool leak) and the section
// holds no external call, so it is brief. Mirrors FtpAccountRepository's JAB-262
// cap fix. Only ACTIVE (non-expired) streams count toward the cap.
func (r *logAccessStreamRepo) ReserveWithinCap(ctx context.Context, stream *models.LogAccessStream, max int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedID string
		if err := tx.Raw("SELECT id FROM users WHERE id = ? FOR UPDATE", stream.UserID).Scan(&lockedID).Error; err != nil {
			return err
		}
		if lockedID == "" {
			return ErrNotFound
		}
		var count int64
		if err := tx.Model(&models.LogAccessStream{}).
			Where("user_id = ? AND expires_at > ?", stream.UserID, time.Now()).
			Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(max) {
			return ErrLogStreamCapExceeded
		}
		if err := tx.Create(stream).Error; err != nil {
			return translate(err)
		}
		return nil
	})
}
