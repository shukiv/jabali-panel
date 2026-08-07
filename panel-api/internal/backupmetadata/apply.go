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
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

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
	SSHKeys        int
	CronJobs       int
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
	if d.PHPPools != nil {
		for _, p := range m.PHPPools {
			if existing, err := d.PHPPools.FindByID(ctx, p.ID); err == nil && existing != nil {
				r.Skipped++
				continue
			} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
				r.Errors = append(r.Errors, fmt.Sprintf("php_pool %s: lookup: %v", p.ID, err))
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
			r.PHPPools++
			if d.PHPPoolIni != nil {
				for _, o := range p.IniOverrides {
					ov := &models.PHPPoolIniOverride{
						ID:        o.ID,
						PoolID:    p.ID,
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
					r.PHPPoolIni++
				}
			}
		}
	}

	// 3) Domains + SSL certs.
	if d.Domains != nil {
		for _, dm := range m.Domains {
			if existing, err := d.Domains.FindByID(ctx, dm.ID); err == nil && existing != nil {
				r.Skipped++
				continue
			} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
				r.Errors = append(r.Errors, fmt.Sprintf("domain %s: lookup: %v", dm.ID, err))
				continue
			}
			row := &models.Domain{
				ID:                    dm.ID,
				UserID:                m.User.ID,
				Name:                  dm.Name,
				DocRoot:               dm.DocRoot,
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
			if err := d.Domains.Create(ctx, row); err != nil {
				r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): create: %v", dm.ID, dm.Name, err))
				continue
			}
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
		if d.Mailboxes != nil {
			for _, mb := range dm.Mailboxes {
				if existing, err := d.Mailboxes.FindByID(ctx, mb.ID); err == nil && existing != nil {
					r.Skipped++
				} else {
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
			for _, fw := range dm.Forwarders {
				if existing, err := d.Forwarders.FindByID(ctx, fw.ID); err == nil && existing != nil {
					r.Skipped++
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
			}
		}
	}

	// 4) Databases + db_users + grants.
	if d.Databases != nil {
		dbNameToID := make(map[string]string, len(m.Databases))
		for _, db := range m.Databases {
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
					r.Skipped++
					continue
				}
				r.Errors = append(r.Errors, fmt.Sprintf("database %s (%s): create: %v", db.ID, db.Name, err))
				continue
			}
			r.Databases++
		}
		if d.DatabaseUsers != nil {
			for _, du := range m.DatabaseUsers {
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

	// 6) SSH keys.
	if d.SSHKeys != nil {
		for _, k := range m.SSHKeys {
			row := &models.SSHKey{
				ID:          k.ID,
				UserID:      m.User.ID,
				Name:        k.Name,
				PublicKey:   k.PublicKey,
				Fingerprint: k.Fingerprint,
				CreatedAt:   now,
			}
			if err := d.SSHKeys.Create(ctx, row); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					r.Skipped++
					continue
				}
				r.Errors = append(r.Errors, fmt.Sprintf("ssh_key %s: create: %v", k.ID, err))
				continue
			}
			r.SSHKeys++
		}
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
	if d.KratosClient != nil && m.User.Email != "" {
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
