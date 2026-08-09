// Per-user metadata bundle written as a sidecar restic snapshot at
// stage=metadata. Captures everything that lives in panel-api MariaDB
// rows but isn't naturally part of /home, dumps, or mail data — e.g.
// the user's database users + grants, application install rows, ssh
// keys. Restore reads this back to recreate panel-side state at the
// same time the data stages get re-imported.
//
// This file lives in internal/backup so both panel-api (writer) and
// panel-agent (also writer for system-side counterpart) can share the
// schema. The producer side does the heavy lifting (panel-api sends
// the bundle in backup.create params); the agent only needs to know
// the JSON shape so it can write the snapshot.
package backup

// AccountMetadata is the JSON shape captured at stage=metadata for one
// account_backup. SchemaVersion + ProducerPanelSHA let the restore-side
// reject bundles produced by an incompatible panel.
//
// Schema v2 (M30.1+) carries every per-user table the panel touches so
// disaster recovery can rebuild a user's full state when the source's
// system_backup panel_db dump is missing the rows (e.g. user was
// deleted between account-backup time and system-backup time, or the
// operator is restoring per-user accounts onto a different host).
type AccountMetadata struct {
	SchemaVersion    int    `json:"schema_version"`
	ProducerPanelSHA string `json:"producer_panel_sha,omitempty"`

	// User core (jabali_panel.users) — full row except mysqladmin
	// password ciphertext is encoded base64 because varbinary doesn't
	// round-trip through JSON cleanly.
	User MetadataUser `json:"user"`

	// Kratos identity + credentials. Optional (may be absent on rows
	// created before M20 backfill landed) but normally populated.
	Kratos *MetadataKratos `json:"kratos,omitempty"`

	// Per-domain rows. The Domains slice itself includes everything that
	// hangs off a domain (SSL cert, mailboxes, forwarders, etc.) so the
	// restore-side can walk a single tree and apply rows in dependency
	// order (domain → mailbox → forwarder/autoresponder).
	Domains []MetadataDomain `json:"domains,omitempty"`

	// PHP pools + ini overrides (per user, not per domain — domains
	// reference pools by id).
	PHPPools []MetadataPHPPool `json:"php_pools,omitempty"`

	// MariaDB databases + their grant matrix.
	Databases     []MetadataDatabase     `json:"databases,omitempty"`
	DatabaseUsers []MetadataDatabaseUser `json:"database_users,omitempty"`

	// Application installs (WordPress, Joomla, Drupal, …).
	AppInstalls []MetadataAppInstall `json:"app_installs,omitempty"`

	// DockerApps are the account's tenant docker-app rows (GH #954). Their
	// data trees ride the stage=docker snapshot (#1017); these rows make the
	// restored apps panel-managed rather than orphaned.
	DockerApps []MetadataDockerApp `json:"docker_apps,omitempty"`

	// SSH keys + cron jobs hang off the user, not a domain.
	SSHKeys  []MetadataSSHKey  `json:"ssh_keys,omitempty"`
	CronJobs []MetadataCronJob `json:"cron_jobs,omitempty"`

	// User-level egress + limit overrides (M18 / M34).
	EgressPolicy   *MetadataEgressPolicy   `json:"egress_policy,omitempty"`
	EgressRequests []MetadataEgressRequest `json:"egress_requests,omitempty"`
	LimitOverride  *MetadataLimitOverride  `json:"limit_override,omitempty"`
}

// MetadataUser mirrors models.User. Pointers preserve NULL semantics
// for nullable columns so the restore-side INSERT writes the right
// thing back into MariaDB.
type MetadataUser struct {
	ID                      string  `json:"id"`
	Email                   string  `json:"email"`
	Username                *string `json:"username,omitempty"`
	NameFirst               string  `json:"name_first"`
	NameLast                string  `json:"name_last"`
	PasswordHash            string  `json:"password_hash,omitempty"`
	IsAdmin                 bool    `json:"is_admin"`
	PackageID               *string `json:"package_id,omitempty"`
	LinuxUID                *uint32 `json:"linux_uid,omitempty"`
	MysqladminUsername      *string `json:"mysqladmin_username,omitempty"`
	MysqladminPasswordEnc   []byte  `json:"mysqladmin_password_enc,omitempty"`
	MysqladminProvisionedAt string  `json:"mysqladmin_provisioned_at,omitempty"`
	KratosIdentityID        *string `json:"kratos_identity_id,omitempty"`
	CreatedAt               string  `json:"created_at,omitempty"`
}

