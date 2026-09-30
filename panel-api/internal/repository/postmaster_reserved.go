package repository

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
)

// postmasterReservedMessage is the MESSAGE_TEXT of the migration 000309
// triggers that refuse a new mailbox, alias, group or shared resource at
// postmaster@ on a domain other than the panel hostname's (ADR-0110).
const postmasterReservedMessage = "postmaster@ belongs to the server administrator"

// errSignalException is MariaDB's ER_SIGNAL_EXCEPTION, raised by SIGNAL.
const errSignalException = 1644

// mapPostmasterReserved turns the trigger's refusal into
// mailaddr.ErrPostmasterReserved, so every door (API, CLI, importers, backup
// restore) can report it as a reserved address rather than an internal error.
// Any other error is returned unchanged.
func mapPostmasterReserved(err error) error {
	var my *mysql.MySQLError
	if errors.As(err, &my) && my.Number == errSignalException && strings.Contains(my.Message, postmasterReservedMessage) {
		return mailaddr.ErrPostmasterReserved
	}
	return err
}
