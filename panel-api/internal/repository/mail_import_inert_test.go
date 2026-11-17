package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A forwarder created disabled is stored disabled. The migration importers
// create source forwarders inert (enabled=0) unless --preserve-source-state;
// with a gorm `default:1` tag GORM left the false out of the INSERT and the
// column's DEFAULT 1 made every imported forwarder active.
func TestEmailForwarderCreate_DisabledIsWritten(t *testing.T) {
	db, mock, raw := newMockForwarderDB(t)
	defer raw.Close()
	lp := "sales"
	f := &models.EmailForwarder{ID: "01", DomainID: "d", Type: "external", LocalPart: &lp, Target: "x@example.com", Enabled: false, ManagedBy: "m35-da-import"}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `email_forwarders` (`id`,`mailbox_id`,`domain_id`,`type`,`local_part`,`target`,`enabled`,`keep_copy`,`managed_by`,`created_at`,`updated_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?)")).
		WithArgs("01", nil, "d", "external", "sales", "x@example.com", false, false, "m35-da-import", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, NewEmailForwarderRepository(db).Create(context.Background(), f))
	require.NoError(t, mock.ExpectationsWereMet())
	require.False(t, f.Enabled)
}

// The same for a mail filter (the cPanel importer creates them inert too).
func TestEmailFilterCreate_DisabledIsWritten(t *testing.T) {
	db, mock, raw := newMockForwarderDB(t)
	defer raw.Close()
	f := &models.EmailFilter{ID: "02", MailboxID: "m", Name: "cpanel-import", Enabled: false, ManagedBy: "m35"}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `email_filters` (`id`,`mailbox_id`,`name`,`sieve_text`,`cpanel_raw`,`priority`,`enabled`,`managed_by`,`created_at`,`updated_at`) VALUES (?,?,?,?,?,?,?,?,?,?)")).
		WithArgs("02", "m", "cpanel-import", nil, nil, 0, false, "m35", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, NewEmailFilterRepository(db).Create(context.Background(), f))
	require.NoError(t, mock.ExpectationsWereMet())
}
