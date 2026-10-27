package api

// GH #2289: a delete that removed the unit before the row let a reconciler
// apply write the unit back, and a failed remove was ignored; the deleted app
// kept running on its port. The row goes first, and a failed remove still
// deletes the app (the reconciler's sweep removes a unit with no row).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type pyDeleteOrder struct{ steps []string }

type pyDeleteFakeApps struct {
	repository.PythonAppRepository
	app   *models.PythonApp
	order *pyDeleteOrder
	err   error
}

func (f *pyDeleteFakeApps) FindByID(context.Context, string) (*models.PythonApp, error) {
	return f.app, nil
}

func (f *pyDeleteFakeApps) Delete(context.Context, string) error {
	f.order.steps = append(f.order.steps, "row")
	return f.err
}

type pyDeleteFakeAgent struct {
	order *pyDeleteOrder
	err   error
}

func (f *pyDeleteFakeAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	f.order.steps = append(f.order.steps, cmd)
	return json.RawMessage(`{"removed":true}`), f.err
}

func deletePythonApp(t *testing.T, h *pythonAppHandler) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/python-apps/app1", nil)
	c.Params = gin.Params{{Key: "id", Value: "app1"}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	h.delete(c)
	return w
}

func pyDeleteHandler(order *pyDeleteOrder, rowErr, agentErr error) *pythonAppHandler {
	port := 20017
	app := &models.PythonApp{ID: "app1", UserID: "u1", DomainID: "dom1", LoopbackPort: &port}
	return &pythonAppHandler{cfg: PythonAppHandlerConfig{
		Apps:    &pyDeleteFakeApps{app: app, order: order, err: rowErr},
		Domains: &staticFakeDomains{domain: &models.Domain{ID: "dom1"}},
		Agent:   &pyDeleteFakeAgent{order: order, err: agentErr},
	}}
}

func TestPythonAppDelete_RowGoesBeforeTheUnit(t *testing.T) {
	order := &pyDeleteOrder{}
	w := deletePythonApp(t, pyDeleteHandler(order, nil, nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if want := []string{"row", "app.python.remove"}; !reflect.DeepEqual(order.steps, want) {
		t.Fatalf("steps = %q, want %q", order.steps, want)
	}
}

// A failed remove doesn't keep the app: its row is gone, and the reconciler
// removes the unit.
func TestPythonAppDelete_FailedRemoveStillDeletes(t *testing.T) {
	order := &pyDeleteOrder{}
	w := deletePythonApp(t, pyDeleteHandler(order, nil, errors.New("agent down")))
	if w.Code != 200 {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
}

// When the row can't be deleted the app is left as it is: its unit keeps
// running and its port stays its own.
func TestPythonAppDelete_RowErrorTouchesNothing(t *testing.T) {
	order := &pyDeleteOrder{}
	w := deletePythonApp(t, pyDeleteHandler(order, errors.New("db down"), nil))
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if want := []string{"row"}; !reflect.DeepEqual(order.steps, want) {
		t.Fatalf("steps = %q, want only the row delete", order.steps)
	}
}
