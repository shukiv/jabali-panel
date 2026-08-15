// Package migrate is the per-source-panel importer framework (M35).
//
// Each source kind (cpanel, directadmin, hestiacp, whm_pkgacct,
// lives under internal/migrate/<kind>/ and implements the
// Discoverer interface in discover.go. Step 1 (DB foundation) and
// Step 2 (this file + discover.go + stage.go) are the wave gate; per-
// source code (Steps 3-7) builds on these contracts without
// reopening them.
//
// All wire types here are stable: the manifest is persisted as JSON
// in migration_jobs.manifest_json after restore, and downstream
// resume code parses it on retry.
package migrate

import "time"

// ManifestSchemaVersion is bumped only on breaking changes to the
// AccountManifest shape. Resume code rejects manifests with a
// higher version than it understands; the operator must update the
// panel before re-running.
const ManifestSchemaVersion = 1

// AccountManifest is the canonical "what we found / what we plan to
// import" report for one source-side account. Built by the analyze
// stage, consumed by every later stage. Persisted as manifest_json
// when restore succeeds.
type AccountManifest struct {
	SchemaVersion int            `json:"schema_version"`
	Source        SourceRef      `json:"source"`
	Sizes         AccountSizes   `json:"sizes"`
	Domains       []DomainSpec   `json:"domains"`
	Mailboxes     []MailboxSpec  `json:"mailboxes"`
	Databases     []DatabaseSpec `json:"databases"`
	DNSZones      []DNSZoneSpec  `json:"dns_zones"`
	Cron          []CronSpec     `json:"cron"`
	SSH           []SSHKeySpec   `json:"ssh_keys"`
	Apps          []AppSpec      `json:"apps"`
	Warnings      []Warning      `json:"warnings,omitempty"`
}

// SourceRef pins where this manifest came from. Stable across
// resume retries.
type SourceRef struct {
	Kind string `json:"kind"` // models.MigrationSource* constants
	Host string `json:"host"` // FQDN or IP of the source panel
	User string `json:"user"` // source-side username
	// Tarball is set only for offline-mode importers (whm_pkgacct
	// uploads a tarball without a live source host).
	Tarball string `json:"tarball,omitempty"`
}

// AccountSizes summarises the bytes the importer must move. Drives
// quota projection in the validate stage.
type AccountSizes struct {
	HomeBytes int64 `json:"home_bytes"`
	DBsBytes  int64 `json:"dbs_bytes"`
	MailBytes int64 `json:"mail_bytes"`
	LogsBytes int64 `json:"logs_bytes"`
}

type DomainSpec struct {
	Name      string `json:"name"`
	DocRoot   string `json:"doc_root"`
	IsPrimary bool   `json:"is_primary"`
	HasPHP    bool   `json:"has_php"`
	PHPVer    string `json:"php_ver,omitempty"`
}

type MailboxSpec struct {
	Address     string `json:"address"` // local@domain
	BytesUsed   int64  `json:"bytes_used"`
	QuotaBytes  int64  `json:"quota_bytes"` // 0 = unlimited on source
	MaildirPath string `json:"maildir_path,omitempty"`
}

type DatabaseSpec struct {
	Engine string `json:"engine"` // mysql | postgres (postgres skipped v1)
	Name   string `json:"name"`
	// User the source linked to this DB. May map to a different
	// jabali user during restore.
	GrantUser string `json:"grant_user,omitempty"`
	// DumpPath is set after analyze writes the dump to the staging dir.
	DumpPath string `json:"dump_path,omitempty"`
	Bytes    int64  `json:"bytes"`
}

type DNSZoneSpec struct {
	Origin   string      `json:"origin"`
	Records  []DNSRecord `json:"records"`
	HasDNSEC bool        `json:"has_dnssec"`
	// SourceFile is the original BIND zone file path (DA), the
	// PowerDNS export blob (cPanel), etc.
	SourceFile string `json:"source_file,omitempty"`
}

type DNSRecord struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Prio    int    `json:"prio,omitempty"`
}

type CronSpec struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	// Source-side user the cron ran as. Validate stage matches it
	// against the importer's --target-user.
	RunAs string `json:"run_as"`
}

type SSHKeySpec struct {
	Comment   string `json:"comment"`
	PublicKey string `json:"public_key"`
	// Stable hash so duplicate keys across re-runs no-op.
	Fingerprint string `json:"fingerprint"`
}

// AppSpec captures detected web applications inside the source
// home tree. cPanel/DA both leave WordPress/Joomla/Drupal in
// recognisable layouts; we record what we found so the operator
// can pick whether to re-run wp-cli / drush after restore.
type AppSpec struct {
	Kind    string `json:"kind"` // wordpress | joomla | drupal | unknown
	Path    string `json:"path"` // absolute path inside source home
	Version string `json:"version,omitempty"`
}

// Warning is an importer-side advisory the operator should read
// before proceeding. NOT a fatal error — those return through the
// stage error path. Examples: "skipped: postgres_unsupported",
// "ssh key skipped: ed25519-sk requires hardware token".
type Warning struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	// At is the wall-clock time the analyze stage emitted this
	// warning; useful for ordering when multiple warnings stack.
	At time.Time `json:"at"`
}
