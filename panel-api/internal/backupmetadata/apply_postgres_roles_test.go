package backupmetadata

import (
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a restore rebuilt a PostgreSQL database user's row but never its
// role or grants, so on another server the site's database login failed.
// Apply now has the agent create the role (with the SCRAM verifier the backup
// carried) and its grants, for the rows it created.

const pgVerifier = "SCRAM-SHA-256$4096:c2FsdHNhbHRzYWx0$c3RvcmVka2V5c3RvcmVka2V5c3RvcmVka2V5:c2VydmVya2V5c2VydmVya2V5c2VydmVya2V5"

// pgMeta is alice's bundle: PostgreSQL database alice_pg and role alice_u
// (with verifier v) granted on it.
func pgMeta(v string) *internalbackup.AccountMetadata {
	alice := "alice"
	return &internalbackup.AccountMetadata{
		User:      internalbackup.MetadataUser{ID: "u1", Username: &alice},
		Databases: []internalbackup.MetadataDatabase{{ID: "db1", Name: "alice_pg", Engine: "postgres"}},
		DatabaseUsers: []internalbackup.MetadataDatabaseUser{{
			ID: "du1", Username: "alice_u", Engine: "postgres", PostgresPasswordVerifier: v,
			Grants: []internalbackup.MetadataDatabaseUserGrant{{ID: "g1", DatabaseID: "db1", DatabaseName: "alice_pg", GrantLevel: "rw", Privileges: "ALL"}},
		}},
	}
}

func (a *maAgent) pgCalls() []maCall {
	var out []maCall
	for _, c := range a.calls {
		if strings.HasPrefix(c.cmd, "db.postgres.") {
			out = append(out, c)
		}
	}
	return out
}

func (a *maAgent) pgGrants() []maCall {
	var out []maCall
	for _, c := range a.pgCalls() {
		if c.cmd == "db.postgres.grant" {
			out = append(out, c)
		}
	}
	return out
}

func TestApply_RecreatesARestoredDatabaseUsersPostgresRoleAndGrant(t *testing.T) {
	f := newMAFixture()
	r := f.apply(pgMeta(pgVerifier))

	calls := f.agent.pgCalls()
	if len(calls) != 2 || calls[0].cmd != "db.postgres.create_role" || calls[1].cmd != "db.postgres.grant" {
		t.Fatalf("agent calls %+v (errors %v), want db.postgres.create_role then db.postgres.grant", calls, r.Errors)
	}
	if c := calls[0].params; c["role"] != "alice_u" || c["password_verifier"] != pgVerifier || c["password"] != nil || c["create_only"] != true {
		t.Fatalf("db.postgres.create_role %v: want alice_u with the backup's verifier, create_only", c)
	}
	if g := calls[1].params; g["db_name"] != "alice_pg" || g["role"] != "alice_u" {
		t.Fatalf("db.postgres.grant %v: want alice_pg to alice_u", g)
	}
	if len(f.agent.dbUserCalls()) != 0 {
		t.Fatalf("MariaDB calls %+v for a PostgreSQL user", f.agent.dbUserCalls())
	}
	if len(r.Errors) != 0 || f.users.rows["du1"] == nil || f.grants.rows["g1"] == nil || r.DatabaseUsers != 1 || r.DatabaseGrants != 1 {
		t.Fatalf("errors %v, rows users=%v grants=%v", r.Errors, f.users.rows, f.grants.rows)
	}
}

// A role without a SCRAM verifier in the backup (an older backup, or an md5
// password, which works only under the role's old name) still comes back,
// with a new password, and the report says so. A malformed verifier is never
// sent.
func TestApply_PostgresRoleWithoutASCRAMVerifierGetsANewPassword(t *testing.T) {
	for name, v := range map[string]string{
		"none":      "",
		"md5":       "md5" + strings.Repeat("0", 32),
		"malformed": pgVerifier + "'; ALTER ROLE postgres SUPERUSER; --",
	} {
		t.Run(name, func(t *testing.T) {
			f := newMAFixture()
			r := f.apply(pgMeta(v))
			calls := f.agent.pgCalls()
			if len(calls) != 2 || calls[0].cmd != "db.postgres.create_role" {
				t.Fatalf("agent calls %+v", calls)
			}
			pw, _ := calls[0].params["password"].(string)
			if len(pw) < 20 || calls[0].params["password_verifier"] != nil || calls[0].params["create_only"] != true {
				t.Fatalf("db.postgres.create_role %v: want a generated password, create_only", calls[0].params)
			}
			if !hasError(r.Errors, "db_user du1 (alice_u): restored with a new password, because the backup doesn't carry its PostgreSQL password") {
				t.Fatalf("errors %v should say the password is new", r.Errors)
			}
		})
	}
}

// A database user the account already had keeps its password: the restore
// adds only the grants it restored.
func TestApply_LeavesAnExistingPostgresRolesPasswordAlone(t *testing.T) {
	f := newMAFixture()
	f.users.rows["du1"] = &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "alice_u", Engine: "postgres"}
	r := f.apply(pgMeta(pgVerifier))

	if calls := f.agent.pgCalls(); len(calls) != 1 || calls[0].cmd != "db.postgres.grant" {
		t.Fatalf("agent calls %+v (errors %v), want only db.postgres.grant", calls, r.Errors)
	}
}

