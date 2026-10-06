// Apply is the inverse of Build: takes an AccountMetadata bundle
// recovered from a stage=meta restic snapshot and inserts the
// per-user rows back into the panel DB.
//
// M30.2 disaster-recovery support. Idempotent: rows that already
// exist by primary key are left alone (FindByID success → skip);
// missing rows are inserted with the manifest's IDs preserved so
// references between rows (domain → ssl_cert, domain → php_pool,
// etc.) stay valid.
//
// Out of scope (deferred):
//   - mailboxes / forwarders / autoresponders / shares — stalwart
//     spool rebuild lives in the agent's mail stage, not here
//   - domain_dnssec_keys — pdnsutil drives this; metadata is a cache
//   - egress policy / requests / limit overrides — scoped to M34
package backupmetadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/forwarderops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sshkeyops"
)

// reservedMailboxLocal reports whether a restored mailbox's local part is one
// the panel keeps for itself (GH #1637), in any spelling that resolves to it.
// A backup is input: a mailbox there would share its Stalwart principal with
// the domain directory's host.
func reservedMailboxLocal(local, domain string) bool {
	if canon, _, err := mailaddr.Canonicalise(local + "@" + domain); err == nil {
		return mailaddr.CheckNotReserved(canon) != nil
	}
	return mailaddr.CheckNotReserved(strings.ToLower(strings.TrimSpace(local))) != nil
}

// ApplyResult counts what landed during a single Apply call.
// The CLI prints these so the operator sees concretely what came back.
type ApplyResult struct {
	UserCreated    bool
	PHPPools       int
	PHPPoolIni     int
	Domains        int
	SSLCerts       int
	Mailboxes      int
	Forwarders     int
	Databases      int
	DatabaseUsers  int
	DatabaseGrants int
	AppInstalls    int
	DockerApps     int
	SSHKeys        int
	CronJobs       int
	FtpAccounts    int
	Skipped        int
	Errors         []string
	// LoginRestored is true when the user's Kratos identity exists AND has a
	// password credential after the apply — i.e. the account can actually sign
	// in with its old password. When false, LoginNote says why + what to do
	// (typically: issue a recovery link). Both are set only when a KratosClient
	// was provided and the bundle carried a user email.
	LoginRestored bool
	LoginNote     string
}

// Apply walks the metadata bundle and inserts missing rows into the
// panel DB. Returns a non-nil ApplyResult even on partial failures;
// per-row errors are collected in Result.Errors so a domain failure
// doesn't abort the database/cron path.
// restoredOwnership decides a restored domain's ownership (GH #1816 /
// ADR-0170 decision 2). Every restore that recreates a domain row is
// admin-run (the admin restore API, `jabali account restore`), so the
// administrator vouches for the name — except for a row the archive itself
// records as pending, which is never promoted by a restore.
func restoredOwnership(dm internalbackup.MetadataDomain) domainops.OwnershipDecision {
	if dm.OwnershipStatus != "" && dm.OwnershipStatus != models.OwnershipVerified {
		return domainops.OwnershipDecision{}
	}
	return domainops.OwnershipDecision{Verified: true, Method: models.OwnershipMethodRestore}
}

