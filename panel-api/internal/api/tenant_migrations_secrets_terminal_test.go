package api

import (
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-301: a terminal migration job must reject new secret uploads — accepting
// them would write credentials for a job that will never run.
func TestTenantMigration_UploadSecretsToTerminalJob_409(t *testing.T) {
	for _, state := range []string{"done", "failed", "cancelled"} {
		h, jobs, ag := newTMHandler()
		uid := "USERA"
		owner := uid
		jobs.jobs["JOBA"] = &models.MigrationJob{ID: "JOBA", TargetUserID: &owner, State: state}

		c, w := ctxAs(uid, `{"ssh_password":"hunter2"}`, "JOBA")
		h.uploadSecrets(c)

		if w.Code != http.StatusConflict {
			t.Errorf("state %q: code=%d, want 409", state, w.Code)
		}
		if ag.last != nil {
			t.Errorf("state %q: terminal job must not write secrets to the agent", state)
		}
	}
}

// A live job still accepts secrets (guard doesn't over-reject).
func TestTenantMigration_UploadSecretsToLiveJob_OK(t *testing.T) {
	h, jobs, ag := newTMHandler()
	uid := "USERA"
	owner := uid
	jobs.jobs["JOBA"] = &models.MigrationJob{ID: "JOBA", TargetUserID: &owner, State: "validating"}

	c, w := ctxAs(uid, `{"ssh_password":"hunter2"}`, "JOBA")
	h.uploadSecrets(c)

	if w.Code != http.StatusOK {
		t.Fatalf("live job upload: code=%d (%s)", w.Code, w.Body.String())
	}
	if ag.last == nil {
		t.Error("a live job must write secrets to the agent")
	}
}
