// Package backupmetadata is the shared producer that builds the
// schema-v2 AccountMetadata bundle (see internal/backup/metadata.go).
// Both the admin /backups handlers and the in-process backup-scheduler
// invoke Build to populate the per-user state blob handed to the
// agent's stage=metadata writer. Centralising the producer here
// prevents the two call sites from drifting apart on schema changes.
package backupmetadata

import (
	"context"
	"encoding/json"
	"log/slog"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// KratosClient defines the interface for Kratos admin operations needed
// during user restore.
type KratosClient interface {
	CreateIdentityWithPassword(ctx context.Context, traits kratosclient.AdminTraits, passwordHash string) (string, error)
	ImportIdentities(ctx context.Context, identities []kratosclient.ExportedIdentity) error
	// IdentityIDByEmail + IdentityHasPassword back the login-verification
	// step (GH #954): after restore, resolve whether the account can
	// actually sign in, and mint a recoverable identity when none exists.
	IdentityIDByEmail(ctx context.Context, email string) (string, error)
	IdentityHasPassword(ctx context.Context, identityID string) (bool, error)
}

// Deps is the union of repos the producer reads. Every field is
// optional; missing repos log + skip the corresponding section.
type Deps struct {
	// Users is the user repo. Build doesn't read this (it gets the
	// user as a parameter), but Apply needs it for upsert on
	// disaster recovery.
	Users          repository.UserRepository
	Databases      repository.DatabaseRepository
	DatabaseUsers  repository.DatabaseUserRepository
	DatabaseGrants repository.DatabaseUserGrantRepository
	Domains        repository.DomainRepository
	Mailboxes      repository.MailboxRepository
	AppInstalls    repository.ApplicationInstallRepository
	SSLCerts       repository.SSLCertificateRepository
	PHPPools       repository.PHPPoolRepository
	PHPPoolIni     repository.PHPPoolIniOverrideRepository
	Forwarders     repository.EmailForwarderRepository
	Autoresponders repository.EmailAutoresponderRepository
	MailboxShares  repository.MailboxShareRepository
	DNSSECKeys     repository.DNSSECKeyRepository
	DNSZones       repository.DNSZoneRepository
	DNSRecords     repository.DNSRecordRepository
	SSHKeys        repository.SSHKeyRepository
	CronJobs       repository.CronJobRepository
	LimitOverrides repository.UserLimitOverrideRepository
	EgressPolicies repository.UserEgressPolicyRepository
	EgressRequests repository.UserEgressRequestRepository
	KratosClient   KratosClient
	Log            *slog.Logger
}

func (d Deps) warn(msg string, err error, kv ...any) {
	if err == nil || d.Log == nil {
		return
	}
	args := append([]any{"err", err}, kv...)
	d.Log.Warn(msg, args...)
}

// timeRFC formats a time.Time as RFC3339 with seconds precision. Empty
// string for zero values so JSON omits the field via omitempty.
func timeRFC(t interface{ Format(string) string }) string {
	return t.Format("2006-01-02T15:04:05Z")
}

// Build returns the populated AccountMetadata bundle for the given
// user. Non-nil result even on partial failures.
func Build(ctx context.Context, user *models.User, d Deps) *internalbackup.AccountMetadata {
	m := &internalbackup.AccountMetadata{
		SchemaVersion: internalbackup.MetadataSchemaVersion,
		User: internalbackup.MetadataUser{
			ID:                    user.ID,
			Email:                 user.Email,
			Username:              user.Username,
			NameFirst:             user.NameFirst,
			NameLast:              user.NameLast,
			PasswordHash:          user.PasswordHash,
			IsAdmin:               user.IsAdmin,
			PackageID:             user.PackageID,
			LinuxUID:              user.LinuxUID,
			MysqladminUsername:    user.MysqladminUsername,
			MysqladminPasswordEnc: user.MysqladminPasswordEnc,
			KratosIdentityID:      user.KratosIdentityID,
			CreatedAt:             timeRFC(user.CreatedAt),
		},
	}
	if user.MysqladminProvisionedAt != nil {
		m.User.MysqladminProvisionedAt = timeRFC(*user.MysqladminProvisionedAt)
	}

	dbName := map[string]string{}
	if d.Databases != nil {
		dbs, _, err := d.Databases.ListByUserID(ctx, user.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			d.warn("metadata: list databases", err, "user_id", user.ID)
		}
		for _, db := range dbs {
			dbName[db.ID] = db.Name
			m.Databases = append(m.Databases, internalbackup.MetadataDatabase{
				ID: db.ID, Name: db.Name, Engine: db.Engine,
				Charset: db.Charset, Collation: db.Collation,
				CreatedAt: timeRFC(db.CreatedAt),
			})
		}
	}

	if d.DatabaseUsers != nil {
		users, _, err := d.DatabaseUsers.ListByUserID(ctx, user.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			d.warn("metadata: list db users", err, "user_id", user.ID)
		}
		for _, du := range users {
			row := internalbackup.MetadataDatabaseUser{
				ID:           du.ID,
				Username:     du.Username,
				PasswordHash: du.PasswordHash,
				CreatedAt:    timeRFC(du.CreatedAt),
			}
			if d.DatabaseGrants != nil {
				grants, gerr := d.DatabaseGrants.ListByDatabaseUserID(ctx, du.ID)
				if gerr != nil {
					d.warn("metadata: list grants", gerr, "database_user_id", du.ID)
				}
				for _, g := range grants {
					row.Grants = append(row.Grants, internalbackup.MetadataDatabaseUserGrant{
						ID: g.ID, DatabaseID: g.DatabaseID,
						DatabaseName: dbName[g.DatabaseID],
						GrantLevel:   g.GrantLevel, Privileges: g.Privileges,
						CreatedAt: timeRFC(g.CreatedAt),
					})
				}
			}
			m.DatabaseUsers = append(m.DatabaseUsers, row)
		}
	}

	if d.PHPPools != nil {
		if pool, err := d.PHPPools.FindByUserID(ctx, user.ID); err == nil && pool != nil {
			poolRow := internalbackup.MetadataPHPPool{
				ID: pool.ID, PHPVersion: pool.PHPVersion,
				PmMode: pool.PmMode, PmMaxChildren: pool.PmMaxChildren,
				ProcessIdleTimeoutSeconds: pool.ProcessIdleTimeoutSeconds,
				Status:    pool.Status,
				CreatedAt: timeRFC(pool.CreatedAt),
			}
			if d.PHPPoolIni != nil {
				if overrides, ierr := d.PHPPoolIni.ListByPool(ctx, pool.ID); ierr == nil {
					for _, o := range overrides {
						poolRow.IniOverrides = append(poolRow.IniOverrides,
							internalbackup.MetadataPHPPoolIniOverride{
								ID: o.ID, Directive: o.Directive,
								Value: o.Value, Kind: o.Kind,
								CreatedAt: timeRFC(o.CreatedAt),
							})
					}
				}
			}
			m.PHPPools = append(m.PHPPools, poolRow)
		}
	}

	if d.Domains != nil {
		domains, _, err := d.Domains.ListByUserID(ctx, user.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			d.warn("metadata: list domains", err, "user_id", user.ID)
		}
		for _, dom := range domains {
			dRow := internalbackup.MetadataDomain{
				ID: dom.ID, Name: dom.Name, DocRoot: dom.DocRoot,
				IsEnabled: dom.IsEnabled, NginxCustomDirectives: dom.NginxCustomDirectives,
				RedirectAllTo: dom.RedirectAllTo, RedirectAllType: dom.RedirectAllType,
				IndexPriority: dom.IndexPriority, SSLEnabled: dom.SSLEnabled,
				PHPPoolID:            dom.PHPPoolID,
				PHPMemoryLimit:       dom.PHPMemoryLimit,
				PHPUploadMaxFilesize: dom.PHPUploadMaxFilesize,
				PHPPostMaxSize:       dom.PHPPostMaxSize,
				PHPMaxInputVars:      dom.PHPMaxInputVars,
				PHPMaxExecutionTime:  dom.PHPMaxExecutionTime,
				PHPMaxInputTime:      dom.PHPMaxInputTime,
				RateLimitRPS:         dom.RateLimitRPS,
				ConnectionLimit:      dom.ConnectionLimit,
				ListenIPv4ID:         dom.ListenIPv4ID,
				ListenIPv6ID:         dom.ListenIPv6ID,
				EmailEnabled:         dom.EmailEnabled,
				DkimSelector:         dom.DkimSelector,
				DkimPublicKey:        dom.DkimPublicKey,
				IsPanelPrimary:       dom.IsPanelPrimary,
				CatchallTarget:       dom.CatchallTarget,
				DisclaimerEnabled:    dom.DisclaimerEnabled,
				DisclaimerText:       dom.DisclaimerText,
				DNSSECEnabled:        dom.DNSSECEnabled,
				CreatedAt:            timeRFC(dom.CreatedAt),
			}
			if dom.EmailEnabledAt != nil {
				dRow.EmailEnabledAt = timeRFC(*dom.EmailEnabledAt)
			}
			if dom.DNSSECEnabledAt != nil {
				dRow.DNSSECEnabledAt = timeRFC(*dom.DNSSECEnabledAt)
			}
			if pr, err := json.Marshal(dom.PageRedirects); err == nil && string(pr) != "null" {
				dRow.PageRedirects = string(pr)
			}
			if nr, err := json.Marshal(dom.NginxRules); err == nil && string(nr) != "null" {
				dRow.NginxRules = string(nr)
			}
			if d.SSLCerts != nil {
				if cert, err := d.SSLCerts.FindByDomainID(ctx, dom.ID); err == nil && cert != nil {
					sslRow := &internalbackup.MetadataSSLCert{
						ID: cert.ID, Status: cert.Status,
						RenewalCount: cert.RenewalCount, LastError: cert.LastError,
						Staging: cert.Staging, CertPath: cert.CertPath, KeyPath: cert.KeyPath,
						CreatedAt: timeRFC(cert.CreatedAt),
					}
					if cert.IssuedAt != nil {
						sslRow.IssuedAt = timeRFC(*cert.IssuedAt)
					}
					if cert.ExpiresAt != nil {
						sslRow.ExpiresAt = timeRFC(*cert.ExpiresAt)
					}
					if cert.LastRenewedAt != nil {
						sslRow.LastRenewedAt = timeRFC(*cert.LastRenewedAt)
					}
					dRow.SSLCertificate = sslRow
				}
			}
			if d.Mailboxes != nil {
				mboxes, _, _ := d.Mailboxes.ListByDomainID(ctx, dom.ID, repository.ListOptions{Limit: 10000})
				for _, mb := range mboxes {
					mbRow := internalbackup.MetadataMailbox{
						ID: mb.ID, LocalPart: mb.LocalPart, EmailCached: mb.EmailCached,
						PasswordHash: mb.PasswordHash, PasswordEnc: mb.PasswordEnc,
						QuotaBytes: mb.QuotaBytes, IsDisabled: mb.IsDisabled,
						CreatedAt: timeRFC(mb.CreatedAt),
					}
					if d.Autoresponders != nil {
						if auto, err := d.Autoresponders.FindByMailboxID(ctx, mb.ID); err == nil && auto != nil {
							ar := &internalbackup.MetadataAutoresponder{
								Enabled: auto.Enabled, Subject: auto.Subject,
								TextBody: auto.TextBody, HTMLBody: auto.HTMLBody,
							}
							if auto.FromDate != nil {
								ar.FromDate = timeRFC(*auto.FromDate)
							}
							if auto.ToDate != nil {
								ar.ToDate = timeRFC(*auto.ToDate)
							}
							mbRow.Autoresponder = ar
						}
					}
					if d.MailboxShares != nil {
						shares, _, _ := d.MailboxShares.FindByOwnerID(ctx, mb.ID, repository.ListOptions{Limit: 1000})
						for _, sh := range shares {
							rights, _ := json.Marshal(sh.Rights)
							mbRow.SharedWith = append(mbRow.SharedWith,
								internalbackup.MetadataMailboxShare{
									ID: sh.ID, SharedWithMailboxID: sh.SharedWithMailboxID,
									Rights: string(rights),
									CreatedAt: timeRFC(sh.CreatedAt),
								})
						}
					}
					dRow.Mailboxes = append(dRow.Mailboxes, mbRow)
				}
			}
			if d.Forwarders != nil {
				fwds, _, _ := d.Forwarders.ListByDomainID(ctx, dom.ID, repository.ListOptions{Limit: 10000})
				for _, fw := range fwds {
					dRow.Forwarders = append(dRow.Forwarders, internalbackup.MetadataForwarder{
						ID: fw.ID, MailboxID: fw.MailboxID, Type: fw.Type,
						LocalPart: fw.LocalPart, Target: fw.Target, Enabled: fw.Enabled,
						CreatedAt: timeRFC(fw.CreatedAt),
					})
				}
			}
			if d.DNSSECKeys != nil {
				keys, _ := d.DNSSECKeys.ListByDomainID(ctx, dom.ID)
				for _, k := range keys {
					dRow.DNSSECKeys = append(dRow.DNSSECKeys, internalbackup.MetadataDNSSECKey{
						KeyTag: k.KeyTag, KeyType: k.KeyType, Algorithm: k.Algorithm,
						PublicKey: k.PublicKey, Active: k.Active,
						ObservedAt: timeRFC(k.ObservedAt),
					})
				}
			}
			// GH #267: capture USER (non-managed) DNS records so restore can
			// re-insert them; managed records re-derive from domain config.
			if d.DNSZones != nil && d.DNSRecords != nil {
				if zone, zerr := d.DNSZones.FindByDomainID(ctx, dom.ID); zerr == nil && zone != nil {
					if recs, rerr := d.DNSRecords.ListByZoneID(ctx, zone.ID); rerr == nil {
						for _, rec := range recs {
							if rec.Managed {
								continue
							}
							dRow.DNSRecords = append(dRow.DNSRecords, internalbackup.MetadataDNSRecord{
								Name: rec.Name, Type: rec.Type, Content: rec.Content,
								TTL: rec.TTL, Priority: rec.Priority, IsEnabled: rec.IsEnabled,
							})
						}
					}
				}
			}
			m.Domains = append(m.Domains, dRow)
		}
	}

	if d.AppInstalls != nil {
		installs, _, err := d.AppInstalls.ListByUserID(ctx, user.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			d.warn("metadata: list app installs", err, "user_id", user.ID)
		}
		for _, ai := range installs {
			m.AppInstalls = append(m.AppInstalls, internalbackup.MetadataAppInstall{
				ID: ai.ID, DomainID: ai.DomainID, DBID: ai.DBID,
				Version: ai.Version, AdminUsername: ai.AdminUsername,
				AdminEmail: ai.AdminEmail, Locale: ai.Locale,
				UseWWW: ai.UseWWW, Subdirectory: ai.Subdirectory,
				Status: ai.Status, AppType: ai.AppType,
				CreatedAt: timeRFC(ai.CreatedAt),
			})
		}
	}

	if d.SSHKeys != nil {
		keys, _ := d.SSHKeys.ListByUserID(ctx, user.ID)
		for _, k := range keys {
			m.SSHKeys = append(m.SSHKeys, internalbackup.MetadataSSHKey{
				ID: k.ID, Name: k.Name, PublicKey: k.PublicKey,
				Fingerprint: k.Fingerprint,
				CreatedAt:   timeRFC(k.CreatedAt),
			})
		}
	}
	if d.CronJobs != nil {
		jobs, _ := d.CronJobs.ListByUserID(ctx, user.ID)
		for _, j := range jobs {
			m.CronJobs = append(m.CronJobs, internalbackup.MetadataCronJob{
				ID: j.ID, Name: j.Name, Command: j.Command, Schedule: j.Schedule,
				Enabled:   j.Enabled,
				CreatedAt: timeRFC(j.CreatedAt),
			})
		}
	}
	if d.LimitOverrides != nil {
		all, _ := d.LimitOverrides.ListAll(ctx)
		for _, lo := range all {
			if lo.UserID != user.ID {
				continue
			}
			m.LimitOverride = &internalbackup.MetadataLimitOverride{
				DiskQuotaMB: lo.DiskQuotaMB, CPUQuotaPercent: lo.CPUQuotaPercent,
				MemoryLimitMB: lo.MemoryLimitMB,
				IOReadMbps:    lo.IOReadMbps, IOWriteMbps: lo.IOWriteMbps,
				MaxTasks:      lo.MaxTasks,
			}
			break
		}
	}
	if d.EgressPolicies != nil {
		if pol, err := d.EgressPolicies.Get(ctx, user.ID); err == nil && pol != nil {
			ep := &internalbackup.MetadataEgressPolicy{
				State: pol.State, AllowedExtra: string(pol.AllowedExtra),
			}
			if pol.LearningStartedAt != nil {
				ep.LearningStartedAt = timeRFC(*pol.LearningStartedAt)
			}
			if pol.UpdatedBy != nil {
				ep.UpdatedBy = *pol.UpdatedBy
			}
			m.EgressPolicy = ep
		}
	}
	if d.EgressRequests != nil {
		reqs, _ := d.EgressRequests.ListByUser(ctx, user.ID)
		for _, r := range reqs {
			er := internalbackup.MetadataEgressRequest{
				ID: r.ID, CIDR: r.CIDR, Port: r.Port, Protocol: r.Protocol,
				Reason: r.Reason, Status: r.Status,
				CreatedAt: timeRFC(r.CreatedAt),
			}
			if r.ReviewedBy != nil {
				er.ReviewedBy = *r.ReviewedBy
			}
			if r.DecidedAt != nil {
				er.DecidedAt = timeRFC(*r.DecidedAt)
			}
			m.EgressRequests = append(m.EgressRequests, er)
		}
	}
	return m
}
