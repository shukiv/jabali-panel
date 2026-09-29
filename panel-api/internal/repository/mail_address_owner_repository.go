package repository

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

// MailAddressOwnerRepository says which principal owns a mail address in the
// panel's database. The mail-address sweep (mailaddrowner.Sweep) compares it
// with the aliases Stalwart's registry holds.
type MailAddressOwnerRepository interface {
	// Owner returns the address of the principal address belongs to: the
	// mailbox at the address, else the mailbox an enabled alias at the
	// address delivers to, else the mail group at the address. It returns ""
	// when nothing owns the address. A mailbox wins over an alias at the same
	// address, as in Stalwart's SQL directory queries.
	Owner(ctx context.Context, address string) (string, error)
}

type mailAddressOwnerRepo struct{ db *gorm.DB }

// NewMailAddressOwnerRepository returns the GORM-backed repository.
func NewMailAddressOwnerRepository(db *gorm.DB) MailAddressOwnerRepository {
	return &mailAddressOwnerRepo{db: db}
}

const mailAddressOwnerQuery = `SELECT o.owner FROM (
  SELECT 1 AS prio, m.email_cached AS owner FROM mailboxes m WHERE m.email_cached = ?
  UNION ALL
  SELECT 2 AS prio, mb.email_cached AS owner FROM email_forwarders f
    JOIN domains d ON d.id = f.domain_id
    JOIN mailboxes mb ON mb.id = f.mailbox_id
    WHERE f.enabled = 1 AND f.type = 'alias' AND CONCAT(f.local_part, '@', d.name) = ?
  UNION ALL
  SELECT 3 AS prio, g.email_cached AS owner FROM mail_groups g WHERE g.email_cached = ?
) o ORDER BY o.prio LIMIT 1`

func (r *mailAddressOwnerRepo) Owner(ctx context.Context, address string) (string, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	var owners []string
	if err := r.db.WithContext(ctx).Raw(mailAddressOwnerQuery, address, address, address).Scan(&owners).Error; err != nil {
		return "", err
	}
	if len(owners) == 0 {
		return "", nil
	}
	return owners[0], nil
}
