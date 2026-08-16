// Package backup is the M30 wave-gate contract: types + restic CLI
// wrapper + manifest schema + tagging convention. Steps 3-12 (agent
// serializers, orchestrator, restore worker, REST handlers, system
// backup/restore) all build against this package.
//
// See ADR-0075 + plans/m30-backup-restore.md.
package backup

import (
	"fmt"
	"strings"
)

// Tag is one snapshot tag. Tags are key=value or bare strings; the
// blanket `jabali` tag has no value.
type Tag string

// Tag keys (left-hand side of `key=value`). Restic tags themselves
// are opaque strings; we constrain the ones Jabali emits so retention
// + listing can filter reliably.
const (
	TagKeyKind       = "kind"
	TagKeyJobID      = "job-id"
	TagKeyStage      = "stage"
	TagKeyUserID     = "user-id"
	TagKeySystem     = "system"
	TagKeyDB         = "db"
	TagKeyApp        = "app"
	TagKeyScheduleID = "schedule-id"
)

// Blanket tag applied to every Jabali-managed snapshot. Retention
// commands scope on this tag so foreign restic snapshots (operators
// running their own backups in the same repo) are never pruned.
const TagBlanket Tag = "jabali"

// BackupKind values — match BackupJobKind constants in panel-api/models.
const (
	KindAccountBackup  = "account_backup"
	KindAccountRestore = "account_restore"
	KindSystemBackup   = "system_backup"
	KindSystemRestore  = "system_restore"
)

// Stage values — ENUM-like at the wire, free-form at the schema. Each
// stage produces one restic snapshot; the manifest stage stitches them
// back together at restore.
const (
	// account-backup stages
	StageHome = "home"
	StageDB   = "db"
	StageMail = "mail"
	// StageDocker captures the data trees of the account's docker apps
	// (/var/lib/jabali/docker-apps/<slug>). They live outside the user
	// home, so the home stage never saw them — an account with a docker
	// app used to back up without its app data, and a migration or DR
	// restore brought the account back without the app (GH #954).
	//
	// docker_app.backup also snapshots these trees, but into the
	// operator's standalone repo (JABALI_RESTIC_REPO), not the account's
	// destination — which is why the omission stayed invisible.
	StageDocker = "docker"
	StageDNS      = "dns"
	StageCron     = "cron"
	StageSSH      = "ssh"
	StageApps     = "apps"
	StagePHP      = "php"
	StageMeta     = "meta" // YAML bundle for cron/ssh/apps/php
	StageManifest = "manifest"

	// system-backup stages — JABALI-PLANE ONLY. The restore target is a
	// freshly-installed Debian + freshly-installed jabali-panel; the OS
	// provider is responsible for hostname, fstab, apt, ssh, sudoers,
	// network. We capture only the things jabali itself owns so a
	// restore on a different VM doesn't trample the operator's OS.
	StagePanelDB       = "panel_db"
	StagePanelConfig   = "panel_config"
	StageServiceConfig = "service_config"
	StageMailState     = "mail_state"
	StageTLS           = "tls"
	StageSecurity      = "security"
	StageOSUsers       = "os_users"
	// jabali runtime state that lives outside MariaDB (crowdsec
	// decisions DB, redis AOF, tetragon spool, ACME webroot).
	StageDataState = "data_state"
)

// MakeTag formats `key=value`.
func MakeTag(key, value string) Tag {
	return Tag(fmt.Sprintf("%s=%s", key, value))
}

// JoinTagsAND renders tags as one comma-joined restic --tag value. restic's
// tag matching is OR across repeated --tag flags and AND within one
// comma-joined flag; every filter in this codebase means AND. Tag values
// never contain commas (ULIDs, stage names, db names, hostnames), which is
// what makes the comma join safe.
func JoinTagsAND(tags []Tag) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ",")
}

// AccountBackupTags returns the canonical tag set for one stage of
// an account_backup. scheduleID is empty for manual creates; when
// set, retention can target the schedule with `--tag schedule-id=<id>`.
func AccountBackupTags(jobID, userID, scheduleID, stage string) []Tag {
	tags := []Tag{
		TagBlanket,
		MakeTag(TagKeyKind, KindAccountBackup),
		MakeTag(TagKeyJobID, jobID),
		MakeTag(TagKeyUserID, userID),
		MakeTag(TagKeyStage, stage),
	}
	if scheduleID != "" {
		tags = append(tags, MakeTag(TagKeyScheduleID, scheduleID))
	}
	return tags
}

// SystemBackupTags returns the canonical tag set for one stage of
// a system_backup. `system=<hostname>` lets multi-host operators
// disambiguate snapshots in a shared remote repo.
func SystemBackupTags(jobID, hostname, scheduleID, stage string) []Tag {
	tags := []Tag{
		TagBlanket,
		MakeTag(TagKeyKind, KindSystemBackup),
		MakeTag(TagKeyJobID, jobID),
		MakeTag(TagKeySystem, hostname),
		MakeTag(TagKeyStage, stage),
	}
	if scheduleID != "" {
		tags = append(tags, MakeTag(TagKeyScheduleID, scheduleID))
	}
	return tags
}

// ToStrings converts a tag slice to []string for restic CLI args.
func ToStrings(tags []Tag) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = string(t)
	}
	return out
}
