package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// ErrDatabaseCapExceeded is returned by DatabaseRepository.CreateWithinCap
// when the owner already has as many databases as the cap allows.
var ErrDatabaseCapExceeded = errors.New("repository: database cap exceeded")

// ErrDatabaseUserCapExceeded is returned by
// DatabaseUserRepository.CreateWithinCap when the owner already has as many
// database users as the cap allows.
var ErrDatabaseUserCapExceeded = errors.New("repository: database user cap exceeded")

// insertWithinOwnerCap inserts row while the owner has fewer than max rows of
// countModel (a table with a user_id column), else capErr. ErrNotFound when
// the owner's users row is missing.
//
// The count and the insert run in one transaction holding the owner's users
// row FOR UPDATE, so two creates racing for the last free slot serialize on
// the lock. The users row is the lock because it exists even at zero rows,
// where a COUNT ... FOR UPDATE would lock nothing (the FTP cap's reasoning,
// JAB-262). Keep the lock before the count: under REPEATABLE READ the read
// view is taken at the first plain SELECT, so a count made after the lock
// sees the other create's commit.
func insertWithinOwnerCap(ctx context.Context, db *gorm.DB, ownerID string, countModel, row any, max uint32, capErr error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedID string
		if err := tx.Raw("SELECT id FROM users WHERE id = ? FOR UPDATE", ownerID).Scan(&lockedID).Error; err != nil {
			return err
		}
		if lockedID == "" {
			return ErrNotFound
		}
		var count int64
		if err := tx.Model(countModel).Where("user_id = ?", ownerID).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(max) {
			return capErr
		}
		if err := tx.Create(row).Error; err != nil {
			return translate(err)
		}
		return nil
	})
}