// MetadataKratos carries the raw exported identity data from Kratos export API.
// This replaces manual database extraction with official export/import functionality.
type MetadataKratos struct {
	// Raw exported identity data from Kratos export API, stored as JSON
	ExportedIdentity string `json:"exported_identity"`
}

// MetadataDomain mirrors models.Domain plus the per-domain children
// (SSL cert, mailboxes, forwarders, autoresponders, mailbox shares,
// DNSSEC keys). Restore-side iterates in this nested order so FK
// dependencies are satisfied.
type MetadataDomain struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	DocRoot               string  `json:"doc_root"`
	IsEnabled             bool    `json:"is_enabled"`
	NginxCustomDirectives *string `json:"nginx_custom_directives,omitempty"`
	RedirectAllTo         *string `json:"redirect_all_to,omitempty"`
	RedirectAllType       *string `json:"redirect_all_type,omitempty"`
	PageRedirects         string  `json:"page_redirects,omitempty"` // raw JSON
	NginxRules            string  `json:"nginx_rules,omitempty"`    // raw JSON
	IndexPriority         string  `json:"index_priority"`
	SSLEnabled            bool    `json:"ssl_enabled"`
	PHPPoolID             *string `json:"php_pool_id,omitempty"`
	PHPMemoryLimit        *string `json:"php_memory_limit,omitempty"`
	PHPUploadMaxFilesize  *string `json:"php_upload_max_filesize,omitempty"`
	PHPPostMaxSize        *string `json:"php_post_max_size,omitempty"`
	PHPMaxInputVars       *int    `json:"php_max_input_vars,omitempty"`
	PHPMaxExecutionTime   *int    `json:"php_max_execution_time,omitempty"`
	PHPMaxInputTime       *int    `json:"php_max_input_time,omitempty"`
	RateLimitRPS          uint32  `json:"rate_limit_rps"`
	ConnectionLimit       uint32  `json:"connection_limit"`
	ListenIPv4ID          *uint64 `json:"listen_ipv4_id,omitempty"`
	ListenIPv6ID          *uint64 `json:"listen_ipv6_id,omitempty"`
	EmailEnabled          bool    `json:"email_enabled"`
	DkimSelector          *string `json:"dkim_selector,omitempty"`
	DkimPublicKey         *string `json:"dkim_public_key,omitempty"`
	EmailEnabledAt        string  `json:"email_enabled_at,omitempty"`
	IsPanelPrimary        bool    `json:"is_panel_primary"`
	CatchallTarget        *string `json:"catchall_target,omitempty"`
	DisclaimerEnabled     bool    `json:"disclaimer_enabled"`
	DisclaimerText        *string `json:"disclaimer_text,omitempty"`
	DNSSECEnabled         bool    `json:"dnssec_enabled"`
	DNSSECEnabledAt       string  `json:"dnssec_enabled_at,omitempty"`
	CreatedAt             string  `json:"created_at,omitempty"`

	SSLCertificate *MetadataSSLCert    `json:"ssl_certificate,omitempty"`
	Mailboxes      []MetadataMailbox   `json:"mailboxes,omitempty"`
	Forwarders     []MetadataForwarder `json:"forwarders,omitempty"`
	// DNSRecords (GH #267) captures the user-created DNS rows so a restore can
	// re-insert them. Managed records (BootstrapRecords + mail) are excluded —
	// they re-derive from domain config via the reconciler.
	DNSRecords []MetadataDNSRecord `json:"dns_records,omitempty"`
	DNSSECKeys []MetadataDNSSECKey `json:"dnssec_keys,omitempty"`
}

// MetadataSSLCert mirrors models.SSLCertificate (sans large per-cert
// content — the cert + key are on disk under /etc/letsencrypt and the
// system-backup tls stage already captures them).
type MetadataSSLCert struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	IssuedAt      string  `json:"issued_at,omitempty"`
	ExpiresAt     string  `json:"expires_at,omitempty"`
	RenewalCount  int     `json:"renewal_count"`
	LastRenewedAt string  `json:"last_renewed_at,omitempty"`
	LastError     *string `json:"last_error,omitempty"`
	Staging       bool    `json:"staging"`
	CertPath      *string `json:"cert_path,omitempty"`
	KeyPath       *string `json:"key_path,omitempty"`
	CreatedAt     string  `json:"created_at,omitempty"`
}

// MetadataPHPPool mirrors models.PHPPool plus its ini overrides.
type MetadataPHPPool struct {
	ID                        string                       `json:"id"`
	PHPVersion                string                       `json:"php_version"`
	PmMode                    string                       `json:"pm_mode"`
	PmMaxChildren             uint32                       `json:"pm_max_children"`
	ProcessIdleTimeoutSeconds uint32                       `json:"process_idle_timeout_seconds"`
	Status                    string                       `json:"status"`
	CreatedAt                 string                       `json:"created_at,omitempty"`
	IniOverrides              []MetadataPHPPoolIniOverride `json:"ini_overrides,omitempty"`
}

