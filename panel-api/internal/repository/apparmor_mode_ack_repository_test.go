package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2078: acknowledged AppArmor profiles.

// Put replaces a profile's earlier acknowledgement in one statement: a second
// Acknowledge must not fail on the primary key.
func TestAppArmorModeAckPut_Upserts(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `apparmor_mode_acks` .* ON DUPLICATE KEY UPDATE `mode`=VALUES\\(`mode`\\),`acknowledged_by`=VALUES\\(`acknowledged_by`\\),`acknowledged_at`=VALUES\\(`acknowledged_at`\\)").
		WillReturnResult(sqlmock.NewResult(1, 2))
	mock.ExpectCommit()

	by := "01HZUSER00000000000000000A"
	row := &models.AppArmorModeAck{Profile: "jabali-fpm-app", Mode: "complain", AcknowledgedBy: &by, AcknowledgedAt: time.Now().UTC()}
	require.NoError(t, NewAppArmorModeAckRepository(db).Put(context.Background(), row))
	require.NoError(t, mock.ExpectationsWereMet())
}

// Deleting a profile without an acknowledgement is not an error: the panel
// deletes on every mode change.
func TestAppArmorModeAckDelete_NoneIsNotAnError(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `apparmor_mode_acks` WHERE profile = \\?").
		WithArgs("jabali-fpm-app").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	require.NoError(t, NewAppArmorModeAckRepository(db).Delete(context.Background(), "jabali-fpm-app"))
	require.NoError(t, mock.ExpectationsWereMet())
}
