package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailboxops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// reconcileSendmailCreds (JAB-230) converges the PHP mail() relay identities:
// every active domain gets a noreply@<domain> SendOnly mailbox (auth-only,
// never receives or stores — GH #371) plus a credential file on the agent
// side that the jabali-sendmail shim reads. Runs per-tick and idempotent, so
// it doubles as the fleet backfill: existing boxes converge on their first
// reconcile after `jabali update`, with no one-shot migration tool.
//
// EmailEnabled is deliberately NOT checked (user decision 2026-08-07): a
// domain with external MX still needs its WordPress forms to send, and a
// send-only principal requires no MX/DNS. The UI/CLI guard against creating
// REGULAR mailboxes on such domains stays where it is.
//
// Steady state is one in-memory fingerprint check per domain per tick; the
// map resets on restart, making the first tick a cheap re-ensure sweep
// (agent call is a content-compare noop). A failed domain stays out of the
// map and retries next tick.
const sendmailRelayLocalPart = "noreply"

func (r *Reconciler) reconcileSendmailCreds(ctx context.Context) {
	if r.domains == nil || r.users == nil || r.agent == nil || r.mailboxes == nil || r.serverSettings == nil || r.sendmailSSOKey == nil {
		return
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.serverSettings.Get(sctx)
	scancel()
	if err != nil || srv == nil || srv.Hostname == "" {
		return
	}
	mailHost := models.PanelMailHostname(srv.Hostname)

	domains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("sendmail-cred: list domains failed", "error", err)
		return
	}

	r.sendmailMu.Lock()
	if r.sendmailDone == nil {
		r.sendmailDone = make(map[string]string)
	}
	r.sendmailMu.Unlock()

	// Username cache — many domains share an owner.
	usernames := make(map[string]string)

	for i := range domains {
		d := &domains[i]
		if err := r.ensureSendmailCred(ctx, d, mailHost, usernames); err != nil {
			r.log.Warn("sendmail-cred: converge failed", "domain", d.Name, "error", err)
		}
	}
}

