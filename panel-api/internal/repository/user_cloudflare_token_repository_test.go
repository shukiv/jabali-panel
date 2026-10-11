package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2032: a tenant's own Cloudflare token.

func TestUserCloudflareTokenGet_NoneIsNotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	mock.ExpectQuery("SELECT \\* FROM `user_cloudflare_tokens` WHERE user_id = \\?").
		WithArgs("01HZUSER00000000000000000A", 1).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}))

	_, err := NewUserCloudflareTokenRepository(db).Get(context.Background(), "01HZUSER00000000000000000A")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Put replaces the user's earlier token in one statement: a second Verify &
// Save must not fail on the primary key.
func TestUserCloudflareTokenPut_Upserts(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `user_cloudflare_tokens` .* ON DUPLICATE KEY UPDATE `token_enc`=VALUES\\(`token_enc`\\),`verified_at`=VALUES\\(`verified_at`\\),`updated_at`=VALUES\\(`updated_at`\\)").
		WillReturnResult(sqlmock.NewResult(1, 2))
	mock.ExpectCommit()

	row := &models.UserCloudflareToken{UserID: "01HZUSER00000000000000000A", TokenEnc: []byte("sealed"), VerifiedAt: time.Now().UTC()}
	require.NoError(t, NewUserCloudflareTokenRepository(db).Put(context.Background(), row))
	require.False(t, row.UpdatedAt.IsZero(), "Put must stamp UpdatedAt")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserCloudflareTokenDelete_NoneIsNotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `user_cloudflare_tokens` WHERE user_id = \\?").
		WithArgs("01HZUSER00000000000000000A").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err := NewUserCloudflareTokenRepository(db).Delete(context.Background(), "01HZUSER00000000000000000A")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