// MetadataPHPPoolIniOverride mirrors models.PHPPoolIniOverride.
type MetadataPHPPoolIniOverride struct {
	ID        string `json:"id"`
	Directive string `json:"directive"`
	Value     string `json:"value"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"created_at,omitempty"`
}

// MetadataDatabase mirrors models.Database.
type MetadataDatabase struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Engine    string `json:"engine"`
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
	CreatedAt string `json:"created_at,omitempty"`
}

// MetadataDatabaseUser mirrors models.DatabaseUser plus its grants
// (one DBUser → many DatabaseIDs). Restore preserves password_hash so
// the user's existing MariaDB credentials keep working.
type MetadataDatabaseUser struct {
	ID           string                      `json:"id"`
	Username     string                      `json:"username"`
	PasswordHash string                      `json:"password_hash,omitempty"`
	CreatedAt    string                      `json:"created_at,omitempty"`
	Grants       []MetadataDatabaseUserGrant `json:"grants,omitempty"`
}

// MetadataDatabaseUserGrant pairs a database user with the database it
// can access at the recorded grant level. DatabaseID is the panel-side
// id (jabali_panel.databases.id); DatabaseName is captured for display
// + cross-checking when the source schema didn't preserve the id.
type MetadataDatabaseUserGrant struct {
	ID           string `json:"id,omitempty"`
	DatabaseID   string `json:"database_id"`
	DatabaseName string `json:"database_name,omitempty"`
	GrantLevel   string `json:"grant_level"`
	Privileges   string `json:"privileges,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// MetadataMailbox mirrors models.Mailbox plus its attached
// autoresponder + share rows. PasswordEnc carries the AES-256-GCM
// blob used by the webmail SSO flow — round-tripped as base64 in JSON.
type MetadataMailbox struct {
	ID            string                 `json:"id"`
	LocalPart     string                 `json:"local_part"`
	EmailCached   string                 `json:"email_cached,omitempty"`
	PasswordHash  string                 `json:"password_hash,omitempty"`
	PasswordEnc   []byte                 `json:"password_enc,omitempty"`
	QuotaBytes    uint64                 `json:"quota_bytes"`
	IsDisabled    bool                   `json:"is_disabled"`
	CreatedAt     string                 `json:"created_at,omitempty"`
	Autoresponder *MetadataAutoresponder `json:"autoresponder,omitempty"`
	SharedWith    []MetadataMailboxShare `json:"shared_with,omitempty"`
}

// MetadataForwarder mirrors models.EmailForwarder. Each forwarder
// belongs to a (domain, mailbox) pair.
type MetadataForwarder struct {
	ID        string  `json:"id"`
	MailboxID *string `json:"mailbox_id,omitempty"`
	Type      string  `json:"type"` // alias | external
	LocalPart *string `json:"local_part,omitempty"`
	Target    string  `json:"target"`
	Enabled   bool    `json:"enabled"`
	CreatedAt string  `json:"created_at,omitempty"`
}

// MetadataAutoresponder mirrors models.EmailAutoresponder.
type MetadataAutoresponder struct {
	Enabled  bool    `json:"enabled"`
	FromDate string  `json:"from_date,omitempty"`
	ToDate   string  `json:"to_date,omitempty"`
	Subject  *string `json:"subject,omitempty"`
	TextBody *string `json:"text_body,omitempty"`
	HTMLBody *string `json:"html_body,omitempty"`
}

// MetadataMailboxShare mirrors models.MailboxShare. Rights is JSON-in-JSON.
type MetadataMailboxShare struct {
	ID                  string `json:"id"`
	SharedWithMailboxID string `json:"shared_with_mailbox_id"`
	Rights              string `json:"rights"` // raw JSON
	CreatedAt           string `json:"created_at,omitempty"`
}

// MetadataDNSRecord mirrors the user-editable columns of models.DNSRecord so a
// restore can re-create a zone's custom records (GH #267).
type MetadataDNSRecord struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	TTL       int    `json:"ttl"`
	Priority  int    `json:"priority"`
	IsEnabled bool   `json:"is_enabled"`
}

// MetadataDNSSECKey mirrors models.DomainDNSSECKey.
type MetadataDNSSECKey struct {
	KeyTag     int    `json:"key_tag"`
	KeyType    string `json:"key_type"`
	Algorithm  uint8  `json:"algorithm"`
	PublicKey  string `json:"public_key"`
	Active     bool   `json:"active"`
	ObservedAt string `json:"observed_at,omitempty"`
}

// MetadataAppInstall mirrors models.ApplicationInstall.
type MetadataAppInstall struct {
	ID            string  `json:"id"`
	DomainID      string  `json:"domain_id"`
	DBID          *string `json:"db_id,omitempty"`
	Version       *string `json:"version,omitempty"`
	AdminUsername string  `json:"admin_username"`
	AdminEmail    string  `json:"admin_email"`
	Locale        string  `json:"locale"`
	UseWWW        bool    `json:"use_www"`
	Subdirectory  string  `json:"subdirectory,omitempty"`
	Status        string  `json:"status"`
	AppType       string  `json:"app_type"`
	CreatedAt     string  `json:"created_at,omitempty"`
}

// MetadataDockerApp mirrors models.DockerApp — the panel row for a tenant
// docker-app install (GH #954). The app's DATA tree is a separate restic
// stage=docker snapshot (#1017); this row is what makes the restored app
// panel-managed instead of orphaned on-disk. The reverse-proxy domain link is
// carried by the domain's own proxy_pass rule (reconstructed with the domain),
// so preserving the app ID + published host ports keeps them consistent.
type MetadataDockerApp struct {
	ID             string  `json:"id"`
	Slug           string  `json:"slug"`
	InstanceSlug   string  `json:"instance_slug"`
	Name           string  `json:"name"`
	CatalogVersion string  `json:"catalog_version"`
	ImageSHA       *string `json:"image_sha,omitempty"`
	Status         string  `json:"status"`
	UpdateMode     string  `json:"update_mode"`
	CPULimit       *string `json:"cpu_limit,omitempty"`
	MemoryLimit    *string `json:"memory_limit,omitempty"`
	PIDsLimit      *int    `json:"pids_limit,omitempty"`
	CreatedAt      string  `json:"created_at,omitempty"`
	// Ports are the published-port rows (host_port pins the reverse-proxy
	// target the migrated domain's proxy_pass points at).
	Ports []MetadataDockerAppPort `json:"ports,omitempty"`
}

// MetadataDockerAppPort mirrors models.DockerAppPublishedPort.
type MetadataDockerAppPort struct {
	ID            string `json:"id"`
	PortName      string `json:"port_name"`
	ContainerPort int    `json:"container_port"`
	BindInterface string `json:"bind_interface"`
	HostPort      int    `json:"host_port"`
	Protocol      string `json:"protocol"`
	ReverseProxy  bool   `json:"reverse_proxy"`
	Enabled       bool   `json:"enabled"`
}

// MetadataSSHKey mirrors models.SSHKey.
type MetadataSSHKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// MetadataCronJob mirrors models.CronJob.
type MetadataCronJob struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	Schedule  string `json:"schedule"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at,omitempty"`
}

