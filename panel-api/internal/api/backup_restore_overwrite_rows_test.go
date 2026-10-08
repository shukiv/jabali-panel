package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: with "Overwrite existing items with the backup" checked, a
// restore from an uploaded file also updates the rows the account already
// has. Only the two account upload doors do. A full server restore replaces
// each account's files and mail, but leaves its rows as they are.

// captureRebuilds records what each upload restore hands the metadata
// rebuild.
func captureRebuilds(t *testing.T) func() []uploadedData {
	t.Helper()
	var mu sync.Mutex
	var got []uploadedData
	prev := applyUploadedMetadata
	applyUploadedMetadata = func(_ *backupHandler, _ context.Context, _ json.RawMessage, _ string, u uploadedData) []string {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, u)
		return nil
	}
	t.Cleanup(func() { applyUploadedMetadata = prev })
	return func() []uploadedData {
		mu.Lock()
		defer mu.Unlock()
		return append([]uploadedData(nil), got...)
	}
}

func TestRunUploadRestore_OverwriteUpdatesTheAccountsRows(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		rebuilds := captureRebuilds(t)
		h, a, _ := ucUploadPasses(t, func(int) string { return `{"upload_confinement_enforced":true}` })
		a.overwrite = overwrite
		h.runUploadRestore(a)
		got := rebuilds()
		if len(got) != 1 || got[0].overwriteRows != overwrite || got[0].keepExisting == overwrite {
			t.Fatalf("overwrite=%v: the rebuild got %+v", overwrite, got)
		}
		if deps := h.restoreMetadataDeps(&got[0]); deps.OverwriteRows != overwrite {
			t.Errorf("overwrite=%v: rebuild deps OverwriteRows=%v", overwrite, deps.OverwriteRows)
		}
	}
}

func TestUploadedBackupRestore_OverwriteUpdatesTheAccountsRows(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		rebuilds := captureRebuilds(t)
		e := newUBEnv(t, &ubAgent{})
		b := keep(t, e, models.UploadedBackupKeep)
		w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore",
			map[string]any{"target_username": "alice", "overwrite": overwrite})
		if w.Code != http.StatusAccepted {
			t.Fatalf("overwrite=%v: restore %d %s", overwrite, w.Code, w.Body)
		}
		waitRestore(t, e, b.ID)
		got := rebuilds()
		if len(got) != 1 || got[0].overwriteRows != overwrite || got[0].keepExisting == overwrite {
			t.Fatalf("overwrite=%v: the rebuild got %+v", overwrite, got)
		}
	}
}

// A full server restore replaces the account's data (keep_existing off) and
// still leaves the rows it already has alone.
func TestRunFullRestore_ReplacesTheDataButNotTheRows(t *testing.T) {
	rebuilds := captureRebuilds(t)
	var restoreParams []map[string]any
	ag := &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		switch cmd {
		case "agent.version":
			return json.RawMessage(`{"version":"x","capabilities":["restore_upload_confinement","restore_keep_existing"]}`), nil
		case "system.fullbackup.extract_uploaded":
			return json.RawMessage(`{"stage":"/var/lib/jabali-uploads/fullrestore-X","users":[` +
				`{"username":"alice","inner_path":"/var/lib/jabali-uploads/fullrestore-X/users/alice.tar.zst"}]}`), nil
		case "backup.restore_from_tar":
			restoreParams = append(restoreParams, params.(map[string]any))
			return json.RawMessage(`{"applied":["home → /home/alice"],"upload_confinement_enforced":true}`), nil
		case "system.fullbackup.cleanup_stage":
			return json.RawMessage(`{"cleaned":"x"}`), nil
		}
		return nil, fmt.Errorf("unexpected agent call %q", cmd)
	}}
	cfg := ucConfig()
	cfg.Agent = ag
	cfg.Users = fullRestoreUsers{}
	h := &backupHandler{cfg: cfg}
	marker := fullMarker(t)
	h.runFullRestore("/x/container.tar.zst", marker, fullRestoreApplyRequest{Usernames: []string{"alice"}})

	if o := readFullOutcome(t, marker); o.Status != "done" || len(restoreParams) == 0 {
		t.Fatalf("outcome %+v", o)
	}
	if len(restoreParams) == 0 || restoreParams[0]["keep_existing"] != false {
		t.Fatalf("restore_from_tar params %v, want keep_existing off", restoreParams)
	}
	got := rebuilds()
	if len(got) != 1 || got[0].overwriteRows || got[0].keepExisting {
		t.Fatalf("the rebuild got %+v, want the data replaced and the rows left alone", got)
	}
	// It has no "Keep the backup's SSL certificates" choice (GH #1993).
	if got[0].keepCertificates {
		t.Fatalf("the rebuild got %+v, want the backup's certificates left out", got)
	}
}

