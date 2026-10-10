package repository

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// MailLoginRepository lists the mailboxes that may sign in to the mail
// server, each with when its password last changed. The mail credentials
// pass removes the app passwords and API keys every other account holds, and
// the app passwords made at or before the password last changed.
type MailLoginRepository interface {
	ListMailLogins(ctx context.Context) (map[string]time.Time, error)
}

type mailLoginRepo struct{ db *gorm.DB }

// NewMailLoginRepository returns a GORM-backed repo.
func NewMailLoginRepository(db *gorm.DB) MailLoginRepository {
	return &mailLoginRepo{db: db}
}

// mailLoginQuery has the conditions of the mail server's queryLogin
// (install/stalwart/apply-plan.json.tmpl and install.sh): an enabled mailbox
// on a verified domain whose owner isn't suspended. A mailbox without a
// password_changed_at (one made before migration 000318 by a path that
// didn't set it) counts from its creation.
const mailLoginQuery = "SELECT m.email_cached AS address, COALESCE(m.password_changed_at, m.created_at) AS changed_at " +
	"FROM mailboxes m JOIN domains d ON d.id = m.domain_id " +
	"WHERE m.is_disabled = 0 AND d.ownership_status = 'verified' " +
	"AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = d.user_id AND u.suspended = 1)"

// ListMailLogins returns lower-cased address → when its password last
// changed.
func (r *mailLoginRepo) ListMailLogins(ctx context.Context) (map[string]time.Time, error) {
	var rows []struct {
		Address   string
		ChangedAt time.Time
	}
	if err := r.db.WithContext(ctx).Raw(mailLoginQuery).Scan(&rows).Error; err != nil {
		return nil, translate(err)
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		out[strings.ToLower(row.Address)] = row.ChangedAt.UTC()
	}
	return out, nil
}