// The per-account Adminer role is the panel's own; no restore takes it from a
// bundle, nor creates it with the bundle's password.
func TestApply_DoesNotRestoreTheAccountsAdminerRole(t *testing.T) {
	f := newMAFixture()
	meta := pgMeta(pgVerifier)
	meta.DatabaseUsers[0].Username = "alice_pgadmin"
	r := f.apply(meta)

	if len(f.users.rows) != 0 || len(f.agent.pgCalls()) != 0 {
		t.Fatalf("restored %v / calls %+v", f.users.rows, f.agent.pgCalls())
	}
	if !hasError(r.Errors, "db_user du1 (alice_pgadmin): not restored: it is the account's Adminer role") {
		t.Fatalf("errors %v", r.Errors)
	}
}

// SECURITY: a role of the restored user's name that already exists is not the
// restored row's: the row would let the account's owner reset its password.
func TestApply_DatabaseUserWhoseNameExistsInPostgresIsTakenBackOut(t *testing.T) {
	f := newMAFixture()
	f.agent.fail["db.postgres.create_role"] = true
	f.agent.failErr = &agentwire.AgentError{Code: agentwire.CodeAlreadyExists, Message: "exists"}
	r := f.apply(pgMeta(pgVerifier))

	if f.users.rows["du1"] != nil || f.grants.rows["g1"] != nil || len(f.agent.pgGrants()) != 0 {
		t.Fatalf("rows left: users=%v grants=%v calls %+v", f.users.rows, f.grants.rows, f.agent.pgCalls())
	}
	if !hasError(r.Errors, "db_user du1 (alice_u): not restored: a PostgreSQL role with this name already exists on this server, and it is not this account's") {
		t.Fatalf("errors %v", r.Errors)
	}
}

func TestApply_PostgresRoleThatFailsIsTakenBackOut(t *testing.T) {
	f := newMAFixture()
	f.agent.fail["db.postgres.create_role"] = true
	r := f.apply(pgMeta(pgVerifier))

	if f.users.rows["du1"] != nil || f.grants.rows["g1"] != nil || r.DatabaseUsers != 0 || r.DatabaseGrants != 0 {
		t.Fatalf("rows left: users=%v grants=%v", f.users.rows, f.grants.rows)
	}
	if !hasError(r.Errors, "db_user du1 (alice_u): not restored: creating its PostgreSQL role failed") {
		t.Fatalf("errors %v", r.Errors)
	}
}

func TestApply_PostgresGrantThatFailsIsTakenBackOut(t *testing.T) {
	f := newMAFixture()
	f.agent.fail["db.postgres.grant"] = true
	r := f.apply(pgMeta(pgVerifier))

	if f.grants.rows["g1"] != nil || f.users.rows["du1"] == nil || r.DatabaseGrants != 0 {
		t.Fatalf("rows: users=%v grants=%v", f.users.rows, f.grants.rows)
	}
	if !hasError(r.Errors, "db_grant g1: not restored: granting it in PostgreSQL failed") {
		t.Fatalf("errors %v", r.Errors)
	}
}

// SECURITY: an agent that can't promise to leave an existing role alone
// creates none, and the panel keeps no row for a role that isn't there.
func TestApply_AgentWithoutPGRoleCreateOnlyCreatesNoRole(t *testing.T) {
	f := newMAFixture()
	f.agent.noPG = true
	r := f.apply(pgMeta(pgVerifier))

	if calls := f.agent.pgCalls(); len(calls) != 0 {
		t.Fatalf("agent calls %+v", calls)
	}
	if f.users.rows["du1"] != nil || f.grants.rows["g1"] != nil {
		t.Fatalf("rows left: users=%v grants=%v", f.users.rows, f.grants.rows)
	}
	if !hasError(r.Errors, "db_user du1 (alice_u): not restored: this server's agent is too old to create its PostgreSQL role safely") {
		t.Fatalf("errors %v", r.Errors)
	}
}

