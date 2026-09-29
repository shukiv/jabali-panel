package repository

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// ErrAddressInUse is returned when a mailbox would share its address with an
// alias, mail group or shared resource in the same domain, or one of those
// with a mailbox. Migration 000306's triggers refuse the row: Stalwart would
// sign the mailbox in to the account holding the alias.
var ErrAddressInUse = errors.New("repository: the address already belongs to an alias, group or mailbox")

// addressInUseMessage is the MESSAGE_TEXT of the migration 000306 triggers.
const addressInUseMessage = "the address already belongs to an alias, group or mailbox"

// errSignal is MariaDB's ER_SIGNAL_EXCEPTION, raised by SIGNAL.
const errSignal = 1644

// mapAddressInUse turns the triggers' refusal into ErrAddressInUse and
// returns any other error unchanged.
func mapAddressInUse(err error) error {
	var my *mysql.MySQLError
	if errors.As(err, &my) && my.Number == errSignal && strings.Contains(my.Message, addressInUseMessage) {
		return ErrAddressInUse
	}
	return err
}
