package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type pwBoundsMailboxRepo struct {
	saMailboxRepo
	created  bool
	pwWrites int
}

func (r *pwBoundsMailboxRepo) ExistsByDomainAndLocalPart(context.Context, string, string) (bool, error) {
	return false, nil
}
func (r *pwBoundsMailboxRepo) Create(context.Context, *models.Mailbox) error {
	r.created = true
	return nil
}
func (r *pwBoundsMailboxRepo) UpdatePasswordHashAndEnc(context.Context, string, string, []byte) error {
	r.pwWrites++
	return nil
}

func pwBoundsRouter(repo *pwBoundsMailboxRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	RegisterMailboxRoutes(r.Group(""), MailboxHandlerConfig{
		Mailboxes: repo,
		Domains: &saDomainRepo{byID: map[string]*models.Domain{
			"dom1": {ID: "dom1", Name: "x.test", UserID: "u1", EmailEnabled: true},
		}},
	})
	return r
}

func pwBoundsCall(t *testing.T, r *gin.Engine, method, target, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// A too-short mailbox password is the caller's mistake: 422 weak_password,
// and nothing is written. Before, the API took any non-empty password.
func TestMailboxCreate_ShortPasswordIs422(t *testing.T) {
	repo := &pwBoundsMailboxRepo{}
	code, body := pwBoundsCall(t, pwBoundsRouter(repo), http.MethodPost, "/domains/dom1/mailboxes",
		`{"local_part":"alice","password":"abc"}`)
	if code != http.StatusUnprocessableEntity || body["error"] != "weak_password" {
		t.Fatalf("status %d body %v, want 422 weak_password", code, body)
	}
	if repo.created {
		t.Fatal("the mailbox was created with the rejected password")
	}
}

func TestMailboxRotate_ShortPasswordIs422(t *testing.T) {
	mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "alice", EmailCached: "alice@x.test"}
	repo := &pwBoundsMailboxRepo{saMailboxRepo: saMailboxRepo{
		byID:    map[string]*models.Mailbox{"mb1": mb},
		byEmail: map[string]*models.Mailbox{"alice@x.test": mb},
	}}
	code, body := pwBoundsCall(t, pwBoundsRouter(repo), http.MethodPost, "/mailboxes/mb1/rotate-password",
		`{"new_password":"abc"}`)
	if code != http.StatusUnprocessableEntity || body["error"] != "weak_password" {
		t.Fatalf("status %d body %v, want 422 weak_password", code, body)
	}
	if repo.pwWrites != 0 {
		t.Fatal("the rejected password was written")
	}
}
