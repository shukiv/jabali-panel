package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// installCounter counts the php.version.install calls the background
// installs make.
type installCounter struct{ calls atomic.Int64 }

func (a *installCounter) Call(_ context.Context, command string, _ any) (json.RawMessage, error) {
	if command == "php.version.install" {
		a.calls.Add(1)
	}
	return json.RawMessage(`{}`), nil
}

// The install keeps running after the handler has answered, while gin hands
// the same *gin.Context to the next request. The background install must
// not read the context then (go test -race reports it).
func TestPHPVersionAdmin_Install_BackgroundDoesNotReadTheRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})
	ag := &installCounter{}
	RegisterPHPVersionAdminRoutes(v1.Group("/admin", middleware.RequireAdmin()), ag, nil)

	accepted := int64(0)
	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/php/versions/8.1/install", nil))
		switch rec.Code {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict: // the previous install still holds 8.1
		default:
			t.Fatalf("request %d: want 202 or 409, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	if accepted == 0 {
		t.Fatal("no install was accepted")
	}
	deadline := time.Now().Add(5 * time.Second)
	for ag.calls.Load() < accepted {
		if time.Now().After(deadline) {
			t.Fatalf("%d installs accepted, %d reached the agent", accepted, ag.calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
