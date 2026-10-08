package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: a restore leaves out what is turned off on this server. With
// SkipPostgres Apply restores no PostgreSQL database or user, and with
// SkipMail no mailbox or forwarder.

func TestApply_SkipPostgresLeavesPostgresOut(t *testing.T) {
	f := newMAFixture()
	meta := pgMeta(pgVerifier)
	my := maMeta(maHash)
	meta.Databases = append(meta.Databases, my.Databases[0])
	meta.DatabaseUsers = append(meta.DatabaseUsers, my.DatabaseUsers[0])
	meta.Databases[1].ID, meta.DatabaseUsers[1].ID, meta.DatabaseUsers[1].Grants[0].ID, meta.DatabaseUsers[1].Grants[0].DatabaseID = "db2", "du2", "g2", "db2"
	meta.DatabaseUsers[1].Username = "alice_my"

	r := Apply(context.Background(), meta, Deps{
		Users: namedUsersRepo{username: "alice"}, Databases: f.dbs, DatabaseUsers: f.users, DatabaseGrants: f.grants, Agent: f.agent,
		SkipPostgres: true,
	})

	if calls := f.agent.pgCalls(); len(calls) != 0 {
		t.Errorf("PostgreSQL calls %+v with PostgreSQL left out", calls)
	}
	if f.dbs.rows["db1"] != nil || f.users.rows["du1"] != nil || f.grants.rows["g1"] != nil {
		t.Errorf("PostgreSQL rows restored: dbs %v users %v grants %v", f.dbs.rows, f.users.rows, f.grants.rows)
	}
	if f.dbs.rows["db2"] == nil || f.users.rows["du2"] == nil || f.grants.rows["g2"] == nil {
		t.Errorf("the MariaDB rows were not restored (errors %v): dbs %v users %v", r.Errors, f.dbs.rows, f.users.rows)
	}
}

// A database user from an older bundle has no engine: one granted on a
// PostgreSQL database is left out with it.
func TestDropSkippedParts_PostgresUserWithoutAnEngine(t *testing.T) {
	m := pgMeta(pgVerifier)
	m.DatabaseUsers[0].Engine = ""
	dropSkippedParts(m, Deps{SkipPostgres: true})
	if len(m.DatabaseUsers) != 0 || len(m.Databases) != 0 {
		t.Errorf("left %+v %+v, want the PostgreSQL user and database out", m.DatabaseUsers, m.Databases)
	}
}

func TestDropSkippedParts_Mail(t *testing.T) {
	m := &internalbackup.AccountMetadata{Domains: []internalbackup.MetadataDomain{{
		Name: "alice.org", EmailEnabled: true,
		Mailboxes:  []internalbackup.MetadataMailbox{{LocalPart: "info", Autoresponder: &internalbackup.MetadataAutoresponder{}}},
		Forwarders: []internalbackup.MetadataForwarder{{Target: "x@example.org"}},
		DNSRecords: []internalbackup.MetadataDNSRecord{{Name: "www"}},
	}}}
	dropSkippedParts(m, Deps{})
	if len(m.Domains[0].Mailboxes) != 1 {
		t.Fatal("nothing skipped, but the mail went")
	}
	dropSkippedParts(m, Deps{SkipMail: true})
	d := m.Domains[0]
	if len(d.Mailboxes) != 0 || len(d.Forwarders) != 0 {
		t.Errorf("mail left in: %+v", d)
	}
	if d.Name != "alice.org" || !d.EmailEnabled || len(d.DNSRecords) != 1 {
		t.Errorf("the domain changed: %+v", d)
	}
}