func Apply(ctx context.Context, m *internalbackup.AccountMetadata, d Deps) ApplyResult {
	r := ApplyResult{}
	if m == nil {
		return r
	}
	now := time.Now().UTC()

	// 1) User row first — every other table FKs to user_id.
	created, uerr := applyUser(ctx, m, d, now)
	if uerr != nil {
		r.Errors = append(r.Errors, "user: "+uerr.Error())
		// no point inserting child rows without the parent
		return r
	}
	r.UserCreated = created

	// 2) PHP pools + ini overrides — domains reference pools by id.
	// poolIDs maps each backup pool id to the pool that stands for it on this
	// server; a backup pool missing from it was not restored (GH #1993).
	poolIDs := map[string]string{}
	if d.PHPPools != nil {
		for _, p := range m.PHPPools {
			if existing, err := d.PHPPools.FindByID(ctx, p.ID); err == nil && existing != nil {
				// SECURITY: an uploaded bundle is untrusted. A pool with this id
				// that another account owns runs PHP as that account's user;
				// binding a restored domain to it would hand the domain that
				// user's privileges.
				if existing.UserID != m.User.ID {
					r.Errors = append(r.Errors, fmt.Sprintf("php_pool %s: not restored: a pool with this id belongs to another account", p.ID))
					continue
				}
				poolIDs[p.ID] = existing.ID
				restorePoolIni(ctx, d, &r, existing.ID, p.IniOverrides)
				r.Skipped++
				continue
			} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
				r.Errors = append(r.Errors, fmt.Sprintf("php_pool %s: lookup: %v", p.ID, err))
				continue
			}
			// GH #1993: an account created on this server before the restore
			// already has its own pool for the default PHP version, under
			// another id. One pool per (user, version) is allowed, so the
			// backup's pool of that version is this one.
			if existing, err := d.PHPPools.FindByUserAndVersion(ctx, m.User.ID, p.PHPVersion); err == nil && existing != nil {
				poolIDs[p.ID] = existing.ID
				restorePoolIni(ctx, d, &r, existing.ID, p.IniOverrides)
				r.Skipped++
				continue
			} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
				r.Errors = append(r.Errors, fmt.Sprintf("php_pool %s: lookup PHP %s pool: %v", p.ID, p.PHPVersion, err))
				continue
			}
			pool := &models.PHPPool{
				ID:                        p.ID,
				UserID:                    m.User.ID,
				PHPVersion:                p.PHPVersion,
				PmMode:                    p.PmMode,
				PmMaxChildren:             p.PmMaxChildren,
				ProcessIdleTimeoutSeconds: p.ProcessIdleTimeoutSeconds,
				Status:                    "pending",
				CreatedAt:                 now,
				UpdatedAt:                 now,
			}
			if err := d.PHPPools.Create(ctx, pool); err != nil {
				r.Errors = append(r.Errors, fmt.Sprintf("php_pool %s: create: %v", p.ID, err))
				continue
			}
			poolIDs[p.ID] = p.ID
			r.PHPPools++
			restorePoolIni(ctx, d, &r, p.ID, p.IniOverrides)
		}
	}

	// 3) Domains + SSL certs.
	// refused holds the domains CheckDomain turned down (GH #1898); none of
	// their mailboxes, forwarders or app installs are restored either.
	refused := map[string]bool{}
	// SECURITY: an uploaded bundle is untrusted, and every id in it was chosen
	// by whoever made the file. These hold the rows that belong to the account
	// being restored, restored now or already its own; a child row the bundle
	// ties to anything else is not restored (GH #1993).
	ownDomains := map[string]bool{}
	ownMailboxes := map[string]bool{}
	ownDatabases := map[string]bool{}
	// account is the account's username on THIS server. Every path the bundle
	// names is moved off the bundle's username onto it and checked against
	// it: the bundle's username is only a claim by whoever made the file.
	account := restoreAccountUsername(ctx, m, d)
	bundleUser := ""
	if m.User.Username != nil {
		bundleUser = *m.User.Username
	}
	if d.Domains != nil {
		for _, dm := range m.Domains {
			if existing, err := d.Domains.FindByID(ctx, dm.ID); err == nil && existing != nil {
				if existing.UserID != m.User.ID {
					refused[dm.ID] = true
					r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): not restored: a domain with this id belongs to another account", dm.ID, dm.Name))
					continue
				}
				ownDomains[dm.ID] = true
				r.Skipped++
				continue
			} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
				refused[dm.ID] = true
				r.Errors = append(r.Errors, fmt.Sprintf("domain %s: lookup: %v", dm.ID, err))
				continue
			}
			row := &models.Domain{
				ID:                    dm.ID,
				UserID:                m.User.ID,
				Name:                  dm.Name,
				DocRoot:               rehomePath(dm.DocRoot, "/home", bundleUser, account),
				IsEnabled:             dm.IsEnabled,
				NginxCustomDirectives: dm.NginxCustomDirectives,
				RedirectAllTo:         dm.RedirectAllTo,
				RedirectAllType:       dm.RedirectAllType,
				IndexPriority:         dm.IndexPriority,
				SSLEnabled:            dm.SSLEnabled,
				PHPPoolID:             dm.PHPPoolID,
				PHPMemoryLimit:        dm.PHPMemoryLimit,
				PHPUploadMaxFilesize:  dm.PHPUploadMaxFilesize,
				PHPPostMaxSize:        dm.PHPPostMaxSize,
				PHPMaxInputVars:       dm.PHPMaxInputVars,
				PHPMaxExecutionTime:   dm.PHPMaxExecutionTime,
				PHPMaxInputTime:       dm.PHPMaxInputTime,
				RateLimitRPS:          dm.RateLimitRPS,
				ConnectionLimit:       dm.ConnectionLimit,
				EmailEnabled:          dm.EmailEnabled,
				DkimSelector:          dm.DkimSelector,
				DkimPublicKey:         dm.DkimPublicKey,
				CatchallTarget:        dm.CatchallTarget,
				DisclaimerEnabled:     dm.DisclaimerEnabled,
				DisclaimerText:        dm.DisclaimerText,
				DNSSECEnabled:         dm.DNSSECEnabled,
				CreatedAt:             now,
				UpdatedAt:             now,
			}
			// Custom nginx directives are admin-only raw config, checked only by
			// the relaxed admin rules. From an uploaded file they are config
			// written by whoever made it (GH #1993).
			if d.Untrusted && row.NginxCustomDirectives != nil && strings.TrimSpace(*row.NginxCustomDirectives) != "" {
				row.NginxCustomDirectives = nil
				r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): custom nginx directives not restored from an uploaded backup; re-add them in the domain's settings after reviewing them", dm.ID, dm.Name))
			}
			if d.CheckDomain == nil {
				refused[dm.ID] = true
				r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): not restored: the restore checks are not wired", dm.ID, dm.Name))
				continue
			}
			warnings, cerr := d.CheckDomain(ctx, row, account)
			if cerr != nil {
				refused[dm.ID] = true
				r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): not restored: %v", dm.ID, dm.Name, cerr))
				continue
			}
			if err := domainops.ApplyOwnershipDecision(&row.OwnershipState, restoredOwnership(dm), now); err != nil {
				refused[dm.ID] = true
				r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): not restored: ownership token: %v", dm.ID, dm.Name, err))
				continue
			}
			for _, w := range warnings {
				r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): %s", dm.ID, dm.Name, w))
			}
			// Bind the domain to the pool that stands for its backup pool here.
			// A pool that wasn't restored leaves it unbound: the reconciler binds
			// an unbound domain to the account's default pool, where pointing at
			// the missing id would fail the row on its foreign key (GH #1993).
			if row.PHPPoolID != nil && d.PHPPools != nil {
				if id, ok := poolIDs[*row.PHPPoolID]; ok {
					row.PHPPoolID = &id
				} else {
					row.PHPPoolID = nil
					r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): its PHP pool was not restored; it uses the account's default PHP pool", dm.ID, dm.Name))
				}
			}
			if err := d.Domains.Create(ctx, row); err != nil {
				// Without the row its mailboxes, forwarders and app installs
				// can't be stored either; skip them so this stays the error.
				refused[dm.ID] = true
				r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): create: %v", dm.ID, dm.Name, err))
				continue
			}
			ownDomains[dm.ID] = true
			r.Domains++
			if dm.SSLCertificate != nil && d.SSLCerts != nil {
				cert := &models.SSLCertificate{
					ID:           dm.SSLCertificate.ID,
					DomainID:     dm.ID,
					Status:       dm.SSLCertificate.Status,
					RenewalCount: dm.SSLCertificate.RenewalCount,
					LastError:    dm.SSLCertificate.LastError,
					Staging:      dm.SSLCertificate.Staging,
					CertPath:     dm.SSLCertificate.CertPath,
					KeyPath:      dm.SSLCertificate.KeyPath,
					CreatedAt:    now,
					UpdatedAt:    now,
				}
				// SECURITY: the vhost renderers write these paths into nginx
				// configs as root. Keep them only when they name this domain's
				// own files; otherwise the certificate is issued again.
				switch {
				case !ownCertFiles(row.Name, cert.CertPath, cert.KeyPath):
					r.Errors = append(r.Errors, fmt.Sprintf("ssl_cert %s (%s): its certificate files are not this domain's own; it will be issued again", cert.ID, row.Name))
					cert.CertPath, cert.KeyPath, cert.Status = nil, nil, models.SSLStatusPending
				case !knownSSLStatus[cert.Status]:
					r.Errors = append(r.Errors, fmt.Sprintf("ssl_cert %s (%s): status %q is not one the panel knows; it will be issued again", cert.ID, row.Name, cert.Status))
					cert.CertPath, cert.KeyPath, cert.Status = nil, nil, models.SSLStatusPending
				}
				if err := d.SSLCerts.Create(ctx, cert); err != nil {
					r.Errors = append(r.Errors, fmt.Sprintf("ssl_cert %s: create: %v", cert.ID, err))
					continue
				}
				r.SSLCerts++
			}
		}
	}

	// 3b) Mailboxes (+ autoresponders + shares) and forwarders.
	// Stalwart reads jabali_panel.mailboxes via its SQL directory
	// (ADR-0042), so recreating the row restores the account's auth +
	// quota immediately. The Maildir MESSAGE content lives in a separate
	// backup stage and is NOT replayed here — the restore command
	// surfaces that as a warning.
	for di := range m.Domains {
		dm := m.Domains[di]
		if refused[dm.ID] {
			continue
		}
		if d.Mailboxes != nil {
			for _, mb := range dm.Mailboxes {
				if reservedMailboxLocal(mb.LocalPart, dm.Name) {
					r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: not restored: %s@%s is reserved for the domain directory", mb.ID, mb.LocalPart, dm.Name))
					continue
				}
				if existing, err := d.Mailboxes.FindByID(ctx, mb.ID); err == nil && existing != nil {
					// Its autoresponder and shares below are written by mailbox
					// id, so a mailbox of another domain stays untouched.
					if existing.DomainID != dm.ID {
						r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: not restored: a mailbox with this id belongs to another domain", mb.ID))
						continue
					}
					ownMailboxes[mb.ID] = true
					r.Skipped++
				} else {
					// The database refuses a mailbox where an alias, group or
					// shared resource is; clearing the address first would take
					// that alias off its account in Stalwart's registry.
					held, err := repository.MailboxAddressHeld(ctx, d.Mailboxes, dm.ID, mb.LocalPart)
					if err != nil {
						r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: not restored: address check: %v", mb.ID, err))
						continue
					}
					if held {
						r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: not restored: %s@%s already belongs to an alias, group or shared resource", mb.ID, mb.LocalPart, dm.Name))
						continue
					}
					// A stale registry alias at this address would sign the
					// restored mailbox in to another account.
					if d.MailAddresses == nil {
						r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: not restored: the mail server client is not wired", mb.ID))
						continue
					}
					if err := d.MailAddresses.ReleaseAddress(ctx, mb.LocalPart+"@"+dm.Name); err != nil {
						r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: not restored: clear %s@%s on the mail server: %v", mb.ID, mb.LocalPart, dm.Name, err))
						continue
					}
					row := &models.Mailbox{
						ID:           mb.ID,
						DomainID:     dm.ID,
						LocalPart:    mb.LocalPart,
						EmailCached:  mb.EmailCached,
						PasswordHash: mb.PasswordHash,
						PasswordEnc:  mb.PasswordEnc,
						QuotaBytes:   mb.QuotaBytes,
						IsDisabled:   mb.IsDisabled,
						CreatedAt:    now,
						UpdatedAt:    now,
					}
					if err := d.Mailboxes.Create(ctx, row); err != nil {
						r.Errors = append(r.Errors, fmt.Sprintf("mailbox %s: create: %v", mb.ID, err))
						continue
					}
					ownMailboxes[mb.ID] = true
					r.Mailboxes++
				}
				// Autoresponder is keyed by the mailbox PK; Update upserts.
				if mb.Autoresponder != nil && d.Autoresponders != nil {
					ar := &models.EmailAutoresponder{
						MailboxID: mb.ID,
						Enabled:   mb.Autoresponder.Enabled,
						Subject:   mb.Autoresponder.Subject,
						TextBody:  mb.Autoresponder.TextBody,
						HTMLBody:  mb.Autoresponder.HTMLBody,
						FromDate:  parseMetaTime(mb.Autoresponder.FromDate),
						ToDate:    parseMetaTime(mb.Autoresponder.ToDate),
					}
					if err := d.Autoresponders.Update(ctx, ar); err != nil {
						r.Errors = append(r.Errors, fmt.Sprintf("autoresponder %s: %v", mb.ID, err))
					}
				}
				// Mailbox shares owned by this mailbox.
				if d.MailboxShares != nil {
					for _, sh := range mb.SharedWith {
						if existing, err := d.MailboxShares.FindByID(ctx, sh.ID); err == nil && existing != nil {
							r.Skipped++
							continue
						}
						var rights models.Rights
						if sh.Rights != "" {
							_ = json.Unmarshal([]byte(sh.Rights), &rights)
						}
						share := &models.MailboxShare{
							ID:                  sh.ID,
							OwnerMailboxID:      mb.ID,
							SharedWithMailboxID: sh.SharedWithMailboxID,
							Rights:              rights,
							CreatedAt:           now,
						}
						if err := d.MailboxShares.Create(ctx, share); err != nil {
							r.Errors = append(r.Errors, fmt.Sprintf("mailbox_share %s: create: %v", sh.ID, err))
						}
					}
				}
			}
		}
		if d.Forwarders != nil {
			// Imported forwarders only reach Stalwart via forwarder.apply — the DB
			// row alone does nothing (GH #1795 follow-up). Map each mailbox id to
			// its email so an enabled restored forwarder can be converged after the
			// loop; mailboxes with no cached email fall back to first-mutation
			// self-heal.
			mbEmail := map[string]string{}
			for _, mb := range dm.Mailboxes {
				if mb.EmailCached != "" {
					mbEmail[mb.ID] = mb.EmailCached
				}
			}
			convergeFwds := map[string]string{}
			for _, fw := range dm.Forwarders {
				if existing, err := d.Forwarders.FindByID(ctx, fw.ID); err == nil && existing != nil {
					r.Skipped++
					continue
				}
				if fw.MailboxID != nil && !ownMailboxes[*fw.MailboxID] {
					r.Errors = append(r.Errors, fmt.Sprintf("forwarder %s: not restored: its mailbox is not one of this account's restored mailboxes", fw.ID))
					continue
				}
				row := &models.EmailForwarder{
					ID:        fw.ID,
					MailboxID: fw.MailboxID,
					DomainID:  dm.ID,
					Type:      fw.Type,
					LocalPart: fw.LocalPart,
					Target:    fw.Target,
					Enabled:   fw.Enabled,
					CreatedAt: now,
					UpdatedAt: now,
				}
				if err := d.Forwarders.Create(ctx, row); err != nil {
					r.Errors = append(r.Errors, fmt.Sprintf("forwarder %s: create: %v", fw.ID, err))
					continue
				}
				r.Forwarders++
				if row.Enabled && row.MailboxID != nil {
					if email, ok := mbEmail[*row.MailboxID]; ok {
						convergeFwds[*row.MailboxID] = email
					}
				}
			}
			// Best-effort push. nil agent (e.g. the backup scheduler) skips this;
			// the rows converge on the first later forwarder mutation instead.
			if d.Agent != nil {
				for mbID, email := range convergeFwds {
					// nil autoresponders: the reconcile sweep re-converges the full
				// composite (forwards + autoresponder) on its next tick (GH #1795).
				if cErr := forwarderops.Converge(ctx, d.Agent, d.Forwarders, d.Autoresponders, mbID, email); cErr != nil {
						r.Errors = append(r.Errors, fmt.Sprintf("forwarder converge %s: %v", email, cErr))
					}
				}
			}
		}
	}

	// 4) Databases + db_users + grants.
	if d.Databases != nil {
		// SECURITY (GH #1993): a database or database-user row is a handle
		// the account's owner can drop, dump, restore or re-password through
		// the panel. Never this server's own; from an uploaded file, only
		// names in the account's own namespace that no other account has.
		var otherDBs, otherDBUsers, accountDBs map[string]bool
		dbRows, dbUserRows := m.Databases, m.DatabaseUsers
		if d.Untrusted {
			var listErr error
			if otherDBs, otherDBUsers, accountDBs, listErr = accountDatabaseNames(ctx, d, m.User.ID); listErr != nil {
				r.Errors = append(r.Errors, fmt.Sprintf("databases: not restored: %v", listErr))
				dbRows, dbUserRows = nil, nil
			}
		}
		dbNameToID := make(map[string]string, len(dbRows))
		for _, db := range dbRows {
			if why := restoredDatabaseRefusal(db.Name, account, d.Untrusted, otherDBs); why != "" {
				r.Errors = append(r.Errors, fmt.Sprintf("database %s (%s): not restored: %s", db.ID, db.Name, why))
				continue
			}
			if d.Untrusted && !d.RestoredDatabases[db.Name] && !accountDBs[db.Name] {
				r.Errors = append(r.Errors, fmt.Sprintf("database %s (%s): not restored: the restore didn't load its data into a database of this account", db.ID, db.Name))
				continue
			}
			dbNameToID[db.Name] = db.ID
			row := &models.Database{
				ID:        db.ID,
				UserID:    m.User.ID,
				Name:      db.Name,
				Engine:    db.Engine,
				Charset:   db.Charset,
				Collation: db.Collation,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := d.Databases.Create(ctx, row); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					if existing, ferr := d.Databases.FindByID(ctx, db.ID); ferr == nil && existing != nil && existing.UserID == m.User.ID {
						ownDatabases[db.ID] = true
					}
					r.Skipped++
					continue
				}
				r.Errors = append(r.Errors, fmt.Sprintf("database %s (%s): create: %v", db.ID, db.Name, err))
				continue
			}
			ownDatabases[db.ID] = true
			r.Databases++
		}
		if d.DatabaseUsers != nil {
			for _, du := range dbUserRows {
				if why := restoredDBUserRefusal(du.Username, account, d.Untrusted, otherDBUsers); why != "" {
					r.Errors = append(r.Errors, fmt.Sprintf("db_user %s (%s): not restored: %s", du.ID, du.Username, why))
					continue
				}
				dbu := &models.DatabaseUser{
					ID:           du.ID,
					UserID:       m.User.ID,
					Username:     du.Username,
					PasswordHash: du.PasswordHash,
					CreatedAt:    now,
					UpdatedAt:    now,
				}
				if err := d.DatabaseUsers.Create(ctx, dbu); err != nil {
					if errors.Is(err, repository.ErrConflict) {
						// Grants below attach to this user id: only to one
						// that is this account's own.
						if existing, ferr := d.DatabaseUsers.FindByID(ctx, du.ID); ferr != nil || existing == nil || existing.UserID != m.User.ID {
							r.Errors = append(r.Errors, fmt.Sprintf("db_user %s: not restored: a database user with this id belongs to another account", du.ID))
							continue
						}
						r.Skipped++
					} else {
						r.Errors = append(r.Errors, fmt.Sprintf("db_user %s: create: %v", du.ID, err))
						continue
					}
				} else {
					r.DatabaseUsers++
				}
				if d.DatabaseGrants != nil {
					for _, g := range du.Grants {
						gid := g.ID
						if gid == "" {
							continue
						}
						dbID := g.DatabaseID
						if dbID == "" && g.DatabaseName != "" {
							dbID = dbNameToID[g.DatabaseName]
						}
						if dbID == "" {
							r.Errors = append(r.Errors, fmt.Sprintf("db_grant %s: unresolved database", gid))
							continue
						}
						if !ownDatabases[dbID] {
							r.Errors = append(r.Errors, fmt.Sprintf("db_grant %s: not restored: its database is not one of this account's", gid))
							continue
						}
						gr := &models.DatabaseUserGrant{
							ID:             gid,
							DatabaseID:     dbID,
							DatabaseUserID: du.ID,
							GrantLevel:     g.GrantLevel,
							Privileges:     g.Privileges,
							CreatedAt:      now,
							UpdatedAt:      now,
						}
						if err := d.DatabaseGrants.Create(ctx, gr); err != nil {
							if errors.Is(err, repository.ErrConflict) {
								r.Skipped++
								continue
							}
							r.Errors = append(r.Errors, fmt.Sprintf("db_grant %s: create: %v", gid, err))
							continue
						}
						r.DatabaseGrants++
					}
				}
			}
		}
	}

	// 5) App installs.
	if d.AppInstalls != nil {
		for _, ai := range m.AppInstalls {
			if refused[ai.DomainID] {
				r.Errors = append(r.Errors, fmt.Sprintf("app_install %s: not restored: its domain was refused", ai.ID))
				continue
			}
			if !ownDomains[ai.DomainID] {
				r.Errors = append(r.Errors, fmt.Sprintf("app_install %s: not restored: its domain is not one of this account's", ai.ID))
				continue
			}
			if ai.DBID != nil && *ai.DBID != "" && !ownDatabases[*ai.DBID] {
				r.Errors = append(r.Errors, fmt.Sprintf("app_install %s: not restored: its database is not one of this account's", ai.ID))
				continue
			}
			row := &models.ApplicationInstall{
				ID:            ai.ID,
				UserID:        m.User.ID,
				DomainID:      ai.DomainID,
				DBID:          ai.DBID,
				Version:       ai.Version,
				AdminUsername: ai.AdminUsername,
				AdminEmail:    ai.AdminEmail,
				Locale:        ai.Locale,
				UseWWW:        ai.UseWWW,
				Subdirectory:  ai.Subdirectory,
				Status:        ai.Status,
				AppType:       ai.AppType,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if err := d.AppInstalls.Create(ctx, row); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					r.Skipped++
					continue
				}
				r.Errors = append(r.Errors, fmt.Sprintf("app_install %s: create: %v", ai.ID, err))
				continue
			}
			r.AppInstalls++
		}
	}

	// 5b) Docker apps (GH #954): rebuild the panel row + published ports so the
	// restored app is panel-managed. The DATA tree + compose-up land via the
	// stage=docker restore (#1017); without this row the app is orphaned on disk.
	if d.DockerApps != nil && len(m.DockerApps) > 0 {
		// An app's data lives in one global dir per effective slug, so a
		// restored app must not take a slug an app with another owner uses.
		apps := m.DockerApps
		existing, listErr := d.DockerApps.ListAll(ctx)
		if listErr != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("docker_apps: not restored: list this server's apps: %v", listErr))
			apps = nil
		}
		for _, a := range apps {
			// GH #1993: a server-level app from an uploaded file would be an
			// admin-level app chosen by whoever made it.
			if a.ServerLevel && d.Untrusted {
				r.Errors = append(r.Errors, fmt.Sprintf("docker_app %s (%s): not restored: a server-level app can't be restored from an uploaded backup", a.ID, a.Slug))
				continue
			}
			// Server-level apps (GH #1360) restore with UserID NULL so they
			// stay admin/server-level; tenant apps are (re)owned by the
			// account being restored. Re-owning a server-level app to the
			// admin would wrongly subject it to tenant validation + slice
			// scoping on the next reconcile.
			var owner *string
			if !a.ServerLevel {
				uid := m.User.ID
				owner = &uid
			}
			if bad := malformedDockerSlug(a); bad != "" {
				r.Errors = append(r.Errors, fmt.Sprintf("docker_app %s: not restored: %q is not an app name", a.ID, bad))
				continue
			}
			slug, taken := dockerSlugTaken(existing, a, owner)
			if taken {
				r.Errors = append(r.Errors, fmt.Sprintf("docker_app %s: not restored: another account's app already uses %q", a.ID, slug))
				continue
			}
			if d.Untrusted && !d.RestoredDockerSlugs[slug] && !accountHasApp(existing, slug, m.User.ID) {
				r.Errors = append(r.Errors, fmt.Sprintf("docker_app %s: not restored: the restore didn't restore its data into an app folder of this account (%q)", a.ID, slug))
				continue
			}
			row := &models.DockerApp{
				ID: a.ID, UserID: owner, Slug: a.Slug, InstanceSlug: a.InstanceSlug,
				Name: a.Name, CatalogVersion: a.CatalogVersion, ImageSHA: a.ImageSHA,
				Status: a.Status, UpdateMode: a.UpdateMode,
				CPULimit: a.CPULimit, MemoryLimit: a.MemoryLimit, PIDsLimit: a.PIDsLimit,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := d.DockerApps.Create(ctx, row); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					r.Skipped++
					continue
				}
				r.Errors = append(r.Errors, fmt.Sprintf("docker_app %s: create: %v", a.ID, err))
				continue
			}
			for _, p := range a.Ports {
				port := &models.DockerAppPublishedPort{
					ID: p.ID, AppID: a.ID, PortName: p.PortName, ContainerPort: p.ContainerPort,
					BindInterface: p.BindInterface, HostPort: p.HostPort, Protocol: p.Protocol,
					ReverseProxy: p.ReverseProxy, Enabled: p.Enabled, CreatedAt: now,
				}
				if err := d.DockerApps.CreatePort(ctx, port); err != nil {
					r.Errors = append(r.Errors, fmt.Sprintf("docker_app %s port %s: create: %v", a.ID, p.PortName, err))
				}
			}
			r.DockerApps++
		}
	}

	// 6) SSH keys. Persist through the shared restore operation (JAB-292 AC4) so
	// this is no longer a direct ssh_keys writer and per-owner authorized_keys
	// convergence has one coalescing point. Backup restore follows the
	// reconciler-tick convergence model the rest of restored state uses (see
	// applyRestoreMetadata), so the batch carries no Scheduler — restored keys
	// re-converge on the next tick, not immediately.
	if d.SSHKeys != nil {
		batch := sshkeyops.NewRestoreBatch(sshkeyops.Deps{Keys: d.SSHKeys})
		for _, k := range m.SSHKeys {
			row := &models.SSHKey{
				ID:          k.ID,
				UserID:      m.User.ID,
				Name:        k.Name,
				PublicKey:   k.PublicKey,
				Fingerprint: k.Fingerprint,
				CreatedAt:   now,
			}
			if err := batch.Restore(ctx, row); err != nil {
				if errors.Is(err, sshkeyops.ErrDuplicate) {
					r.Skipped++
					continue
				}
				r.Errors = append(r.Errors, fmt.Sprintf("ssh_key %s: create: %v", k.ID, err))
				continue
			}
			r.SSHKeys++
		}
		batch.Flush()
	}

	// 7) Cron jobs.
	if d.CronJobs != nil {
		for _, cj := range m.CronJobs {
			row := &models.CronJob{
				ID:        cj.ID,
				UserID:    m.User.ID,
				Name:      cj.Name,
				Command:   cj.Command,
				Schedule:  cj.Schedule,
				Enabled:   cj.Enabled,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := d.CronJobs.Create(ctx, row); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					r.Skipped++
					continue
				}
				r.Errors = append(r.Errors, fmt.Sprintf("cron_job %s: create: %v", cj.ID, err))
				continue
			}
			r.CronJobs++
		}
	}

	// 7b) FTP/SFTP subaccounts (GH #1361). Rebuild the row as-is: the
	// reconciler reprovisions the system user/jail/quota from these fields on
	// its next tick, reusing the stored UID (its owned files carry it) and
	// JailPath. The login PASSWORD is NOT in the panel DB — the agent restores
	// it into /etc/shadow from the metadata's PasswordShadow (see the agent
	// restore staging path); without that the reconciler sets a throwaway and
	// the tenant must reset. QuotaMB is restored verbatim (a restore onto a
	// smaller package can overcommit the split — the reconciler's cap check is
	// advisory here). Username embeds the SOURCE tenant prefix and is restored
	// as-is; the home and jail paths move to this server's username.
	if d.FtpAccounts != nil {
		for _, a := range m.FtpAccounts {
			// The reconciler provisions the system user, home and jail from
			// these paths: keep them inside this account's own home and jail
			// directory, never another account's.
			home := rehomePath(a.HomePath, "/home", bundleUser, account)
			if !pathWithin(home, "/home", account) {
				r.Errors = append(r.Errors, fmt.Sprintf("ftp_account %s: not restored: home %q is outside /home/%s", a.ID, a.HomePath, account))
				continue
			}
			jail := a.JailPath
			if jail != "" {
				jail = rehomePath(jail, ftpops.JailRoot, bundleUser, account)
				if !pathWithin(jail, ftpops.JailRoot, account) {
					r.Errors = append(r.Errors, fmt.Sprintf("ftp_account %s: not restored: jail %q is outside %s/%s", a.ID, a.JailPath, ftpops.JailRoot, account))
					continue
				}
			}
			row := &models.FtpAccount{
				ID:           a.ID,
				UserID:       m.User.ID,
				Username:     a.Username,
				HomePath:     home,
				FTPAccess:    a.FTPAccess,
				SFTPAccess:   a.SFTPAccess,
				WebDAVAccess: a.WebDAVAccess,
				IsEnabled:    a.IsEnabled,
				UID:          a.UID,
				Isolated:     a.Isolated,
				QuotaMB:      a.QuotaMB,
				JailPath:     jail,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if err := d.FtpAccounts.Create(ctx, row); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					r.Skipped++
					continue
				}
				r.Errors = append(r.Errors, fmt.Sprintf("ftp_account %s: create: %v", a.ID, err))
				continue
			}
			r.FtpAccounts++
		}
	}

	// 8) Kratos identity restoration
	if m.Kratos != nil && m.Kratos.ExportedIdentity != "" && d.KratosClient != nil {
		var exportedIdentity kratosclient.ExportedIdentity
		if err := json.Unmarshal([]byte(m.Kratos.ExportedIdentity), &exportedIdentity); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("kratos identity: unmarshal: %v", err))
		} else {
			if err := d.KratosClient.ImportIdentities(ctx, []kratosclient.ExportedIdentity{exportedIdentity}); err != nil {
				r.Errors = append(r.Errors, fmt.Sprintf("kratos identity: import: %v", err))
			}
		}
	}

	// 9) Login verification (GH #954). The steps above can each half-succeed
	// (applyUser creates an identity only when the panel hash is a usable
	// bcrypt; the bundle's exported identity may predate credential capture;
	// an identity may exist from a prior run). Resolve the ground truth —
	// does an identity exist for this email, and does it hold a password? —
	// so the operator gets a definitive login status instead of a swallowed
	// warning, and a fresh (password-less) identity is minted when none
	// exists so a recovery link can actually be issued.
	//
	// SECURITY (GH #1408): gated on `created`. The email + is_admin + password
	// hash read here are bundle-controlled, and the restore-from-UPLOAD path
	// remaps only user.id — not the email — into an EXISTING target user
	// (created=false). Ungated, an uploaded tar carrying an attacker's email +
	// is_admin=true + a known hash would MINT a matching (admin) Kratos identity
	// on this box: privilege escalation. We only ever mint/verify for a user we
	// just created this run (a trusted same-box snapshot or a sanitized
	// create-from-manifest, both non-admin per applyUser); an
	// upload-into-existing-user restore never touches Kratos here.
	if created && d.KratosClient != nil && m.User.Email != "" {
		username := ""
		if m.User.Username != nil {
			username = *m.User.Username
		}
		id, err := d.KratosClient.IdentityIDByEmail(ctx, m.User.Email)
		switch {
		case err != nil:
			r.LoginNote = fmt.Sprintf("kratos lookup failed (%v) — verify Kratos is up, then issue a recovery link", err)
		case id == "":
			// No identity at all. Mint one so the account is recoverable:
			// with the panel bcrypt when the bundle carried a usable one
			// (login works immediately), otherwise credential-less (login
			// needs a recovery link).
			pwd := m.User.PasswordHash
			if pwd != "" && pwd != "!" && len(pwd) >= 59 {
				traits := kratosclient.AdminTraits{Email: m.User.Email, Username: username, IsAdmin: m.User.IsAdmin}
				if _, cerr := d.KratosClient.CreateIdentityWithPassword(ctx, traits, pwd); cerr == nil || errors.Is(cerr, kratosclient.ErrIdentityExisted) {
					r.LoginRestored = true
					r.LoginNote = "identity created with the preserved password — the user signs in with their old password"
				} else {
					r.LoginNote = fmt.Sprintf("identity create failed (%v) — issue a recovery link after fixing", cerr)
				}
			} else {
				r.LoginNote = "no identity and no usable password hash in the bundle — issue a recovery link: jabali user password " + username + " --link"
			}
		default:
			hasPW, herr := d.KratosClient.IdentityHasPassword(ctx, id)
			switch {
			case herr != nil:
				r.LoginNote = fmt.Sprintf("identity %s present but password check failed (%v)", id, herr)
			case hasPW:
				r.LoginRestored = true
				r.LoginNote = "identity present with a password credential — the user signs in with their old password"
			default:
				r.LoginNote = "identity present but has NO password credential — issue a recovery link: jabali user password " + username + " --link"
			}
		}
	}
	return r
}