// A grant joins a user and a database of one engine: a bundle that pairs a
// MariaDB user with a PostgreSQL database (or the other way) gets no grant,
// in either engine, and no grant row.
func TestApply_GrantAcrossEnginesIsNotMade(t *testing.T) {
	for name, c := range map[string]struct {
		dbEngine, userEngine, want string
	}{
		"MariaDB user on a PostgreSQL database": {"postgres", "mariadb", "db_grant g1: not restored: alice_pg is a PostgreSQL database and alice_u a MariaDB user"},
		"PostgreSQL user on a MariaDB database": {"mariadb", "postgres", "db_grant g1: not restored: alice_pg is a MariaDB database and alice_u a PostgreSQL user"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMAFixture()
			meta := pgMeta(pgVerifier)
			meta.Databases[0].Engine = c.dbEngine
			meta.DatabaseUsers[0].Engine = c.userEngine
			meta.DatabaseUsers[0].NativePasswordHash = maHash
			r := f.apply(meta)

			for _, call := range f.agent.calls {
				if call.cmd == "db_user.grant" || call.cmd == "db.postgres.grant" {
					t.Fatalf("granted across engines: %+v", call)
				}
			}
			if f.grants.rows["g1"] != nil || r.DatabaseGrants != 0 || !hasError(r.Errors, c.want) {
				t.Fatalf("grant rows %v errors %v", f.grants.rows, r.Errors)
			}
		})
	}
}

// SECURITY: from an uploaded file, a PostgreSQL grant is made only on a
// database that holds nothing but the file's data, by the PostgreSQL list:
// the MariaDB one names other databases.
func TestApply_UploadedBackupGrantsOnAPostgresDatabaseOnlyWhenItHoldsOnlyTheFilesData(t *testing.T) {
	for name, c := range map[string]struct {
		maria, pg map[string]bool
		want      string // "" = granted
	}{
		"all the file's":                 {nil, map[string]bool{"alice_pg": true}, ""},
		"loaded over its data":           {map[string]bool{}, map[string]bool{}, "db_grant g1: not restored: alice_pg holds data that isn't the uploaded backup's"},
		"only the MariaDB list names it": {map[string]bool{"alice_pg": true}, map[string]bool{}, "db_grant g1: not restored: alice_pg holds data that isn't the uploaded backup's"},
		"an older agent":                 {map[string]bool{"alice_pg": true}, nil, "db_grant g1: not restored: this server's agent is too old to tell whether alice_pg holds only the uploaded backup's data"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMAFixture()
			r := Apply(context.Background(), pgMeta(pgVerifier), Deps{
				Users: namedUsersRepo{username: "alice"}, Databases: f.dbs, DatabaseUsers: f.users, DatabaseGrants: f.grants, Agent: f.agent,
				Untrusted: true, RestoredDatabases: map[string]bool{"alice_pg": true}, ArchiveMariaDBs: c.maria, ArchivePostgresDBs: c.pg,
			})
			grants := f.agent.pgGrants()
			if c.want == "" {
				if len(grants) != 1 || f.grants.rows["g1"] == nil {
					t.Fatalf("grants %+v rows %v errors %v, want the grant", grants, f.grants.rows, r.Errors)
				}
				return
			}
			if len(grants) != 0 || f.grants.rows["g1"] != nil || !hasError(r.Errors, c.want) {
				t.Fatalf("grants %+v rows %v errors %v, want %q", grants, f.grants.rows, r.Errors, c.want)
			}
		})
	}
}

// --- Overwrite ---

// owrPGApply restores pgMeta as an uploaded file, with Overwrite, whose data
// is all in alice_pg (by the PostgreSQL list).
func owrPGApply(f *maFixture, v string, tweak func(*Deps)) owrDBRun {
	meta := pgMeta(v)
	meta.DatabaseUsers[0].PasswordHash = "$2a$10$backup"
	return owrDBApplyWith(f, meta, true, func(d *Deps) {
		d.ArchiveMariaDBs = map[string]bool{}
		d.ArchivePostgresDBs = map[string]bool{"alice_pg": true}
		if tweak != nil {
			tweak(d)
		}
	}, "alice_pg")
}

