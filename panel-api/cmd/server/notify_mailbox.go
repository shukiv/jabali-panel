package main

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/app"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// notifyMailboxLocalPart is the system mailbox used as the authenticated
// envelope sender for local-mode email notifications (GH #322). Stalwart's
// submission port (:587) requires AUTH, so the notification Email sender logs
// in as this mailbox; it only ever sends, never receives.
const notifyMailboxLocalPart = mailaddr.NotifyLocalPart

// notifyMailboxQuotaBytes is a small quota — the mailbox is send-only and
// should never accumulate stored mail.
const notifyMailboxQuotaBytes = 64 * 1024 * 1024 // 64 MiB

// provisionNotifyMailbox ensures the send-only notify mailbox exists on the
// panel's primary mail domain (the panel hostname domain, M6.4) so local-mode
// email notifications can authenticate to Stalwart. Returns the notify address,
// or "" when it can't be provisioned yet (no email-enabled panel domain) — in
// that case local mode has no creds and the operator must use an external SMTP
// relay until the panel domain has email enabled and the panel is restarted.
func provisionNotifyMailbox(ctx context.Context, deps app.Deps, log *slog.Logger) string {
	if deps.Mailboxes == nil || deps.Domains == nil || deps.ServerSettings == nil || deps.SSOKey == nil {
		return ""
	}
	st, err := deps.ServerSettings.Get(ctx)
	if err != nil || st == nil || st.Hostname == "" {
		log.Warn("notify mailbox: no hostname set; local-mode email notifications need an external relay")
		return ""
	}
	dom, err := deps.Domains.FindByName(ctx, st.Hostname)
	if err != nil || dom == nil {
		log.Warn("notify mailbox: panel hostname domain not found; skipping provision", "hostname", st.Hostname)
		return ""
	}
	if !dom.EmailEnabled {
		log.Warn("notify mailbox: email not enabled on the panel domain; skipping provision", "domain", dom.Name)
		return ""
	}
	email := notifyMailboxLocalPart + "@" + dom.Name

	exists, err := deps.Mailboxes.ExistsByDomainAndLocalPart(ctx, dom.ID, notifyMailboxLocalPart)
	if err != nil {
		log.Warn("notify mailbox: existence check failed", "err", err)
		return ""
	}
	if exists {
		return email
	}

	// Strong random password (crypto/rand). Stored bcrypt-hashed (for
	// Stalwart auth) plus sealed with the SSO key (so the Email sender can
	// recover the plaintext to authenticate). Same envelope mailbox creation
	// uses (createMailboxDirect).
	password := ids.NewSecret()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Warn("notify mailbox: hash failed", "err", err)
		return ""
	}
	sealed, err := deps.SSOKey.Seal([]byte(password))
	if err != nil {
		log.Warn("notify mailbox: seal failed", "err", err)
		return ""
	}
	// Stalwart keeps every alias it has seen, so a stale alias at this address
	// would sign the notify mailbox in to another account. Best effort, unlike
	// the other mailbox creates: on a fresh install the panel starts before
	// Stalwart, the panel domain is the admin's own, and the reconciler's
	// alias sweep takes a stale alias off within minutes.
	if deps.MailAddresses != nil {
		if err := deps.MailAddresses.ReleaseAddress(ctx, email); err != nil {
			log.Warn("notify mailbox: could not clear the address on the mail server; the alias sweep retries", "err", err)
		}
	}
	now := time.Now().UTC()
	mb := &models.Mailbox{
		ID:           ids.NewULID(),
		DomainID:     dom.ID,
		LocalPart:    notifyMailboxLocalPart,
		PasswordHash: string(hash),
		PasswordEnc:  sealed,
		QuotaBytes:   notifyMailboxQuotaBytes,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := deps.Mailboxes.Create(ctx, mb); err != nil {
		log.Warn("notify mailbox: create row failed", "err", err)
		return ""
	}
	// Best-effort Stalwart sync (the SQL directory is authoritative for auth, so
	// this is belt-and-suspenders — same call createMailboxDirect makes).
	if deps.Agent != nil {
		if _, err := deps.Agent.Call(ctx, "mailbox.create", map[string]any{"id": mb.ID, "email": email}); err != nil {
			log.Warn("notify mailbox: agent mailbox.create failed (auth still works via SQL directory)", "err", err)
		}
	}
	log.Info("provisioned notify mailbox for local-mode email notifications", "email", email)
	return email
}

// notifyLocalCreds returns a loader the Email sender calls to authenticate
// loopback submission: it loads the notify mailbox and unseals its password.
func notifyLocalCreds(deps app.Deps, email string) func(ctx context.Context) (string, string, bool) {
	return func(ctx context.Context) (string, string, bool) {
		if email == "" || deps.Mailboxes == nil || deps.SSOKey == nil {
			return "", "", false
		}
		mb, err := deps.Mailboxes.FindByEmail(ctx, email)
		if err != nil || mb == nil || len(mb.PasswordEnc) == 0 {
			return "", "", false
		}
		pw, err := deps.SSOKey.Open(mb.PasswordEnc)
		if err != nil {
			return "", "", false
		}
		return email, string(pw), true
	}
}
