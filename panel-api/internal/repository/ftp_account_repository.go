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
	// ReserveWithinCap atomically enforces the per-tenant account cap and the
	// isolated quota-split, then INSERTs acct — all under a per-tenant row lock
	// so concurrent creates can never exceed the cap (JAB-262). Returns
	// ErrFtpCapExceeded / ErrFtpQuotaSplitExceeded / ErrConflict.
	ReserveWithinCap(ctx context.Context, acct *models.FtpAccount, maxAccounts int, pkgDiskQuotaMB uint32) error
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
	// AllocateUID hands out the next isolated-subaccount uid from the monotonic
	// never-reuse allocator (GH #1145). The value only ever increases, so a
	// freed uid is never reassigned while any file it owns may survive.
	AllocateUID(ctx context.Context) (uint32, error)
	// SumIsolatedQuotaByUserID totals quota_mb across a tenant's ISOLATED
	// subaccounts, for the split-allocation check (Σ subaccounts + tenant ≤
	// package quota).
	SumIsolatedQuotaByUserID(ctx context.Context, userID string) (uint64, error)
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

// ReserveWithinCap — see the interface doc. The whole check-and-insert runs in
// one transaction gated by a FOR UPDATE lock on the tenant's users row: a
// stable per-tenant serialization point that exists even at zero FTP rows (a
// COUNT ... FOR UPDATE would lock nothing there and let two first-creates both
// pass). The row lock releases on commit/rollback — no GET_LOCK connection-pool
// leak — and the section holds no external call, so it is brief. JAB-262.
func (r *ftpAccountRepo) ReserveWithinCap(ctx context.Context, acct *models.FtpAccount, maxAccounts int, pkgDiskQuotaMB uint32) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedID string
		if err := tx.Raw("SELECT id FROM users WHERE id = ? FOR UPDATE", acct.UserID).Scan(&lockedID).Error; err != nil {
			return err
		}
		if lockedID == "" {
			return ErrNotFound
		}
		var count int64
		if err := tx.Model(&models.FtpAccount{}).Where("user_id = ?", acct.UserID).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(maxAccounts) {
			return ErrFtpCapExceeded
		}
		if acct.Isolated && pkgDiskQuotaMB > 0 {
			var used uint64
			if err := tx.Model(&models.FtpAccount{}).
				Where("user_id = ? AND isolated = ?", acct.UserID, true).
				Select("COALESCE(SUM(quota_mb), 0)").Scan(&used).Error; err != nil {
				return err
			}
			if used+uint64(acct.QuotaMB) > uint64(pkgDiskQuotaMB) {
				return ErrFtpQuotaSplitExceeded
			}
		}
		if err := tx.Create(acct).Error; err != nil {
			return translateFtpAccount(err)
		}
		return nil
	})
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
		Select("home_path", "ftp_access", "sftp_access", "webdav_access", "is_enabled", "updated_at").
		Updates(map[string]any{
			"home_path":     acct.HomePath,
			"ftp_access":    acct.FTPAccess,
			"sftp_access":   acct.SFTPAccess,
			"webdav_access": acct.WebDAVAccess,
			"is_enabled":    acct.IsEnabled,
			"updated_at":    acct.UpdatedAt,
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

func (r *ftpAccountRepo) AllocateUID(ctx context.Context) (uint32, error) {
	var uid uint32
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// FOR UPDATE serializes concurrent creates so two subaccounts never get
		// the same uid. Read the current high-water mark, then bump it.
		if err := tx.Raw("SELECT next_uid FROM ftp_subaccount_uid_seq WHERE id = 1 FOR UPDATE").Scan(&uid).Error; err != nil {
			return err
		}
		if uid == 0 {
			return errors.New("ftp_subaccount_uid_seq is not seeded")
		}
		return tx.Exec("UPDATE ftp_subaccount_uid_seq SET next_uid = next_uid + 1 WHERE id = 1").Error
	})
	if err != nil {
		return 0, err
	}
	return uid, nil
}

func (r *ftpAccountRepo) SumIsolatedQuotaByUserID(ctx context.Context, userID string) (uint64, error) {
	var sum uint64
	row := r.db.WithContext(ctx).
		Model(&models.FtpAccount{}).
		Where("user_id = ? AND isolated = 1", userID).
		Select("COALESCE(SUM(quota_mb), 0)").Row()
	if err := row.Scan(&sum); err != nil {
		return 0, err
	}
	return sum, nil
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
