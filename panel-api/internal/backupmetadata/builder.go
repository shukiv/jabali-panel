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
	DockerApps     repository.DockerAppRepository
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
	FtpAccounts    repository.FtpAccountRepository
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

// metadataDockerRow maps a docker_apps row (+ its published ports) to the
// backup metadata shape. serverLevel is threaded from the caller — it is
// NOT derivable from the row here (a.UserID) alone in a way Apply can trust,
// so we record it explicitly.
func metadataDockerRow(a *models.DockerApp, serverLevel bool, ports []*models.DockerAppPublishedPort) internalbackup.MetadataDockerApp {
	row := internalbackup.MetadataDockerApp{
		ID: a.ID, Slug: a.Slug, InstanceSlug: a.InstanceSlug,
		Name: a.Name, CatalogVersion: a.CatalogVersion,
		ImageSHA: a.ImageSHA, Status: a.Status, UpdateMode: a.UpdateMode,
		CPULimit: a.CPULimit, MemoryLimit: a.MemoryLimit, PIDsLimit: a.PIDsLimit,
		CreatedAt: timeRFC(a.CreatedAt), ServerLevel: serverLevel,
	}
	for _, p := range ports {
		row.Ports = append(row.Ports, internalbackup.MetadataDockerAppPort{
			ID: p.ID, PortName: p.PortName, ContainerPort: p.ContainerPort,
			BindInterface: p.BindInterface, HostPort: p.HostPort,
			Protocol: p.Protocol, ReverseProxy: p.ReverseProxy, Enabled: p.Enabled,
		})
	}
	return row
}

