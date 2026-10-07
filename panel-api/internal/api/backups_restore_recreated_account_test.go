package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: restoring an account's backup job into the same account after it
// was deleted and created again (same username, new id) rebuilt none of the
// panel rows: the bundle names the old id, so the rebuild tried to create the
// account a second time and the username clashed.

// rcUsers is a user table: Create refuses a taken id or username.
type rcUsers struct {
	repository.UserRepository
	rows map[string]*models.User
}

func (r *rcUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := r.rows[id]; ok {
		c := *u
		return &c, nil
	}
	return nil, repository.ErrNotFound
}

func (r *rcUsers) Create(_ context.Context, u *models.User) error {
	for _, e := range r.rows {
		if e.ID == u.ID || (e.Username != nil && u.Username != nil && *e.Username == *u.Username) {
			return repository.ErrConflict
		}
	}
	c := *u
	r.rows[u.ID] = &c
	return nil
}

// runRecreatedRestore restores a snapshot of alice (old id "u-old") into the
// account named target, whose id is "u-new", and returns the job's sealed
// status and the database rows.
func runRecreatedRestore(t *testing.T, target string) (*sealCapture, *daDBs, *rcUsers) {
	t.Helper()
	jobs := newSealCapture()
	users := &rcUsers{rows: map[string]*models.User{"u-new": {ID: "u-new", Username: &target}}}
	dbs := &daDBs{}
	h := &backupHandler{cfg: BackupHandlerConfig{
		Jobs: jobs, Users: users, Databases: dbs, DatabaseUsers: &daDBUsers{}, DatabaseGrants: &daGrants{},
		Agent: restoreAgent{reply: json.RawMessage(`{"job_id":"job-1","stages":[{"name":"db","status":"ok"}],"metadata":` +
			`{"user":{"id":"u-old","username":"alice"},"databases":[{"id":"db1","name":"alice_wp","engine":"mariadb"}]}}`)},
	}}
	h.runAccountRestoreJob("job-1", &models.BackupDestination{ID: "d1", Kind: "local"},
		map[string]any{"target_user_id": "u-new", "target_username": target})
	jobs.wait(t)
	return jobs, dbs, users
}

func TestRunAccountRestoreJob_RebuildsTheRowsOfARecreatedAccount(t *testing.T) {
	jobs, dbs, users := runRecreatedRestore(t, "alice")

	if jobs.status != models.BackupJobStatusSucceeded || jobs.errText != "" {
		t.Fatalf("status %q error %q, want succeeded", jobs.status, jobs.errText)
	}
	if len(dbs.rows) != 1 || dbs.rows[0].UserID != "u-new" {
		t.Fatalf("database rows %+v, want alice_wp on the recreated account u-new", dbs.rows)
	}
	if len(users.rows) != 1 {
		t.Fatalf("users %v: the restore must not create a second account", users.rows)
	}
}

// A snapshot of another account keeps its own account id: only the same
// account created again is retargeted.
func TestRunAccountRestoreJob_DoesNotMoveAnotherAccountsSnapshot(t *testing.T) {
	_, dbs, _ := runRecreatedRestore(t, "bob")
	for _, d := range dbs.rows {
		if d.UserID == "u-new" {
			t.Fatalf("database rows %+v: alice's snapshot was moved onto bob", dbs.rows)
		}
	}
}

func TestRetargetRecreatedAccount(t *testing.T) {
	bundle := json.RawMessage(`{"user":{"id":"u-old","username":"alice"},"domains":[{"id":"d1","name":"a.test"}]}`)
	for name, c := range map[string]struct {
		id, username, wantID string
	}{
		"same account, new id":   {"u-new", "alice", "u-new"},
		"same id":                {"u-old", "alice", "u-old"},
		"another account":        {"u-new", "bob", "u-old"},
		"target username absent": {"u-new", "", "u-old"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := retargetRecreatedAccount(bundle, c.id, c.username)
			if err != nil {
				t.Fatal(err)
			}
			if got := metadataUserID(out); got != c.wantID {
				t.Fatalf("user id %q, want %q", got, c.wantID)
			}
		})
	}
	// A bundle without a username is never retargeted, nor one whose username
	// is empty, onto a target without one.
	out, _ := retargetRecreatedAccount(json.RawMessage(`{"user":{"id":"u-old"}}`), "u-new", "alice")
	if metadataUserID(out) != "u-old" {
		t.Fatal("a bundle that names no username was retargeted")
	}
	out, _ = retargetRecreatedAccount(json.RawMessage(`{"user":{"id":"u-old","username":""}}`), "u-new", "")
	if metadataUserID(out) != "u-old" {
		t.Fatal("a bundle with an empty username was retargeted")
	}
}

// The DNS records go to the recreated account's domain too.
func TestRunAccountRestoreJob_RestoresARecreatedAccountsDNSRecords(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	f := &rdFixture{
		domains: &rdDomains{rows: map[string]*models.Domain{}},
		zones:   &rdZones{zones: map[string]*models.DNSZone{}, appearAt: map[string]int{}, lookups: map[string]int{}},
		records: &rdRecords{},
		sched:   &rdScheduler{},
		srv:     &models.ServerSettings{PublicIPv4: "203.0.113.10", DefaultDNSTTL: 300},
	}
	f.addDomain("d1", "example.com", "u-new", "z1")
	alice := "alice"
	jobs := newSealCapture()
	h := &backupHandler{cfg: BackupHandlerConfig{
		Jobs: jobs, Users: &rcUsers{rows: map[string]*models.User{"u-new": {ID: "u-new", Username: &alice}}},
		Agent: restoreAgent{reply: json.RawMessage(`{"job_id":"job-1","stages":[{"name":"home","status":"ok"}],"metadata":` +
			`{"user":{"id":"u-old","username":"alice"},"domains":[{"id":"d1","name":"example.com",` +
			`"dns_records":[{"name":"sub","type":"NS","content":"ns1.elsewhere.net.","ttl":3600,"is_enabled":true}]}]}}`)},
		Domains: &rdUploadDomains{*f.domains}, DNSZones: f.zones, DNSRecords: f.records,
		ServerSettings: rdSettings{s: f.srv}, Scheduler: f.sched,
	}}
	h.runAccountRestoreJob("job-1", &models.BackupDestination{ID: "d1", Kind: "local"},
		map[string]any{"target_user_id": "u-new", "target_username": "alice"})
	jobs.wait(t)

	if got := strings.Join(f.records.userRecords("z1"), "|"); got != "sub NS ns1.elsewhere.net." {
		t.Fatalf("records %q (status %s, error %q), want the backup's NS record", got, jobs.status, jobs.errText)
	}
}
