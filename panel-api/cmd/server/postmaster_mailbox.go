package main

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/app"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// postmasterLocalPart is the RFC 5321 postmaster address every mail domain
// must accept.
const postmasterLocalPart = "postmaster"

// postmasterMailboxQuotaBytes bounds the admin postmaster mailbox: it takes
// postmaster@ mail for every domain that has no postmaster of its own.
const postmasterMailboxQuotaBytes = 1 << 30 // 1 GiB

// provisionPostmasterMailbox ensures the server admin's postmaster mailbox
// exists on the panel-primary domain (the panel hostname). Stalwart's directory
// (queryRecipient, install.sh and apply-plan.json.tmpl) resolves
// postmaster@<domain> to this mailbox for every email-enabled domain that has
// no postmaster mailbox, alias or group of its own, and queryEmailAliases lists
// those addresses on it. Without it Stalwart answers 550 to postmaster@, so the
// DMARC and TLS reports receivers send there never arrive (ADR-0110).
//
// Stalwart keeps each of those addresses on this account for good, so
// migration 000308 refuses a new postmaster@ mailbox, alias, group or shared
// resource on any other domain: a tenant mailbox there would sign in here.
//
// It is an ordinary mailbox, listed on the panel domain like any other, so the
// admin reads it in webmail. Stalwart reads the reports for the panel to import
// and also delivers them here, beside other mail to postmaster@; report mail
// filed as spam is expunged from Junk after 30 days.
//
// It does nothing when the panel domain has no email, or when the admin has
// already made postmaster@ there: a mailbox is used as is, and an alias or
// group is the admin's choice (the directory fallback then stays off, since it
// needs a mailbox). Returns the address, or "".
func provisionPostmasterMailbox(ctx context.Context, deps app.Deps, log *slog.Logger) string {
	if deps.Domains == nil || deps.Mailboxes == nil || deps.SSOKey == nil {
		return ""
	}
	dom, err := deps.Domains.FindPanelPrimary(ctx)
	if err != nil || dom == nil {
		log.Info("postmaster mailbox: no panel-primary domain yet; postmaster@ for domains without their own is not accepted")
		return ""
	}
	if !dom.EmailEnabled {
		log.Info("postmaster mailbox: email not enabled on the panel domain; postmaster@ for domains without their own is not accepted", "domain", dom.Name)
		return ""
	}
	email := postmasterLocalPart + "@" + dom.Name

	exists, err := deps.Mailboxes.ExistsByDomainAndLocalPart(ctx, dom.ID, postmasterLocalPart)
	if err != nil {
		log.Warn("postmaster mailbox: existence check failed", "err", err)
		return ""
	}
	if exists {
		return email
	}
	if deps.MailGroups != nil {
		taken, err := deps.MailGroups.ExistsByDomainAndLocalPart(ctx, dom.ID, postmasterLocalPart)
		if err != nil {
			log.Warn("postmaster mailbox: group check failed", "err", err)
			return ""
		}
		if taken {
			log.Warn("postmaster mailbox: postmaster@ on the panel domain is a group; other domains' postmaster@ is not accepted until it is a mailbox", "domain", dom.Name)
			return ""
		}
	}
	if deps.Forwarders != nil {
		fwds, err := deps.Forwarders.ListByDomainIDs(ctx, []string{dom.ID})
		if err != nil {
			log.Warn("postmaster mailbox: forwarder check failed", "err", err)
			return ""
		}
		for _, f := range fwds {
			if f.LocalPart != nil && *f.LocalPart == postmasterLocalPart {
				log.Warn("postmaster mailbox: postmaster@ on the panel domain is an alias; other domains' postmaster@ is not accepted until it is a mailbox", "domain", dom.Name)
				return ""
			}
		}
	}

	// A shared resource there (or a row the checks above missed) makes the
	// database refuse the mailbox; clearing the address on the mail server
	// first would take a live alias off its account.
	held, err := repository.MailboxAddressHeld(ctx, deps.Mailboxes, dom.ID, postmasterLocalPart)
	if err != nil {
		log.Warn("postmaster mailbox: address check failed", "err", err)
		return ""
	}
	if held {
		log.Warn("postmaster mailbox: postmaster@ on the panel domain belongs to an alias, group or shared resource; other domains' postmaster@ is not accepted until it is a mailbox", "domain", dom.Name)
		return ""
	}

	// Random password, stored bcrypt-hashed (Stalwart auth) and sealed with
	// the SSO key (webmail sign-in from the panel), as for any mailbox.
	password := ids.NewSecret()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Warn("postmaster mailbox: hash failed", "err", err)
		return ""
	}
	sealed, err := deps.SSOKey.Seal([]byte(password))
	if err != nil {
		log.Warn("postmaster mailbox: seal failed", "err", err)
		return ""
	}
	// Stalwart keeps every alias it has seen, so a stale alias at this address
	// would sign the new mailbox in to another account. Best effort, like the
	// notify mailbox: this runs at panel start, before Stalwart exists on a
	// fresh install, on the admin's own domain, and the reconciler's alias
	// sweep takes a stale alias off within minutes.
	if deps.MailAddresses != nil {
		if err := deps.MailAddresses.ReleaseAddress(ctx, email); err != nil {
			log.Warn("postmaster mailbox: could not clear the address on the mail server; the alias sweep retries", "err", err)
		}
	}
	now := time.Now().UTC()
	mb := &models.Mailbox{
		ID:           ids.NewULID(),
		DomainID:     dom.ID,
		LocalPart:    postmasterLocalPart,
		PasswordHash: string(hash),
		PasswordEnc:  sealed,
		QuotaBytes:   postmasterMailboxQuotaBytes,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := deps.Mailboxes.Create(ctx, mb); err != nil {
		log.Warn("postmaster mailbox: create row failed", "err", err)
		return ""
	}
	// Best-effort Stalwart sync; the SQL directory is authoritative.
	if deps.Agent != nil {
		if _, err := deps.Agent.Call(ctx, "mailbox.create", map[string]any{"id": mb.ID, "email": email}); err != nil {
			log.Warn("postmaster mailbox: agent mailbox.create failed (delivery still works via SQL directory)", "err", err)
		}
	}
	log.Info("provisioned the admin postmaster mailbox", "email", email)
	return email
}