// The rebuild changes an existing mailbox through the mailbox settings, so
// the mail server follows; a restore from this server's own backups updates
// no existing row.
func TestRestoreMetadataDeps_OverwriteRowsWiring(t *testing.T) {
	h := &backupHandler{cfg: BackupHandlerConfig{Mailboxes: &mpMailboxRepo{}}}
	deps := h.restoreMetadataDeps(&uploadedData{overwriteRows: true})
	if !deps.OverwriteRows || deps.MailboxSettings == nil {
		t.Fatalf("deps OverwriteRows=%v MailboxSettings=%v", deps.OverwriteRows, deps.MailboxSettings)
	}
	if h.restoreMetadataDeps(nil).OverwriteRows {
		t.Error("a restore from this server's own backups updates no existing row")
	}
}

// An existing domain whose web settings the rebuild changes is converged at
// once, as after a save on the domain page.
func TestRestoreMetadataDeps_ScheduleDomainWiring(t *testing.T) {
	sched := &rmdScheduler{}
	h := &backupHandler{cfg: BackupHandlerConfig{Scheduler: sched}}
	deps := h.restoreMetadataDeps(&uploadedData{overwriteRows: true})
	if deps.ScheduleDomain == nil {
		t.Fatal("ScheduleDomain not wired")
	}
	deps.ScheduleDomain("d1")
	if len(sched.ids) != 1 || sched.ids[0] != "d1" {
		t.Fatalf("scheduled %v, want d1", sched.ids)
	}
	if (&backupHandler{}).restoreMetadataDeps(&uploadedData{overwriteRows: true}).ScheduleDomain != nil {
		t.Error("ScheduleDomain wired without a scheduler")
	}
}

type rmdScheduler struct{ ids []string }

func (s *rmdScheduler) Schedule(id string) { s.ids = append(s.ids, id) }

// rmsMailboxes records the mailbox setters a restore calls.
type rmsMailboxes struct {
	repository.MailboxRepository
	quotas   map[string]uint64
	disabled map[string]bool
}

func (r *rmsMailboxes) UpdateQuota(_ context.Context, id string, q uint64) error {
	r.quotas[id] = q
	return nil
}

func (r *rmsMailboxes) SetDisabled(_ context.Context, id string, disabled bool) error {
	r.disabled[id] = disabled
	return nil
}

// A restore changes an existing mailbox the way the mailbox page does: the
// quota floor, the mail server told of the new quota, and its login cache
// flushed on a disable.
func TestRestoreMailboxSettings_DoesWhatTheMailboxPageDoes(t *testing.T) {
	ctx := context.Background()
	repo := &rmsMailboxes{quotas: map[string]uint64{}, disabled: map[string]bool{}}
	var calls []string
	ag := &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		calls = append(calls, fmt.Sprintf("%s %v", cmd, params))
		return json.RawMessage(`{}`), nil
	}}
	s := restoreMailboxSettings{mailboxes: repo, agent: ag}
	mb := &models.Mailbox{ID: "mb1", EmailCached: "info@alice.org"}

	if err := s.SetQuota(ctx, mb, 1024); err == nil || len(repo.quotas) != 0 {
		t.Fatalf("a quota under the floor: err %v quotas %v", err, repo.quotas)
	}
	if err := s.SetQuota(ctx, mb, 2<<30); err != nil || repo.quotas["mb1"] != 2<<30 {
		t.Fatalf("quota: err %v quotas %v", err, repo.quotas)
	}
	if err := s.SetDisabled(ctx, mb, false); err != nil || repo.disabled["mb1"] {
		t.Fatalf("enable: err %v disabled %v", err, repo.disabled)
	}
	if err := s.SetDisabled(ctx, mb, true); err != nil || !repo.disabled["mb1"] {
		t.Fatalf("disable: err %v disabled %v", err, repo.disabled)
	}
	want := []string{"mailbox.set_quota map[email:info@alice.org id:mb1 quota_bytes:2147483648]", "mail.auth_cache.flush map[]"}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("agent calls %q, want %q", calls, want)
	}
}