// MetadataEgressPolicy mirrors models.UserEgressPolicy.
type MetadataEgressPolicy struct {
	State             string `json:"state"`
	AllowedExtra      string `json:"allowed_extra"` // raw JSON
	LearningStartedAt string `json:"learning_started_at,omitempty"`
	UpdatedBy         string `json:"updated_by,omitempty"`
}

// MetadataEgressRequest mirrors models.UserEgressRequest.
type MetadataEgressRequest struct {
	ID         string `json:"id"`
	CIDR       string `json:"cidr"`
	Port       *uint  `json:"port,omitempty"`
	Protocol   string `json:"protocol"`
	Reason     string `json:"reason"`
	Status     string `json:"status"`
	ReviewedBy string `json:"reviewed_by,omitempty"`
	DecidedAt  string `json:"decided_at,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// MetadataLimitOverride mirrors models.UserLimitOverride.
type MetadataLimitOverride struct {
	DiskQuotaMB     *uint32 `json:"disk_quota_mb,omitempty"`
	CPUQuotaPercent *uint32 `json:"cpu_quota_percent,omitempty"`
	MemoryLimitMB   *uint32 `json:"memory_limit_mb,omitempty"`
	IOReadMbps      *uint32 `json:"io_read_mbps,omitempty"`
	IOWriteMbps     *uint32 `json:"io_write_mbps,omitempty"`
	MaxTasks        *uint32 `json:"max_tasks,omitempty"`
}

// MetadataSchemaVersion is the current AccountMetadata.schema_version.
// Bump on incompatible changes; restore validates strict equality and
// the producer always writes this constant.
const MetadataSchemaVersion = 2
