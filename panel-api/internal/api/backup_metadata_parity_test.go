package api

import (
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupmetadata"
)

// TestBackupMetadataAdaptersInLockstep guards JAB-359: both manual-backup
// handler configs (admin BackupHandlerConfig + tenant MeBackupsHandlerConfig)
// must declare every metadata section field that backupmetadata.Deps — the
// single producer they both feed — consumes. If a schema-v2 section is added to
// the builder but not mirrored onto an adapter config, that adapter's manual
// backup would silently omit the section (the exact JAB-312/JAB-359 gap that
// dropped egress policies/requests and, on /me, most of the set).
//
// The producer is the source of truth; KratosClient/Log are wiring, not
// sections, so they're excluded. So is CheckDomain (GH #1898): a restore-time
// guard hook, which the admin restore builds from its own alias and settings
// stores. And MailAddresses: the restore's Stalwart registry release, which
// only the admin restore wires. And Untrusted, RestoredDatabases,
// RestoredDockerSlugs, ArchiveMariaDBs, ArchivePostgresDBs, KeepExisting,
// OverwriteRows, MailboxSettings, ScheduleDomain, SkipMail, SkipPostgres,
// FtpPasswordsStaged and AccountCreated (GH #1993): per-restore inputs for a
// bundle from an uploaded file. And RestoreCertificates (GH #1993): whether
// the restore installs the certificates the bundle carries. And RestoresDNS
// (GH #1993): whether the restore door publishes DNS records afterwards.
func TestBackupMetadataAdaptersInLockstep(t *testing.T) {
	skip := map[string]bool{"KratosClient": true, "Log": true, "CheckDomain": true, "MailAddresses": true,
		"Untrusted": true, "RestoredDatabases": true, "RestoredDockerSlugs": true, "KeepExisting": true,
		"ArchiveMariaDBs": true, "ArchivePostgresDBs": true, "OverwriteRows": true, "MailboxSettings": true, "ScheduleDomain": true,
		"SkipMail": true, "SkipPostgres": true, "FtpPasswordsStaged": true, "AccountCreated": true,
		"RestoreCertificates": true, "RestoresDNS": true}

	builder := reflect.TypeOf(backupmetadata.Deps{})
	adapters := map[string]reflect.Type{
		"BackupHandlerConfig":    reflect.TypeOf(BackupHandlerConfig{}),
		"MeBackupsHandlerConfig": reflect.TypeOf(MeBackupsHandlerConfig{}),
	}

	for i := 0; i < builder.NumField(); i++ {
		f := builder.Field(i)
		if skip[f.Name] {
			continue
		}
		for name, typ := range adapters {
			if _, ok := typ.FieldByName(f.Name); !ok {
				t.Errorf("%s is missing metadata section field %q declared on backupmetadata.Deps — its manual backup bundle would omit that section", name, f.Name)
			}
		}
	}
}
