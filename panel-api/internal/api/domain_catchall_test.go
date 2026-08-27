package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"github.com/gin-gonic/gin"
)

// The catch-all HTTP path is thin over domainmailpolicy and cannot be
// box-verified (the socket needs a Kratos session), so these handler tests
// cover the JAB-338 error mapping + the empty-target-clears branch. Agent is
// nil, so the inline push is a clean no-op (no warning).
func buildCatchallRouter(dom *models.Domain) (*gin.Engine, *mockDomainRepo) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: dom.UserID, IsAdmin: true})
		c.Next()
	})
	repo := newMockDomainRepo()
	repo.domains[dom.ID] = dom
	RegisterDomainCatchallRoutes(v1, DomainCatchallHandlerConfig{Domains: repo})
	return r, repo
}

func putCatchall(r *gin.Engine, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/domains/d1/catchall", bytes.NewBufferString(`{"target":"`+target+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestCatchallPut(t *testing.T) {
	t.Run("valid external target is accepted + persisted (domain lowercased, +tag kept)", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com", EmailEnabled: true}
		r, repo := buildCatchallRouter(dom)
		if code := putCatchall(r, "Me+tag@Gmail.COM").Code; code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if got := repo.domains["d1"].CatchallTarget; got == nil || *got != "Me+tag@gmail.com" {
			t.Errorf("persisted target = %v", got)
		}
	})

	t.Run("garbage target is rejected 400", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com", EmailEnabled: true}
		r, repo := buildCatchallRouter(dom)
		if code := putCatchall(r, "not-an-email").Code; code != http.StatusBadRequest {
			t.Fatalf("garbage target should be 400, got %d", code)
		}
		if repo.domains["d1"].CatchallTarget != nil {
			t.Error("garbage target must not persist")
		}
	})

	t.Run("non-email domain is rejected 400", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com", EmailEnabled: false}
		r, _ := buildCatchallRouter(dom)
		if code := putCatchall(r, "a@b.com").Code; code != http.StatusBadRequest {
			t.Fatalf("catch-all on a non-email domain should be 400, got %d", code)
		}
	})

	t.Run("empty target clears", func(t *testing.T) {
		tgt := "old@b.com"
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com", EmailEnabled: true, CatchallTarget: &tgt}
		r, repo := buildCatchallRouter(dom)
		if code := putCatchall(r, "").Code; code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if repo.domains["d1"].CatchallTarget != nil {
			t.Errorf("empty target must clear, got %v", *repo.domains["d1"].CatchallTarget)
		}
	})
}
