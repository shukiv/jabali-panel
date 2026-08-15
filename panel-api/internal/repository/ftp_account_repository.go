package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// FtpAccountRepository defines data access for tenant FTP/SFTP subaccounts
// (GH #1053). The panel row is the ONLY handle to the underlying system
// user — callers that delete rows must run the agent-side teardown first;
// this layer never cascades.
type FtpAccountRepository interface {
	Create(ctx context.Context, acct *models.FtpAccount) error
	FindByID(ctx context.Context, id string) (*models.FtpAccount, error)
	FindByIDAndUserID(ctx context.Context, id, userID string) (*models.FtpAccount, error)
	FindByUsername(ctx context.Context, username string) (*models.FtpAccount, error)
	ListByUserID(ctx context.Context, userID string) ([]models.FtpAccount, error)
	List(ctx context.Context) ([]models.FtpAccount, error)
	// Update persists the mutable fields only, via an explicit Select
	// allowlist — a full-struct save would silently write zero values into
	// columns the caller never touched.
	Update(ctx context.Context, acct *models.FtpAccount) error
	Delete(ctx context.Context, id string) error
	CountByUserID(ctx context.Context, userID string) (int64, error)
}

type ftpAccountRepo struct{ db *gorm.DB }

func NewFtpAccountRepository(db *gorm.DB) FtpAccountRepository {
	return &ftpAccountRepo{db: db}
}

func (r *ftpAccountRepo) Create(ctx context.Context, acct *models.FtpAccount) error {
	if err := r.db.WithContext(ctx).Create(acct).Error; err != nil {
		return translateFtpAccount(err)
	}
	return nil
}

func (r *ftpAccountRepo) FindByID(ctx context.Context, id string) (*models.FtpAccount, error) {
	var acct models.FtpAccount
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&acct).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &acct, nil
}

func (r *ftpAccountRepo) FindByIDAndUserID(ctx context.Context, id, userID string) (*models.FtpAccount, error) {
	var acct models.FtpAccount
	if err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&acct).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &acct, nil
}

func (r *ftpAccountRepo) FindByUsername(ctx context.Context, username string) (*models.FtpAccount, error) {
	var acct models.FtpAccount
	if err := r.db.WithContext(ctx).Where("username = ?", username).First(&acct).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &acct, nil
}

func (r *ftpAccountRepo) ListByUserID(ctx context.Context, userID string) ([]models.FtpAccount, error) {
	var accts []models.FtpAccount
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Find(&accts).Error; err != nil {
		return nil, err
	}
	return accts, nil
}

func (r *ftpAccountRepo) List(ctx context.Context) ([]models.FtpAccount, error) {
	var accts []models.FtpAccount
	if err := r.db.WithContext(ctx).Order("created_at DESC").Find(&accts).Error; err != nil {
		return nil, err
	}
	return accts, nil
}

func (r *ftpAccountRepo) Update(ctx context.Context, acct *models.FtpAccount) error {
	// Immutable on purpose: id, user_id, username (renaming a system user
	// is a delete+create at the agent layer), created_at.
	err := r.db.WithContext(ctx).
		Model(&models.FtpAccount{}).
		Where("id = ?", acct.ID).
		Select("home_path", "ftp_access", "sftp_access", "is_enabled", "updated_at").
		Updates(map[string]any{
			"home_path":   acct.HomePath,
			"ftp_access":  acct.FTPAccess,
			"sftp_access": acct.SFTPAccess,
			"is_enabled":  acct.IsEnabled,
			"updated_at":  acct.UpdatedAt,
		}).Error
	return translateFtpAccount(err)
}

func (r *ftpAccountRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.FtpAccount{}).Error
}

func (r *ftpAccountRepo) CountByUserID(ctx context.Context, userID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.FtpAccount{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// translateFtpAccount maps GORM/driver errors to repository conventions.
func translateFtpAccount(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	var my *mysql.MySQLError
	if errors.As(err, &my) && my.Number == 1062 {
		// 1062 = ER_DUP_ENTRY (unique username)
		return ErrConflict
	}
	if strings.Contains(err.Error(), "Duplicate entry") {
		return ErrConflict
	}
	return err
}
