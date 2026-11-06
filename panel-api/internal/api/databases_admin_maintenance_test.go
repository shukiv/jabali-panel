package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// maintAgent counts calls so a refused start can prove nothing was run.
type maintAgent struct{ calls atomic.Int32 }

func (a *maintAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	a.calls.Add(1)
	return json.RawMessage(`{"summary":"ok"}`), nil
}

func postMaintenance(t *testing.T, fa *fakeDBAdmin, ag *maintAgent) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{Agent: ag, DBAdmin: fa, Log: slog.Default()}}
	r := gin.New()
	r.POST("/maintenance", func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		h.runMaintenance(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/maintenance", bytes.NewReader([]byte(`{"engine":"mariadb","scope":"all"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Two starts can both pass the RunningJob check; the insert is where the
// database refuses the second one (unique running engine). That refusal is a
// 409 like the check's, not a 500, and nothing is sent to the agent.
func TestRunMaintenance_InsertConflictIs409(t *testing.T) {
	fa := &fakeDBAdmin{createErr: repository.ErrConflict}
	ag := &maintAgent{}
	w := postMaintenance(t, fa, ag)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("maintenance_already_running")) {
		t.Fatalf("body = %s, want maintenance_already_running", w.Body.String())
	}
	if n := ag.calls.Load(); n != 0 {
		t.Fatalf("agent called %d times after a refused start", n)
	}
}

func TestRunMaintenance_StartsWhenFree(t *testing.T) {
	fa := &fakeDBAdmin{}
	w := postMaintenance(t, fa, &maintAgent{})
	if w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", w.Code, w.Body.String())
	}
	if len(fa.created) != 1 || fa.created[0].Status != "running" || fa.created[0].Engine != "mariadb" {
		t.Fatalf("created = %+v, want one running mariadb job", fa.created)
	}
}