// serverLevelDockerApps returns the live (non-deleted) admin / server-level
// docker apps — UserID NULL (M48). Derived from ListAll + filter rather than
// a new repo method so the DockerAppRepository interface (and its mocks)
// stays put. Errors warn + yield nothing, matching the tenant path.
func (d Deps) serverLevelDockerApps(ctx context.Context) []*models.DockerApp {
	all, err := d.DockerApps.ListAll(ctx)
	if err != nil {
		d.warn("metadata: list all docker apps (server-level)", err)
		return nil
	}
	out := make([]*models.DockerApp, 0, len(all))
	for _, a := range all {
		if a == nil || a.UserID != nil || a.Status == models.DockerAppStatusDeleted {
			continue
		}
		out = append(out, a)
	}
	return out
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
		// JAB-374: batch grants for all db-users in ONE query, grouped by
		// database_user_id, instead of a query per db-user. Same rows, same
		// per-user order (both are unordered → InnoDB PK order).
		grantsByUser := map[string][]models.DatabaseUserGrant{}
		if d.DatabaseGrants != nil && len(users) > 0 {
			ids := make([]string, len(users))
			for i, du := range users {
				ids[i] = du.ID
			}
			grants, gerr := d.DatabaseGrants.ListByDatabaseUserIDs(ctx, ids)
			if gerr != nil {
				d.warn("metadata: list grants (batch)", gerr, "user_id", user.ID)
			}
			for _, g := range grants {
				grantsByUser[g.DatabaseUserID] = append(grantsByUser[g.DatabaseUserID], g)
			}
		}
		for _, du := range users {
			row := internalbackup.MetadataDatabaseUser{
				ID:           du.ID,
				Username:     du.Username,
				PasswordHash: du.PasswordHash,
				CreatedAt:    timeRFC(du.CreatedAt),
			}
			for _, g := range grantsByUser[du.ID] {
				row.Grants = append(row.Grants, internalbackup.MetadataDatabaseUserGrant{
					ID: g.ID, DatabaseID: g.DatabaseID,
					DatabaseName: dbName[g.DatabaseID],
					GrantLevel:   g.GrantLevel, Privileges: g.Privileges,
					CreatedAt: timeRFC(g.CreatedAt),
				})
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
				Status:                    pool.Status,
				CreatedAt:                 timeRFC(pool.CreatedAt),
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

		// JAB-374: batch every per-domain / per-mailbox / per-zone association
		// in a fixed number of queries (bounded by resource TYPE, not row
		// count), grouped in memory by parent id. Order within each parent
		// matches the old single-parent query (the batch methods ORDER BY
		// parent_id, <the sibling's key>), so the emitted bundle is identical.
		domIDs := make([]string, len(domains))
		for i, dom := range domains {
			domIDs[i] = dom.ID
		}

		certByDomain := map[string]*models.SSLCertificate{}
		if d.SSLCerts != nil && len(domIDs) > 0 {
			certs, cerr := d.SSLCerts.FindByDomainIDs(ctx, domIDs)
			if cerr != nil {
				d.warn("metadata: list ssl certs (batch)", cerr, "user_id", user.ID)
			}
			for i := range certs {
				if _, ok := certByDomain[certs[i].DomainID]; !ok {
					certByDomain[certs[i].DomainID] = &certs[i]
				}
			}
		}

		mailboxesByDomain := map[string][]models.Mailbox{}
		var allMailboxIDs []string
		if d.Mailboxes != nil && len(domIDs) > 0 {
			mbs, merr := d.Mailboxes.ListByDomainIDs(ctx, domIDs)
			if merr != nil {
				d.warn("metadata: list mailboxes (batch)", merr, "user_id", user.ID)
			}
			for _, mb := range mbs {
				mailboxesByDomain[mb.DomainID] = append(mailboxesByDomain[mb.DomainID], mb)
				allMailboxIDs = append(allMailboxIDs, mb.ID)
			}
		}

		autoByMailbox := map[string]*models.EmailAutoresponder{}
		if d.Autoresponders != nil && len(allMailboxIDs) > 0 {
			autos, aerr := d.Autoresponders.ListByMailboxIDs(ctx, allMailboxIDs)
			if aerr != nil {
				d.warn("metadata: list autoresponders (batch)", aerr, "user_id", user.ID)
			}
			for i := range autos {
				if _, ok := autoByMailbox[autos[i].MailboxID]; !ok {
					autoByMailbox[autos[i].MailboxID] = &autos[i]
				}
			}
		}

		sharesByMailbox := map[string][]models.MailboxShare{}
		if d.MailboxShares != nil {
			// No Limit: the old per-mailbox FindByOwnerID capped 1000/mailbox; an
			// account-wide cap here could truncate a large account BELOW that, so we
			// leave shares unlimited (opts.Limit 0). Shares are rare per account.
			shares, _, serr := d.MailboxShares.ListByUserID(ctx, user.ID, repository.ListOptions{})
			if serr != nil {
				d.warn("metadata: list mailbox shares (batch)", serr, "user_id", user.ID)
			}
			for _, sh := range shares {
				sharesByMailbox[sh.OwnerMailboxID] = append(sharesByMailbox[sh.OwnerMailboxID], sh)
			}
		}

		fwdByDomain := map[string][]models.EmailForwarder{}
		if d.Forwarders != nil && len(domIDs) > 0 {
			fwds, ferr := d.Forwarders.ListByDomainIDs(ctx, domIDs)
			if ferr != nil {
				d.warn("metadata: list forwarders (batch)", ferr, "user_id", user.ID)
			}
			for _, fw := range fwds {
				fwdByDomain[fw.DomainID] = append(fwdByDomain[fw.DomainID], fw)
			}
		}

		dnssecByDomain := map[string][]models.DomainDNSSECKey{}
		if d.DNSSECKeys != nil && len(domIDs) > 0 {
			keys, kerr := d.DNSSECKeys.ListByDomainIDs(ctx, domIDs)
			if kerr != nil {
				d.warn("metadata: list dnssec keys (batch)", kerr, "user_id", user.ID)
			}
			for _, k := range keys {
				dnssecByDomain[k.DomainID] = append(dnssecByDomain[k.DomainID], k)
			}
		}

		zoneByDomain := map[string]*models.DNSZone{}
		var allZoneIDs []string
		if d.DNSZones != nil && len(domIDs) > 0 {
			zones, zerr := d.DNSZones.FindByDomainIDs(ctx, domIDs)
			if zerr != nil {
				d.warn("metadata: list dns zones (batch)", zerr, "user_id", user.ID)
			}
			for i := range zones {
				if _, ok := zoneByDomain[zones[i].DomainID]; !ok {
					zoneByDomain[zones[i].DomainID] = &zones[i]
					allZoneIDs = append(allZoneIDs, zones[i].ID)
				}
			}
		}

		recordsByZone := map[string][]models.DNSRecord{}
		if d.DNSRecords != nil && len(allZoneIDs) > 0 {
			recs, rerr := d.DNSRecords.ListByZoneIDs(ctx, allZoneIDs)
			if rerr != nil {
				d.warn("metadata: list dns records (batch)", rerr, "user_id", user.ID)
			}
			for _, rec := range recs {
				recordsByZone[rec.ZoneID] = append(recordsByZone[rec.ZoneID], rec)
			}
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
			if cert := certByDomain[dom.ID]; cert != nil {
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
			for _, mb := range mailboxesByDomain[dom.ID] {
				mbRow := internalbackup.MetadataMailbox{
					ID: mb.ID, LocalPart: mb.LocalPart, EmailCached: mb.EmailCached,
					PasswordHash: mb.PasswordHash, PasswordEnc: mb.PasswordEnc,
					QuotaBytes: mb.QuotaBytes, IsDisabled: mb.IsDisabled,
					CreatedAt: timeRFC(mb.CreatedAt),
				}
				if auto := autoByMailbox[mb.ID]; auto != nil {
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
				for _, sh := range sharesByMailbox[mb.ID] {
					rights, _ := json.Marshal(sh.Rights)
					mbRow.SharedWith = append(mbRow.SharedWith,
						internalbackup.MetadataMailboxShare{
							ID: sh.ID, SharedWithMailboxID: sh.SharedWithMailboxID,
							Rights:    string(rights),
							CreatedAt: timeRFC(sh.CreatedAt),
						})
				}
				dRow.Mailboxes = append(dRow.Mailboxes, mbRow)
			}
			for _, fw := range fwdByDomain[dom.ID] {
				dRow.Forwarders = append(dRow.Forwarders, internalbackup.MetadataForwarder{
					ID: fw.ID, MailboxID: fw.MailboxID, Type: fw.Type,
					LocalPart: fw.LocalPart, Target: fw.Target, Enabled: fw.Enabled,
					CreatedAt: timeRFC(fw.CreatedAt),
				})
			}
			for _, k := range dnssecByDomain[dom.ID] {
				dRow.DNSSECKeys = append(dRow.DNSSECKeys, internalbackup.MetadataDNSSECKey{
					KeyTag: k.KeyTag, KeyType: k.KeyType, Algorithm: k.Algorithm,
					PublicKey: k.PublicKey, Active: k.Active,
					ObservedAt: timeRFC(k.ObservedAt),
				})
			}
			// GH #267: capture USER (non-managed) DNS records so restore can
			// re-insert them; managed records re-derive from domain config.
			if zone := zoneByDomain[dom.ID]; zone != nil {
				for _, rec := range recordsByZone[zone.ID] {
					if rec.Managed {
						continue
					}
					dRow.DNSRecords = append(dRow.DNSRecords, internalbackup.MetadataDNSRecord{
						Name: rec.Name, Type: rec.Type, Content: rec.Content,
						TTL: rec.TTL, Priority: rec.Priority, IsEnabled: rec.IsEnabled,
					})
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

	// Docker apps (GH #954): capture the tenant docker-app rows + their
	// published ports so a restore can rebuild the panel row. The DATA tree
	// rides the stage=docker snapshot (#1017); without this row the restored
	// app is orphaned on disk. JAB-374: ports are batch-loaded for all apps
	// (tenant + server-level) in one query, grouped by app id.
	if d.DockerApps != nil {
		apps, err := d.DockerApps.ListByUserID(ctx, user.ID)
		if err != nil {
			d.warn("metadata: list docker apps", err, "user_id", user.ID)
		}
		var serverApps []*models.DockerApp
		if user.IsAdmin {
			// Admin / server-level apps (models.DockerApp.UserID NULL, M48) have
			// no tenant account, so account backups never carried them (GH #1360)
			// — the only cover was a full system backup. Fold them into an admin
			// account's backup so the ordinary per-account restore rebuilds them
			// too. serverLevel=true keeps Apply from re-owning them to the admin.
			serverApps = d.serverLevelDockerApps(ctx)
		}
		appIDs := make([]string, 0, len(apps)+len(serverApps))
		for _, a := range apps {
			appIDs = append(appIDs, a.ID)
		}
		for _, a := range serverApps {
			appIDs = append(appIDs, a.ID)
		}
		portsByApp := map[string][]*models.DockerAppPublishedPort{}
		if len(appIDs) > 0 {
			ports, perr := d.DockerApps.ListPortsForApps(ctx, appIDs)
			if perr != nil {
				d.warn("metadata: list docker app ports (batch)", perr, "user_id", user.ID)
			}
			for _, p := range ports {
				portsByApp[p.AppID] = append(portsByApp[p.AppID], p)
			}
		}
		for _, a := range apps {
			m.DockerApps = append(m.DockerApps, metadataDockerRow(a, false, portsByApp[a.ID]))
		}
		for _, a := range serverApps {
			m.DockerApps = append(m.DockerApps, metadataDockerRow(a, true, portsByApp[a.ID]))
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
	if d.FtpAccounts != nil {
		// GH #1361: capture the FTP/SFTP subaccount rows. PasswordShadow is
		// left empty here — the panel can't read /etc/shadow; the agent fills
		// it in enrichFtpCredentials before the metadata snapshot is written.
		accts, ferr := d.FtpAccounts.ListByUserID(ctx, user.ID)
		if ferr != nil {
			d.warn("metadata: list ftp accounts", ferr, "user_id", user.ID)
		}
		for _, a := range accts {
			m.FtpAccounts = append(m.FtpAccounts, internalbackup.MetadataFtpAccount{
				ID: a.ID, Username: a.Username, HomePath: a.HomePath,
				FTPAccess: a.FTPAccess, SFTPAccess: a.SFTPAccess, WebDAVAccess: a.WebDAVAccess,
				IsEnabled: a.IsEnabled, UID: a.UID, Isolated: a.Isolated,
				QuotaMB: a.QuotaMB, JailPath: a.JailPath,
				CreatedAt: timeRFC(a.CreatedAt),
			})
		}
	}
	if d.LimitOverrides != nil {
		// User-scoped lookup, not ListAll()+in-memory filter (JAB-374 AC#5).
		if lo, err := d.LimitOverrides.FindByUserID(ctx, user.ID); err == nil && lo != nil {
			m.LimitOverride = &internalbackup.MetadataLimitOverride{
				DiskQuotaMB: lo.DiskQuotaMB, CPUQuotaPercent: lo.CPUQuotaPercent,
				MemoryLimitMB: lo.MemoryLimitMB,
				IOReadMbps:    lo.IOReadMbps, IOWriteMbps: lo.IOWriteMbps,
				MaxTasks: lo.MaxTasks,
			}
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
