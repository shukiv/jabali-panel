package api

import (
	"context"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// FindByID lets bulkCreate read the discovery draft's plan (GH #633/#634).
// Defined on the shared fakeIMAPJobs (admin_migrations_imap_test.go).
func (f *fakeIMAPJobs) FindByID(_ context.Context, id string) (*models.MigrationJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j, ok := f.byID[id]; ok {
		return j, nil
	}
	return nil, repository.ErrNotFound
}

// TestBulkCreate_ChildrenInheritDraftPreservePlan guards GH #633/#634.
//
// The wizard writes the import plan — selected areas + preserve.credentials —
// onto the discovery DRAFT job. The multi-account bulk handler must copy that
// PlanJSON into every child job. The original bug: it cloned only the SSH
// secret, never the plan, so each child imported with PlanJSON=nil →
// migrationPlanPreserve returned zero → migrated mailbox + MySQL-user passwords
// were silently reset even though the operator ticked "Carry over source
// passwords". This is the path a HestiaCP multi-account SSH migration uses.
func TestBulkCreate_ChildrenInheritDraftPreservePlan(t *testing.T) {
	// Agent=nil → inheritSecrets=false, so the secrets_clone precondition is
	// skipped; the draftPlan copy is gated only on source_job_id, so it still runs.
	h, jobs := newIMAPHandler(nil)

	plan := `{"areas":{"databases":true,"mailboxes":true},"preserve":{"credentials":true}}`
	draft := &models.MigrationJob{
		ID: "DRAFT01", SourceKind: "hestiacp", SourceHost: "web01",
		SourceUser: "__discovery__", PlanJSON: &plan,
	}
	if err := jobs.Create(context.Background(), draft); err != nil {
		t.Fatal(err)
	}

	body := `{"source_kind":"hestiacp","source_host":"web01",` +
		`"accounts":["notary","acme"],"source_job_id":"DRAFT01"}`
	w := postJSON(h.bulkCreate, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("bulkCreate status=%d body=%s", w.Code, w.Body.String())
	}

	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	got := 0
	for id, j := range jobs.byID {
		if id == "DRAFT01" {
			continue // the discovery draft itself
		}
		if j.PlanJSON == nil {
			t.Errorf("child %s (%s): PlanJSON is nil — preserve plan NOT inherited", id, j.SourceUser)
			continue
		}
		if *j.PlanJSON != plan {
			t.Errorf("child %s (%s): PlanJSON=%q, want draft plan %q", id, j.SourceUser, *j.PlanJSON, plan)
			continue
		}
		got++
	}
	if got != 2 {
		t.Fatalf("expected 2 children carrying the draft's preserve plan, got %d", got)
	}
}

// TestBulkCreate_NoSourceJob_NoPlan confirms the inheritance is scoped: without
// a source_job_id there is no draft to copy from, so children stay plan-less
// (import defaults to all-areas, preserve OFF = secure default).
func TestBulkCreate_NoSourceJob_NoPlan(t *testing.T) {
	h, jobs := newIMAPHandler(nil)
	body := `{"source_kind":"hestiacp","source_host":"web01","accounts":["solo"]}`
	w := postJSON(h.bulkCreate, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("bulkCreate status=%d body=%s", w.Code, w.Body.String())
	}
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	for _, j := range jobs.byID {
		if j.PlanJSON != nil {
			t.Errorf("child %s: PlanJSON=%q, want nil (no draft to inherit)", j.ID, *j.PlanJSON)
		}
	}
}
