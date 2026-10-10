package repository

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// PasswordChangeGrace is how long after a password change an app password
// still counts as made before it. Stalwart answers webmail and JMAP logins
// from a login cache that keeps taking the old password until the panel's
// flush reaches it (the agent may hold a flush back a few seconds), so an
// app password made in that window may have been made with the old one.
const PasswordChangeGrace = time.Minute

// MailLoginRepository lists the mailboxes that may sign in to the mail
// server, each with the cutoff for its app passwords: PasswordChangeGrace
// after its password last changed, or when it was created. The mail
// credentials pass removes the app passwords and API keys every other
// account holds, and the app passwords made at or before the cutoff.
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
// on a verified domain whose owner isn't suspended. password_changed_at is
// unset on a mailbox that still has the password it was created with.
const mailLoginQuery = "SELECT m.email_cached AS address, m.password_changed_at AS changed_at, m.created_at AS created_at " +
	"FROM mailboxes m JOIN domains d ON d.id = m.domain_id " +
	"WHERE m.is_disabled = 0 AND d.ownership_status = 'verified' " +
	"AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = d.user_id AND u.suspended = 1)"

// ListMailLogins returns lower-cased address → the cutoff for its app
// passwords.
func (r *mailLoginRepo) ListMailLogins(ctx context.Context) (map[string]time.Time, error) {
	var rows []struct {
		Address   string
		ChangedAt *time.Time
		CreatedAt time.Time
	}
	if err := r.db.WithContext(ctx).Raw(mailLoginQuery).Scan(&rows).Error; err != nil {
		return nil, translate(err)
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		cutoff := row.CreatedAt
		if row.ChangedAt != nil {
			cutoff = row.ChangedAt.Add(PasswordChangeGrace)
		}
		out[strings.ToLower(row.Address)] = cutoff.UTC()
	}
	return out, nil
}
