package repository_test

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// BatchUpdateDiskUsage lives on the concrete *userRepo (not the repo-wide
// UserRepository interface, which 9 fakes implement) — reached by type
// assertion, exactly as the disk-usage sweeper's UserStore does.
type batchDiskStore interface {
	BatchUpdateDiskUsage(ctx context.Context, rows []repository.DiskUsageRow, checkedAt time.Time) error
}

func newBatchRepo(t *testing.T, gdb *gorm.DB) batchDiskStore {
	t.Helper()
	s, ok := repository.NewUserRepository(gdb).(batchDiskStore)
	if !ok {
		t.Fatal("*userRepo does not implement BatchUpdateDiskUsage")
	}
	return s
}

// One chunk → one UPDATE with a CASE per column, scoped by WHERE id IN (…), and
// NO users.updated_at column (a background sweep must not look like a user edit).
func TestUserRepository_BatchUpdateDiskUsage_OneStatement(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := newBatchRepo(t, gdb)

	checkedAt := time.Now().UTC()
	rows := []repository.DiskUsageRow{
		{UserID: "u1", UsedKB: 100, LimitKB: 1000},
		{UserID: "u2", UsedKB: 200, LimitKB: 0},
	}

	mock.ExpectExec(regexp.QuoteMeta("UPDATE `users` SET `disk_used_kb` = CASE `id`")).
		WithArgs(
			// disk_used_kb CASE pairs: (id, used)
			"u1", sqlmock.AnyArg(), "u2", sqlmock.AnyArg(),
			// disk_limit_kb CASE pairs: (id, limit)
			"u1", sqlmock.AnyArg(), "u2", sqlmock.AnyArg(),
			// disk_checked_at
			sqlmock.AnyArg(),
			// WHERE id IN (…)
			"u1", "u2",
		).
		WillReturnResult(sqlmock.NewResult(0, 2))

	require.NoError(t, repo.BatchUpdateDiskUsage(context.Background(), rows, checkedAt))
	require.NoError(t, mock.ExpectationsWereMet())
}

// A batch larger than the chunk size persists in a BOUNDED number of statements
// (⌈N/chunk⌉), not one per account — the whole point of JAB-376.
func TestUserRepository_BatchUpdateDiskUsage_ChunksLargeBatch(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := newBatchRepo(t, gdb)

	rows := make([]repository.DiskUsageRow, 501) // 500 chunk + 1 → 2 statements
	for i := range rows {
		rows[i] = repository.DiskUsageRow{UserID: string(rune('A'+i%26)) + itoa(i), UsedKB: uint64(i), LimitKB: 0}
	}

	// Exactly two UPDATE statements, not 501.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `users` SET `disk_used_kb` = CASE `id`")).
		WillReturnResult(sqlmock.NewResult(0, 500))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `users` SET `disk_used_kb` = CASE `id`")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.BatchUpdateDiskUsage(context.Background(), rows, time.Now().UTC()))
	require.NoError(t, mock.ExpectationsWereMet())
}

// A duplicate UserID must collapse to one CASE entry (last wins), never emit
// `CASE id WHEN 'u1' THEN … WHEN 'u1' THEN …` which MySQL resolves to the first.
func TestUserRepository_BatchUpdateDiskUsage_DedupsDuplicateIDs(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := newBatchRepo(t, gdb)

	rows := []repository.DiskUsageRow{
		{UserID: "u1", UsedKB: 100, LimitKB: 0},
		{UserID: "u1", UsedKB: 999, LimitKB: 5000}, // dup id, later value wins
	}
	// One statement, one WHEN pair per column (2 args each), one id in the IN.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `users` SET `disk_used_kb` = CASE `id`")).
		WithArgs(
			"u1", sqlmock.AnyArg(), // used CASE (single)
			"u1", sqlmock.AnyArg(), // limit CASE (single)
			sqlmock.AnyArg(), // checked_at
			"u1",             // IN list (single)
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.BatchUpdateDiskUsage(context.Background(), rows, time.Now().UTC()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepository_BatchUpdateDiskUsage_EmptyIsNoOp(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := newBatchRepo(t, gdb)
	// No ExpectExec — an empty batch must issue no statement.
	require.NoError(t, repo.BatchUpdateDiskUsage(context.Background(), nil, time.Now().UTC()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