// applyUser inserts the user row when missing. Returns (created, err).
// Existing row → no-op (created=false). PasswordHash falls back to "!"
// when the manifest didn't carry one (older snapshots).
func applyUser(ctx context.Context, m *internalbackup.AccountMetadata, d Deps, now time.Time) (bool, error) {
	users := d.users()
	if users == nil {
		return false, errors.New("user repo not provided in Deps")
	}
	if existing, err := users.FindByID(ctx, m.User.ID); err == nil && existing != nil {
		return false, nil
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return false, fmt.Errorf("lookup: %w", err)
	}
	// SECURITY (GH #1408): refuse to CREATE an admin user from a backup bundle.
	// An account backup is only ever a non-admin tenant (admins host nothing and
	// are never account-backed-up), so is_admin=true here is a malformed or
	// crafted bundle — and creating an admin from an attacker-controllable
	// uploaded tar is a privilege-escalation. The row is only reached on the
	// CREATE path (an existing user no-ops above), so a legitimate
	// same-box/#954 restore of a non-admin is unaffected.
	if m.User.IsAdmin {
		return false, errors.New("refusing to create an admin user from a backup bundle")
	}
	pwd := m.User.PasswordHash
	if pwd == "" {
		pwd = "!" // locked — operator must reset via Kratos recovery
	}

	// Try to create Kratos identity if we have a client and valid password hash
	var kratosIdentityID *string
	if d.KratosClient != nil && pwd != "" && pwd != "!" && len(pwd) >= 59 {
		username := ""
		if m.User.Username != nil {
			username = *m.User.Username
		}
		traits := kratosclient.AdminTraits{
			Email:    m.User.Email,
			Username: username,
			IsAdmin:  m.User.IsAdmin,
		}
		if identityID, err := d.KratosClient.CreateIdentityWithPassword(ctx, traits, pwd); err == nil {
			kratosIdentityID = &identityID
		} else {
			// Log warning but don't fail the user restore
			if d.Log != nil {
				d.Log.Warn("failed to create kratos identity during restore", "user_id", m.User.ID, "error", err)
			}
		}
	}

	row := &models.User{
		ID:                    m.User.ID,
		Email:                 m.User.Email,
		Username:              m.User.Username,
		NameFirst:             m.User.NameFirst,
		NameLast:              m.User.NameLast,
		PasswordHash:          pwd,
		IsAdmin:               m.User.IsAdmin,
		PackageID:             m.User.PackageID,
		LinuxUID:              m.User.LinuxUID,
		MysqladminUsername:    m.User.MysqladminUsername,
		MysqladminPasswordEnc: m.User.MysqladminPasswordEnc,
		KratosIdentityID:      kratosIdentityID,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := users.Create(ctx, row); err != nil {
		return false, fmt.Errorf("create: %w", err)
	}
	return true, nil
}

// systemDatabaseNames are this server's own databases (with every jabali_*
// one); systemDBUserNames its own MariaDB accounts (with every jabali* and
// jb_s_* restore shadow one).
var (
	systemDatabaseNames = map[string]bool{
		"mysql": true, "information_schema": true, "performance_schema": true, "sys": true,
		"crowdsec": true, "postgres": true, "template0": true, "template1": true, "jabali": true,
	}
	systemDBUserNames = map[string]bool{
		"root": true, "mysql": true, "mariadb.sys": true, "crowdsec": true, "debian-sys-maint": true,
		"postgres": true, "jabali": true,
	}
	restoredDBNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

func systemDatabase(name string) bool {
	l := strings.ToLower(name)
	return systemDatabaseNames[l] || strings.HasPrefix(l, "jabali_")
}

func systemDBUser(name string) bool {
	l := strings.ToLower(name)
	return systemDBUserNames[l] || strings.HasPrefix(l, "jabali_") || strings.HasPrefix(l, "jabali-") || strings.HasPrefix(l, "jb_s_")
}

// restoredDatabaseRefusal says why a database row named name may not be
// restored for account, or "".
func restoredDatabaseRefusal(name, account string, untrusted bool, others map[string]bool) string {
	switch {
	case systemDatabase(name):
		return "it is one of this server's own databases"
	case !untrusted:
		return ""
	case !restoredDBNameRe.MatchString(name) || !accountNameRe.MatchString(account) ||
		len(name) <= len(account)+1 || !strings.HasPrefix(name, account+"_"):
		return fmt.Sprintf("a database from an uploaded backup must be named %s_<name>", account)
	case others[name]:
		return "another account has a database with this name"
	}
	return ""
}

// restoredDBUserRefusal is restoredDatabaseRefusal for a database user.
func restoredDBUserRefusal(name, account string, untrusted bool, others map[string]bool) string {
	switch {
	case systemDBUser(name):
		return "it is one of this server's own database accounts"
	case !untrusted:
		return ""
	case !restoredDBNameRe.MatchString(name) || !accountNameRe.MatchString(account) ||
		len(name) <= len(account)+1 || !strings.HasPrefix(name, account+"_"):
		return fmt.Sprintf("a database user from an uploaded backup must be named %s_<name>", account)
	case others[name]:
		return "another account has a database user with this name"
	}
	return ""
}

// accountDatabaseNames returns the database and database-user names rows of
// accounts other than userID hold, and the database names userID's own rows
// hold.
func accountDatabaseNames(ctx context.Context, d Deps, userID string) (dbs, users, own map[string]bool, err error) {
	dbs, users, own = map[string]bool{}, map[string]bool{}, map[string]bool{}
	rows, _, err := d.Databases.List(ctx, repository.ListOptions{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list this server's databases: %w", err)
	}
	for _, r := range rows {
		if r.UserID != userID {
			dbs[r.Name] = true
		} else {
			own[r.Name] = true
		}
	}
	if d.DatabaseUsers != nil {
		urows, _, err := d.DatabaseUsers.List(ctx, repository.ListOptions{})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("list this server's database users: %w", err)
		}
		for _, r := range urows {
			if r.UserID != userID {
				users[r.Username] = true
			}
		}
	}
	return dbs, users, own, nil
}

// dockerSlugRe is the agent's rule for a docker app slug (validateSlug): the
// slugs name the app's data directory and compose project.
var dockerSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// malformedDockerSlug returns a's slug or instance slug when it isn't a plain
// app name, else "".
func malformedDockerSlug(a internalbackup.MetadataDockerApp) string {
	if !dockerSlugRe.MatchString(a.Slug) {
		return a.Slug
	}
	if a.InstanceSlug != "" && !dockerSlugRe.MatchString(a.InstanceSlug) {
		return a.InstanceSlug
	}
	return ""
}

// accountHasApp reports whether the account userID already has an app whose
// data folder is slug.
func accountHasApp(existing []*models.DockerApp, slug, userID string) bool {
	for _, e := range existing {
		if e != nil && e.UserID != nil && *e.UserID == userID && e.EffectiveSlug() == slug {
			return true
		}
	}
	return false
}

// dockerSlugTaken reports whether an app on this server other than a, with an
// owner other than owner (nil = server-level), already uses a's effective
// slug, and returns that slug.
func dockerSlugTaken(existing []*models.DockerApp, a internalbackup.MetadataDockerApp, owner *string) (string, bool) {
	slug := a.InstanceSlug
	if slug == "" {
		slug = a.Slug
	}
	for _, e := range existing {
		if e == nil || e.ID == a.ID || e.EffectiveSlug() != slug {
			continue
		}
		sameOwner := (e.UserID == nil && owner == nil) || (e.UserID != nil && owner != nil && *e.UserID == *owner)
		if !sameOwner {
			return slug, true
		}
	}
	return slug, false
}

// knownSSLStatus is every status an ssl_certificates row can hold.
var knownSSLStatus = map[string]bool{
	models.SSLStatusPending: true, models.SSLStatusIssuing: true, models.SSLStatusIssued: true,
	models.SSLStatusFailed: true, models.SSLStatusRevoked: true, models.SSLStatusRenewing: true,
	models.SSLStatusSelfSigned: true, models.SSLStatusCustom: true, models.SSLStatusPendingACMERetry: true,
}

// certFileDirs are the two layouts the panel writes a domain's certificate
// in: certbot's lineage (issued and custom certificates) and the self-signed
// placeholder.
var certFileDirs = []string{"/etc/letsencrypt/live/", "/etc/ssl/jabali-selfsigned/"}

// ownCertFiles reports whether cert and key are domain's own certificate
// files in one of certFileDirs, or both unset.
func ownCertFiles(domain string, cert, key *string) bool {
	if cert == nil || key == nil {
		return cert == nil && key == nil
	}
	if domain == "" || strings.HasPrefix(domain, ".") || strings.ContainsAny(domain, "/\\") {
		return false
	}
	for _, dir := range certFileDirs {
		if *cert == dir+domain+"/fullchain.pem" && *key == dir+domain+"/privkey.pem" {
			return true
		}
	}
	return false
}

// restorePoolIni writes a backup pool's ini overrides onto poolID, the pool
// that stands for it on this server. A directive the pool already sets keeps
// its value: restore adds settings, it doesn't overwrite them.
func restorePoolIni(ctx context.Context, d Deps, r *ApplyResult, poolID string, overrides []internalbackup.MetadataPHPPoolIniOverride) {
	if d.PHPPoolIni == nil || len(overrides) == 0 {
		return
	}
	current, err := d.PHPPoolIni.ListByPool(ctx, poolID)
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("php_pool %s: list ini overrides: %v", poolID, err))
		return
	}
	set := make(map[string]bool, len(current))
	for _, o := range current {
		set[o.Directive] = true
	}
	now := time.Now().UTC()
	for _, o := range overrides {
		if set[o.Directive] {
			r.Skipped++
			continue
		}
		ov := &models.PHPPoolIniOverride{
			ID:        o.ID,
			PoolID:    poolID,
			Directive: o.Directive,
			Value:     o.Value,
			Kind:      o.Kind,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := d.PHPPoolIni.Create(ctx, ov); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("php_pool_ini %s: create: %v", o.ID, err))
			continue
		}
		set[o.Directive] = true
		r.PHPPoolIni++
	}
}

