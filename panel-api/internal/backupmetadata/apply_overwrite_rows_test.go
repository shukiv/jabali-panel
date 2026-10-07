package backupmetadata

import (
	"context"
	"errors"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: with "Overwrite existing items with the backup" checked on an
// account upload door (OverwriteRows), the rows the account already has take
// the backup's settings. The file's author must not get a login to data the
// file didn't supply: an existing mailbox keeps its password (its mail is
// already here), and an existing database user takes the backup's password
// only when every database it can open was restored from this file.

// owrMailboxSettings records the changes Apply makes to existing mailboxes.
type owrMailboxSettings struct {
	quotas   map[string]uint64
	disabled map[string]bool
	err      error
}

func newOwrMailboxSettings() *owrMailboxSettings {
	return &owrMailboxSettings{quotas: map[string]uint64{}, disabled: map[string]bool{}}
}

func (s *owrMailboxSettings) SetQuota(_ context.Context, mb *models.Mailbox, quotaBytes uint64) error {
	if s.err != nil {
		return s.err
	}
	s.quotas[mb.ID] = quotaBytes
	return nil
}

func (s *owrMailboxSettings) SetDisabled(_ context.Context, mb *models.Mailbox, disabled bool) error {
	if s.err != nil {
		return s.err
	}
	s.disabled[mb.ID] = disabled
	return nil
}

const (
	owrGiB  = uint64(1) << 30
	owrHere = "$2a$10$here"
)

// owrMailboxRun restores info@alice.org from a backup onto the account's own
// info@alice.org (mb-here: 1 GiB, enabled).
func owrMailboxRun(t *testing.T, backup internalbackup.MetadataMailbox, overwrite bool, settings MailboxSettings) ApplyResult {
	t.Helper()
	f := newBnFixture()
	f.mbs.existing = []models.Mailbox{{ID: "mb-here", DomainID: "d-here", LocalPart: "info", EmailCached: "info@alice.org",
		QuotaBytes: owrGiB, PasswordHash: owrHere}}
	m := owMeta()
	dm := aliceDomain("d-here")
	dm.Mailboxes = []internalbackup.MetadataMailbox{backup}
	m.Domains = []internalbackup.MetadataDomain{dm}
	d := f.deps()
	d.Untrusted = true
	d.OverwriteRows = overwrite
	d.KeepExisting = !overwrite
	if settings != nil {
		d.MailboxSettings = settings
	}
	r := Apply(context.Background(), m, d)
	if len(f.mbs.created) != 0 {
		t.Fatalf("created mailboxes %+v; info@alice.org is already there", f.mbs.created)
	}
	return r
}

func owrBackupMailbox(quota uint64, disabled bool, hash string) internalbackup.MetadataMailbox {
	return internalbackup.MetadataMailbox{ID: "mb-backup", LocalPart: "info", EmailCached: "info@alice.org",
		QuotaBytes: quota, IsDisabled: disabled, PasswordHash: hash}
}

func owrHas(r ApplyResult, sub string) bool {
	return strings.Contains(strings.Join(r.Errors, "|"), sub)
}

func TestApply_OverwriteSetsAnExistingMailboxsQuotaAndDisabledButKeepsItsPassword(t *testing.T) {
	s := newOwrMailboxSettings()
	r := owrMailboxRun(t, owrBackupMailbox(2*owrGiB, true, "$2a$10$backup"), true, s)

	if s.quotas["mb-here"] != 2*owrGiB || !s.disabled["mb-here"] || len(s.quotas) != 1 || len(s.disabled) != 1 {
		t.Fatalf("quotas %v disabled %v, want mb-here at 2 GiB and disabled (errors %v)", s.quotas, s.disabled, r.Errors)
	}
	if !owrHas(r, "mailbox mb-backup (info@alice.org): kept its password") {
		t.Fatalf("errors %v, want the kept password reported", r.Errors)
	}
}

func TestApply_OverwriteChangesNothingOnAMailboxThatAlreadyMatches(t *testing.T) {
	s := newOwrMailboxSettings()
	r := owrMailboxRun(t, owrBackupMailbox(owrGiB, false, owrHere), true, s)
	if len(s.quotas)+len(s.disabled) != 0 || len(r.Errors) != 0 {
		t.Fatalf("quotas %v disabled %v errors %v, want nothing to change", s.quotas, s.disabled, r.Errors)
	}
	// Nothing to change needs no mailbox settings either.
	if r := owrMailboxRun(t, owrBackupMailbox(owrGiB, false, owrHere), true, nil); len(r.Errors) != 0 {
		t.Fatalf("errors %v without mailbox settings, want none", r.Errors)
	}
}

// A quota under the mailbox page's floor is not taken; the rest still is.
func TestApply_OverwriteKeepsAQuotaUnderTheFloor(t *testing.T) {
	s := newOwrMailboxSettings()
	r := owrMailboxRun(t, owrBackupMailbox(1024, true, owrHere), true, s)
	if len(s.quotas) != 0 || !s.disabled["mb-here"] {
		t.Fatalf("quotas %v disabled %v, want the quota kept and the mailbox disabled", s.quotas, s.disabled)
	}
	if !owrHas(r, "mailbox mb-backup (info@alice.org): quota not taken from the backup") {
		t.Fatalf("errors %v, want the kept quota reported", r.Errors)
	}
}

func TestApply_KeepingLeavesAnExistingMailboxAlone(t *testing.T) {
	s := newOwrMailboxSettings()
	r := owrMailboxRun(t, owrBackupMailbox(2*owrGiB, true, "$2a$10$backup"), false, s)
	if len(s.quotas)+len(s.disabled) != 0 || owrHas(r, "kept its password") {
		t.Fatalf("quotas %v disabled %v errors %v, want the mailbox left alone", s.quotas, s.disabled, r.Errors)
	}
}

func TestApply_OverwriteReportsAMailboxItCouldNotUpdate(t *testing.T) {
	t.Run("settings not wired", func(t *testing.T) {
		r := owrMailboxRun(t, owrBackupMailbox(2*owrGiB, false, owrHere), true, nil)
		if !owrHas(r, "mailbox mb-backup (info@alice.org): settings not updated") {
			t.Fatalf("errors %v", r.Errors)
		}
	})
	t.Run("update fails", func(t *testing.T) {
		s := newOwrMailboxSettings()
		s.err = errors.New("db down")
		r := owrMailboxRun(t, owrBackupMailbox(2*owrGiB, true, owrHere), true, s)
		if !owrHas(r, "mailbox mb-backup (info@alice.org): quota not updated: db down") ||
			!owrHas(r, "mailbox mb-backup (info@alice.org): disabled not updated: db down") {
			t.Fatalf("errors %v", r.Errors)
		}
	})
}

// --- database users ---

// owrGrants lists the grants by database user, like the grant repository.
type owrGrants struct{ *maGrants }

func (r owrGrants) ListByDatabaseUserID(_ context.Context, id string) ([]models.DatabaseUserGrant, error) {
	var out []models.DatabaseUserGrant
	for _, g := range r.rows {
		if g.DatabaseUserID == id {
			out = append(out, *g)
		}
	}
	return out, nil
}

// owrDBUsers records the panel password hashes Apply writes.
type owrDBUsers struct {
	*maDBUsers
	hashes map[string]string
}

func (r owrDBUsers) UpdatePasswordHash(_ context.Context, id, hash string) error {
	r.hashes[id] = hash
	return nil
}

type owrDBRun struct {
	f      *maFixture
	users  owrDBUsers
	result ApplyResult
}

// owrDBApply restores meta as an uploaded file whose data restored the
// databases named in restored, into databases that held nothing before.
func owrDBApply(f *maFixture, meta *internalbackup.AccountMetadata, overwrite bool, restored ...string) owrDBRun {
	return owrDBApplyWith(f, meta, overwrite, nil, restored...)
}

// owrDBApplyWith is owrDBApply with tweak applied to the deps first.
func owrDBApplyWith(f *maFixture, meta *internalbackup.AccountMetadata, overwrite bool, tweak func(*Deps), restored ...string) owrDBRun {
	users := owrDBUsers{maDBUsers: f.users, hashes: map[string]string{}}
	set, archive := map[string]bool{}, map[string]bool{}
	for _, n := range restored {
		set[n], archive[n] = true, true
	}
	d := Deps{
		Users: namedUsersRepo{username: "alice"}, Databases: f.dbs, DatabaseUsers: users,
		DatabaseGrants: owrGrants{f.grants}, Agent: f.agent,
		Untrusted: true, RestoredDatabases: set, ArchiveMariaDBs: archive, OverwriteRows: overwrite, KeepExisting: !overwrite,
	}
	if tweak != nil {
		tweak(&d)
	}
	r := Apply(context.Background(), meta, d)
	return owrDBRun{f: f, users: users, result: r}
}

// passwordCalls are the db_user.create calls that set an existing user's
// password (not create_only).
func (o owrDBRun) passwordCalls() []maCall {
	var out []maCall
	for _, c := range o.f.agent.dbUserCalls() {
		if c.cmd == "db_user.create" && c.params["create_only"] != true {
			out = append(out, c)
		}
	}
	return out
}

// owrBackupDBUser is maMeta (alice_wp, and alice_u granted on it) with the
// panel row's own hash.
func owrBackupDBUser(hash string) *internalbackup.AccountMetadata {
	m := maMeta(hash)
	m.DatabaseUsers[0].PasswordHash = "$2a$10$backup"
	return m
}

func owrExistingDBUser(f *maFixture, username, engine string) {
	f.users.rows["du1"] = &models.DatabaseUser{ID: "du1", UserID: "u1", Username: username, Engine: engine, PasswordHash: owrHere}
}

func TestApply_OverwriteSetsTheBackupsPasswordOnADatabaseUserWhoseDataItRestored(t *testing.T) {
	f := newMAFixture()
	owrExistingDBUser(f, "alice_u", "mariadb")
	o := owrDBApply(f, owrBackupDBUser(maHash), true, "alice_wp")

	calls := o.passwordCalls()
	if len(calls) != 1 || calls[0].params["db_user_name"] != "alice_u" || calls[0].params["password_hash"] != maHash ||
		calls[0].params["password"] != nil {
		t.Fatalf("password calls %+v (errors %v), want alice_u set to the backup's hash", calls, o.result.Errors)
	}
	if o.users.hashes["du1"] != "$2a$10$backup" {
		t.Fatalf("panel hash %q, want the backup's", o.users.hashes["du1"])
	}
}

// The MariaDB account changed is this server's row's, whatever the file names.
func TestApply_OverwriteSetsThePasswordOfThisServersAccount(t *testing.T) {
	f := newMAFixture()
	owrExistingDBUser(f, "alice_live", "mariadb")
	o := owrDBApply(f, owrBackupDBUser(maHash), true, "alice_wp")
	calls := o.passwordCalls()
	if len(calls) != 1 || calls[0].params["db_user_name"] != "alice_live" {
		t.Fatalf("password calls %+v (errors %v), want this server's alice_live", calls, o.result.Errors)
	}
}

func TestApply_OverwriteKeepsADatabaseUsersPassword(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(*maFixture)
		meta    *internalbackup.AccountMetadata
		restore []string
		tweak   func(*Deps)
		want    string
	}{
		{
			name: "it can open a database the backup didn't restore",
			setup: func(f *maFixture) {
				owrExistingDBUser(f, "alice_u", "mariadb")
				f.dbs.rows["db-old"] = &models.Database{ID: "db-old", UserID: "u1", Name: "alice_old", Engine: "mariadb"}
				f.grants.rows["g-old"] = &models.DatabaseUserGrant{ID: "g-old", DatabaseID: "db-old", DatabaseUserID: "du1"}
			},
			meta: owrBackupDBUser(maHash), restore: []string{"alice_wp"},
			want: "it can open alice_old, which this backup didn't restore",
		},
		{
			name:  "the backup didn't restore its database's data",
			setup: func(f *maFixture) { owrExistingDBUser(f, "alice_u", "mariadb") },
			meta:  owrBackupDBUser(maHash),
			want:  "it can open no database this backup restored",
		},
		{
			name:  "the backup has no MariaDB password for it",
			setup: func(f *maFixture) { owrExistingDBUser(f, "alice_u", "mariadb") },
			meta:  owrBackupDBUser(""), restore: []string{"alice_wp"},
			want: "the backup doesn't carry its MariaDB password",
		},
		{
			// Its database was loaded over the data it had here.
			name: "a database it can open already had data here",
			setup: func(f *maFixture) {
				owrExistingDBUser(f, "alice_u", "mariadb")
				f.dbs.rows["db1"] = &models.Database{ID: "db1", UserID: "u1", Name: "alice_wp", Engine: "mariadb"}
				f.grants.rows["g-here"] = &models.DatabaseUserGrant{ID: "g-here", DatabaseID: "db1", DatabaseUserID: "du1"}
			},
			meta: owrBackupDBUser(maHash), restore: []string{"alice_wp"},
			tweak: func(d *Deps) { d.ArchiveMariaDBs = map[string]bool{} },
			want:  "it can open alice_wp, which holds data that isn't the uploaded backup's",
		},
		{
			name: "it can open another account's database",
			setup: func(f *maFixture) {
				owrExistingDBUser(f, "alice_u", "mariadb")
				f.dbs.rows["db-x"] = &models.Database{ID: "db-x", UserID: "u2", Name: "alice_x", Engine: "mariadb"}
				f.grants.rows["g-x"] = &models.DatabaseUserGrant{ID: "g-x", DatabaseID: "db-x", DatabaseUserID: "du1"}
			},
			meta: owrBackupDBUser(maHash), restore: []string{"alice_wp", "alice_x"},
			want: "it can open alice_x, which isn't this account's",
		},
		{
			name:  "an older agent",
			setup: func(f *maFixture) { owrExistingDBUser(f, "alice_u", "mariadb") },
			meta:  owrBackupDBUser(maHash), restore: []string{"alice_wp"},
			tweak: func(d *Deps) { d.ArchiveMariaDBs = nil },
			want:  "this server's agent is too old to tell",
		},
		{
			name:  "the backup has no PostgreSQL password for it",
			setup: func(f *maFixture) { owrExistingDBUser(f, "alice_u", "postgres") },
			meta:  owrBackupDBUser(maHash), restore: []string{"alice_wp"},
			want: "the backup doesn't carry its PostgreSQL password",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMAFixture()
			tc.setup(f)
			o := owrDBApplyWith(f, tc.meta, true, tc.tweak, tc.restore...)
			if calls := o.passwordCalls(); len(calls) != 0 || len(o.users.hashes) != 0 {
				t.Fatalf("password calls %+v hashes %v, want the password kept", calls, o.users.hashes)
			}
			if !strings.Contains(strings.Join(o.result.Errors, "|"), "db_user du1 (alice_u): kept its password: "+tc.want) {
				t.Fatalf("errors %v, want %q", o.result.Errors, tc.want)
			}
		})
	}
}

// The panel row follows MariaDB: when the agent can't set the password, the
// row keeps the old one.
func TestApply_OverwriteKeepsThePanelHashWhenMariaDBRefuses(t *testing.T) {
	f := newMAFixture()
	owrExistingDBUser(f, "alice_u", "mariadb")
	f.agent.fail["db_user.create"] = true
	o := owrDBApply(f, owrBackupDBUser(maHash), true, "alice_wp")
	if len(o.users.hashes) != 0 || !strings.Contains(strings.Join(o.result.Errors, "|"), "db_user du1 (alice_u): kept its password: setting it in MariaDB failed") {
		t.Fatalf("hashes %v errors %v", o.users.hashes, o.result.Errors)
	}
}

func TestApply_KeepingLeavesAnExistingDatabaseUsersPassword(t *testing.T) {
	f := newMAFixture()
	owrExistingDBUser(f, "alice_u", "mariadb")
	o := owrDBApply(f, owrBackupDBUser(maHash), false, "alice_wp")
	if calls := o.passwordCalls(); len(calls) != 0 || len(o.users.hashes) != 0 {
		t.Fatalf("password calls %+v hashes %v, want the password left alone", calls, o.users.hashes)
	}
}
