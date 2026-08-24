package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type countingMigAgent struct{ calls int32 }

func (a *countingMigAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	atomic.AddInt32(&a.calls, 1)
	return json.RawMessage("{}"), nil
}

func fireImportWP(h *adminMigrationsHandler, id, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.runImportWP(c)
	return w
}

// JAB-301: runImportWP must load + state-gate the job before dispatch. A missing
// or terminal job must never reach the Agent.
func TestRunImportWP_GuardsMissingAndTerminal(t *testing.T) {
	body := `{"dest_user":"alice","dest_domain":"ex.com"}`

	t.Run("missing job → 404, no agent call", func(t *testing.T) {
		ag := &countingMigAgent{}
		h, _ := newIMAPHandler(ag)
		w := fireImportWP(h, "nope", body)
		if w.Code != http.StatusNotFound {
			t.Fatalf("code=%d, want 404", w.Code)
		}
		if atomic.LoadInt32(&ag.calls) != 0 {
			t.Error("missing job must not reach the agent")
		}
	})

	t.Run("terminal job → 409, no agent call", func(t *testing.T) {
		for _, state := range []string{"done", "failed", "cancelled"} {
			ag := &countingMigAgent{}
			h, jobs := newIMAPHandler(ag)
			jobs.byID["job1"] = &models.MigrationJob{ID: "job1", State: state}
			w := fireImportWP(h, "job1", body)
			if w.Code != http.StatusConflict {
				t.Errorf("state %q: code=%d, want 409", state, w.Code)
			}
			if atomic.LoadInt32(&ag.calls) != 0 {
				t.Errorf("state %q: terminal job must not reach the agent", state)
			}
		}
	})

	t.Run("live job → dispatches", func(t *testing.T) {
		ag := &countingMigAgent{}
		h, jobs := newIMAPHandler(ag)
		jobs.byID["job1"] = &models.MigrationJob{ID: "job1", State: "staged"}
		w := fireImportWP(h, "job1", body)
		if w.Code != http.StatusOK {
			t.Fatalf("code=%d, want 200 (%s)", w.Code, w.Body.String())
		}
		if atomic.LoadInt32(&ag.calls) != 1 {
			t.Errorf("a live job must dispatch exactly one agent call, got %d", ag.calls)
		}
	})
}