func (r *Reconciler) ensureSendmailCred(ctx context.Context, d *models.Domain, mailHost string, usernames map[string]string) error {
	fingerprintWant := sendmailFingerprint(d.Name, mailHost)
	r.sendmailMu.Lock()
	done := r.sendmailDone[d.ID]
	r.sendmailMu.Unlock()
	if done == fingerprintWant {
		return nil
	}

	username, ok := usernames[d.UserID]
	if !ok {
		u, err := r.users.FindByID(ctx, d.UserID)
		if err != nil {
			return fmt.Errorf("owner lookup: %w", err)
		}
		// Admins (incl. the panel-primary domain's synthesized user_<ulid>
		// row) and rows without a Linux account run no tenant PHP — same
		// skip set ReconcilePHPPools uses. Without the IsAdmin check the
		// panel hostname's owner passes the empty-check but fails the OS
		// user.Lookup agent-side, warn-looping every tick (seen on the
		// testserver E2E).
		if u.Username == nil || *u.Username == "" || u.IsAdmin {
			r.sendmailMu.Lock()
			r.sendmailDone[d.ID] = fingerprintWant
			r.sendmailMu.Unlock()
			return nil
		}
		username = *u.Username
		usernames[d.UserID] = username
	}

	email, password, err := r.ensureRelayMailbox(ctx, d)
	if err != nil {
		return err
	}
	if email == "" {
		// Both candidate local parts are taken by human mailboxes (migrated
		// accounts often ship a real noreply@). Never touch those — the shim
		// falls back to the user's default.cred from another domain. Cache as
		// done so this warns once per boot, not every tick; a panel restart
		// (daily fleet auto-update at the latest) re-evaluates.
		r.log.Warn("sendmail-cred: no free relay local part — leaving human mailboxes untouched",
			"domain", d.Name, "tried", sendmailRelayLocalPart+", "+sendmailRelayFallbackLocalPart)
		r.sendmailMu.Lock()
		r.sendmailDone[d.ID] = fingerprintWant
		r.sendmailMu.Unlock()
		return nil
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := r.agent.Call(cctx, "sendmail.cred.ensure", map[string]any{
		"username": username,
		"domain":   d.Name,
		"email":    email, // may be the jabali-noreply@ fallback — must match the mailbox row
		"password": password,
		"host":     mailHost,
	}); err != nil {
		// A DB username with no OS account is a permanent condition from
		// this loop's perspective — cache it (one warn per boot) instead of
		// retry-warning every tick. Anything else stays retryable.
		var ae *agent.AgentError
		if errors.As(err, &ae) && ae.Code == agent.CodeNotFound {
			r.log.Warn("sendmail-cred: owner has no OS user — skipping until restart", "domain", d.Name, "username", username)
			r.sendmailMu.Lock()
			r.sendmailDone[d.ID] = fingerprintWant
			r.sendmailMu.Unlock()
			return nil
		}
		return fmt.Errorf("agent cred ensure: %w", err)
	}

	r.sendmailMu.Lock()
	r.sendmailDone[d.ID] = fingerprintWant
	r.sendmailMu.Unlock()
	return nil
}

// sendmailRelayFallbackLocalPart is tried when a HUMAN mailbox already owns
// noreply@<domain> (migrated accounts ship those routinely).
const sendmailRelayFallbackLocalPart = "jabali-noreply"

// ensureRelayMailbox returns (email, plaintext password) for the domain's
// relay identity, creating a SendOnly mailbox or rotating a panel-created row
// whose sealed plaintext is missing/unreadable.
//
// It NEVER touches a mailbox with SendOnly=false: those are human accounts
// (a migrated noreply@ arrives with an imported hash and PasswordEnc=NULL —
// rotating it would silently break the owner's IMAP/webmail login fleet-wide).
// Both candidates taken by humans → ("", "", nil): the caller skips the
// domain and the shim's default.cred fallback covers it.
func (r *Reconciler) ensureRelayMailbox(ctx context.Context, d *models.Domain) (string, string, error) {
	for _, localPart := range []string{sendmailRelayLocalPart, sendmailRelayFallbackLocalPart} {
		email := localPart + "@" + d.Name
		mb, err := r.mailboxes.FindByEmail(ctx, email)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return "", "", fmt.Errorf("find %s: %w", email, err)
		}

		if mb == nil {
			password, cerr := r.createRelayMailbox(ctx, d, localPart, email)
			if cerr != nil {
				return "", "", cerr
			}
			return email, password, nil
		}

		if !mb.SendOnly {
			continue // human mailbox — hands off, try the fallback name
		}

		if len(mb.PasswordEnc) > 0 {
			plain, oerr := r.sendmailSSOKey.Open(mb.PasswordEnc)
			if oerr == nil {
				return email, string(plain), nil
			}
			// Sealed under a rotated key — fall through to a password rotate.
		}

		// Panel-created row without a recoverable plaintext: rotate.
		password := ids.NewSecret()
		hash, herr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if herr != nil {
			return "", "", fmt.Errorf("hash: %w", herr)
		}
		enc, serr := r.sendmailSSOKey.Seal([]byte(password))
		if serr != nil {
			return "", "", fmt.Errorf("seal: %w", serr)
		}
		if uerr := r.mailboxes.UpdatePasswordHashAndEnc(ctx, mb.ID, string(hash), enc); uerr != nil {
			return "", "", fmt.Errorf("rotate %s: %w", email, uerr)
		}
		// Invalidate Stalwart's auth cache for the principal (best-effort).
		nctx, ncancel := context.WithTimeout(ctx, 10*time.Second)
		_, _ = r.agent.Call(nctx, "mailbox.set_password", map[string]any{"id": mb.ID, "email": email})
		ncancel()
		return email, password, nil
	}
	return "", "", nil
}

func (r *Reconciler) createRelayMailbox(ctx context.Context, d *models.Domain, localPart, email string) (string, error) {
	// JAB-291: the sendmail relay is an infrastructure principal — mint it via
	// the shared Mailbox Lifecycle's explicit system entry point (System=true,
	// SendOnly=true, sealed envelope) rather than hand-assembling the row here.
	// Stalwart JMAP-registry registration stays best-effort (ADR-0045: DB is
	// authoritative), passed as the notify hook.
	notify := func(nctx context.Context, cmd string, params any) {
		ncctx, ncancel := context.WithTimeout(nctx, 10*time.Second)
		defer ncancel()
		_, _ = r.agent.Call(ncctx, cmd, params)
	}
	_, password, err := mailboxops.CreateSystem(ctx,
		mailboxops.Deps{Mailboxes: r.mailboxes, SSOKey: r.sendmailSSOKey},
		mailboxops.SystemCreateInput{
			Domain:      d,
			LocalPart:   localPart,
			DisplayName: d.Name + " (system sender)",
			QuotaBytes:  16 * 1024 * 1024,
		}, notify)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", email, err)
	}
	return password, nil
}

// sendmailFingerprint keys the done-cache on the inputs that would change
// the cred file. Password changes always pass through this reconciler
// itself, which clears the entry by writing a new fingerprint check path
// (rotate happens before the fingerprint is stored).
func sendmailFingerprint(domain, host string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + host))
	return hex.EncodeToString(sum[:8])
}

// WithSendmailCreds wires the JAB-230 relay-credential convergence: the
// mailbox repo backs the noreply@ SendOnly principals and key seals their
// plaintext (same /etc/jabali-panel/sso.key envelope the webmail SSO uses).
// Both nil-safe: missing either disables the loop.
func (r *Reconciler) WithSendmailCreds(mailboxes repository.MailboxRepository, key *ssokey.Key) *Reconciler {
	r.mailboxes = mailboxes
	r.sendmailSSOKey = key
	return r
}
