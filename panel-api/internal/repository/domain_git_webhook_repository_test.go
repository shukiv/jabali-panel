package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// A new secret replaces the key and the secret only: a waiting push and the
// last delivery stay.
func TestGitWebhook_PutKeepsWhatDeliveriesRecorded(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainGitWebhookRepository(gdb)
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `domain_git_webhooks` .+ ON DUPLICATE KEY UPDATE " +
		"`hook_id`=VALUES\\(`hook_id`\\),`secret_enc`=VALUES\\(`secret_enc`\\),`updated_at`=VALUES\\(`updated_at`\\)$").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Put(context.Background(), &models.DomainGitWebhook{
		DomainID: "dom", HookID: "0123456789abcdef0123456789abcdef", SecretEnc: []byte("sealed"), CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

// A push already waiting keeps its time.
func TestGitWebhook_QueueKeepsTheFirstPush(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainGitWebhookRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domain_git_webhooks` SET `queued_at`=\\?,`updated_at`=\\? WHERE domain_id = \\? AND queued_at IS NULL$").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "dom").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	require.NoError(t, repo.Queue(context.Background(), "dom", time.Now().UTC()))
	require.NoError(t, mock.ExpectationsWereMet())
}

// Of two passes, only the one whose statement changed the row takes the push.
func TestGitWebhook_UnqueueTakesThePushOnce(t *testing.T) {
	for _, tc := range []struct {
		affected int64
		want     bool
	}{{1, true}, {0, false}} {
		gdb, mock, raw := newMockDB(t)
		repo := repository.NewDomainGitWebhookRepository(gdb)
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE `domain_git_webhooks` SET `queued_at`=.+,`updated_at`=\\? WHERE domain_id = \\? AND queued_at IS NOT NULL$").
			WillReturnResult(sqlmock.NewResult(0, tc.affected))
		mock.ExpectCommit()

		taken, err := repo.Unqueue(context.Background(), "dom")
		require.NoError(t, err)
		require.Equal(t, tc.want, taken)
		require.NoError(t, mock.ExpectationsWereMet())
		raw.Close()
	}
}

// A waiting push started later records its result and keeps its delivery's
// time.
func TestGitWebhook_RecordDeliveryWithoutATimeKeepsIt(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainGitWebhookRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domain_git_webhooks` SET `last_result`=\\?,`updated_at`=\\? WHERE domain_id = \\?$").
		WithArgs(models.GitWebhookStarted, sqlmock.AnyArg(), "dom").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domain_git_webhooks` SET `last_delivery_at`=\\?,`last_result`=\\?,`updated_at`=\\? WHERE domain_id = \\?$").
		WithArgs(sqlmock.AnyArg(), models.GitWebhookIgnored, sqlmock.AnyArg(), "dom").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.RecordDelivery(context.Background(), "dom", time.Time{}, models.GitWebhookStarted))
	require.NoError(t, repo.RecordDelivery(context.Background(), "dom", time.Now().UTC(), models.GitWebhookIgnored))
	require.NoError(t, mock.ExpectationsWereMet())
}
