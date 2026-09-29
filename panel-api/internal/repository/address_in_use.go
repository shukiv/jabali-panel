package repository

import (
	"context"
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

// mailboxAddressHeldQuery is the condition migration 000306's
// trg_mailboxes_one_owner_insert refuses a new mailbox on: an alias (enabled
// or not), a mail group or a shared resource at the address.
const mailboxAddressHeldQuery = `SELECT
  EXISTS (SELECT 1 FROM email_forwarders f WHERE f.domain_id = ? AND f.type = 'alias' AND f.local_part = ?)
  OR EXISTS (SELECT 1 FROM mail_groups g WHERE g.domain_id = ? AND g.local_part = ?)
  OR EXISTS (SELECT 1 FROM shared_resources s WHERE s.domain_id = ? AND s.local_part = ?)`

// AddressHeld reports whether an alias, mail group or shared resource in the
// domain holds localPart, so the database would refuse a mailbox there.
func (r *mailboxRepo) AddressHeld(ctx context.Context, domainID, localPart string) (bool, error) {
	var held bool
	err := r.db.WithContext(ctx).
		Raw(mailboxAddressHeldQuery, domainID, localPart, domainID, localPart, domainID, localPart).
		Scan(&held).Error
	return held, err
}

// MailboxAddressHeld asks repo whether the database would refuse a mailbox at
// localPart@domain. The mailbox create doors ask before they take the address
// off Stalwart's registry: a create the database refuses must leave a live
// alias where it is. A repository without the check (a test fake) reports the
// address free, and the trigger still refuses the insert.
func MailboxAddressHeld(ctx context.Context, repo MailboxRepository, domainID, localPart string) (bool, error) {
	h, ok := repo.(interface {
		AddressHeld(ctx context.Context, domainID, localPart string) (bool, error)
	})
	if !ok {
		return false, nil
	}
	return h.AddressHeld(ctx, domainID, localPart)
}