// pgPasswordCalls are the db.postgres.create_role calls that set an existing
// role's password (not create_only).
func (o owrDBRun) pgPasswordCalls() []maCall {
	var out []maCall
	for _, c := range o.f.agent.pgCalls() {
		if c.cmd == "db.postgres.create_role" && c.params["create_only"] != true {
			out = append(out, c)
		}
	}
	return out
}

func TestApply_OverwriteSetsTheBackupsVerifierOnAPostgresRoleWhoseDataItRestored(t *testing.T) {
	f := newMAFixture()
	owrExistingDBUser(f, "alice_live", "postgres")
	o := owrPGApply(f, pgVerifier, nil)

	calls := o.pgPasswordCalls()
	if len(calls) != 1 || calls[0].params["role"] != "alice_live" || calls[0].params["password_verifier"] != pgVerifier || calls[0].params["password"] != nil {
		t.Fatalf("password calls %+v (errors %v), want this server's alice_live set to the backup's verifier", calls, o.result.Errors)
	}
	if o.users.hashes["du1"] != "$2a$10$backup" {
		t.Fatalf("panel hash %q, want the backup's", o.users.hashes["du1"])
	}
}

func TestApply_OverwriteKeepsAPostgresRolesPassword(t *testing.T) {
	for _, tc := range []struct {
		name  string
		v     string
		setup func(*maFixture)
		tweak func(*Deps)
		want  string
	}{
		{name: "no SCRAM verifier in the backup", v: "md5" + strings.Repeat("0", 32), want: "the backup doesn't carry its PostgreSQL password"},
		{
			// It could open alice_pg before; the file was loaded over its data.
			name: "only the MariaDB list names its database",
			v:    pgVerifier,
			setup: func(f *maFixture) {
				f.dbs.rows["db1"] = &models.Database{ID: "db1", UserID: "u1", Name: "alice_pg", Engine: "postgres"}
				f.grants.rows["g-here"] = &models.DatabaseUserGrant{ID: "g-here", DatabaseID: "db1", DatabaseUserID: "du1"}
			},
			tweak: func(d *Deps) {
				d.ArchiveMariaDBs, d.ArchivePostgresDBs = map[string]bool{"alice_pg": true}, map[string]bool{}
			},
			want: "it can open alice_pg, which holds data that isn't the uploaded backup's",
		},
		{
			name:  "an older agent",
			v:     pgVerifier,
			tweak: func(d *Deps) { d.ArchivePostgresDBs = nil },
			want:  "this server's agent is too old to tell",
		},
		{
			name: "it has a grant on a MariaDB database",
			v:    pgVerifier,
			setup: func(f *maFixture) {
				f.dbs.rows["db-m"] = &models.Database{ID: "db-m", UserID: "u1", Name: "alice_m", Engine: "mariadb"}
				f.grants.rows["g-m"] = &models.DatabaseUserGrant{ID: "g-m", DatabaseID: "db-m", DatabaseUserID: "du1"}
			},
			tweak: func(d *Deps) { d.RestoredDatabases["alice_m"], d.ArchivePostgresDBs["alice_m"] = true, true },
			want:  "it has a grant on alice_m, which is a MariaDB database",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMAFixture()
			owrExistingDBUser(f, "alice_u", "postgres")
			if tc.setup != nil {
				tc.setup(f)
			}
			o := owrPGApply(f, tc.v, tc.tweak)
			if calls := o.pgPasswordCalls(); len(calls) != 0 || len(o.users.hashes) != 0 {
				t.Fatalf("password calls %+v hashes %v, want the password kept", calls, o.users.hashes)
			}
			if !strings.Contains(strings.Join(o.result.Errors, "|"), "db_user du1 (alice_u): kept its password: "+tc.want) {
				t.Fatalf("errors %v, want %q", o.result.Errors, tc.want)
			}
		})
	}
}

// The panel row follows PostgreSQL: when the agent can't set the password,
// the row keeps the old one.
func TestApply_OverwriteKeepsThePanelHashWhenPostgresRefuses(t *testing.T) {
	f := newMAFixture()
	owrExistingDBUser(f, "alice_u", "postgres")
	f.agent.fail["db.postgres.create_role"] = true
	o := owrPGApply(f, pgVerifier, nil)
	if len(o.users.hashes) != 0 || !strings.Contains(strings.Join(o.result.Errors, "|"), "db_user du1 (alice_u): kept its password: setting it in PostgreSQL failed") {
		t.Fatalf("hashes %v errors %v", o.users.hashes, o.result.Errors)
	}
}