// restoreAccountUsername is the username of the account being restored: this
// server's row when it has one, else the bundle's.
func restoreAccountUsername(ctx context.Context, m *internalbackup.AccountMetadata, d Deps) string {
	if users := d.users(); users != nil {
		if u, err := users.FindByID(ctx, m.User.ID); err == nil && u != nil && u.Username != nil && *u.Username != "" {
			return *u.Username
		}
	}
	if m.User.Username != nil {
		return *m.User.Username
	}
	return ""
}

// accountNameRe is a panel username (userops' rule); it can't be "." or "..".
var accountNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// pathWithin reports whether the absolute path p, cleaned, is base/account or
// inside it. An account name that isn't a panel username admits nothing.
func pathWithin(p, base, account string) bool {
	if !accountNameRe.MatchString(account) || !filepath.IsAbs(p) {
		return false
	}
	root := base + "/" + account
	c := filepath.Clean(p)
	return c == root || strings.HasPrefix(c, root+"/")
}

// rehomePath moves p from base/from to base/to when it is that directory or
// inside it: the backup names the account's paths under the username it had
// where the backup was made. Any other path comes back unchanged for the
// caller's own check.
func rehomePath(p, base, from, to string) string {
	if from == to || !accountNameRe.MatchString(from) || !accountNameRe.MatchString(to) || !filepath.IsAbs(p) {
		return p
	}
	old := base + "/" + from
	c := filepath.Clean(p)
	if c == old || strings.HasPrefix(c, old+"/") {
		return base + "/" + to + c[len(old):]
	}
	return p
}

// users is the user repo accessor on Deps. Builder Deps doesn't carry
// it (Build is given the user as a parameter); Apply needs read+write
// for upsert. Stored as a separate field on a new ApplyDeps would
// require duplicating the struct, so add it as a nullable lookup
// here and have callers extend Deps via the Users field below.
func (d Deps) users() repository.UserRepository { return d.Users }

// parseMetaTime parses a timeRFC ("2006-01-02T15:04:05Z") string back to
// a *time.Time, returning nil for empty or unparseable input.
func parseMetaTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
