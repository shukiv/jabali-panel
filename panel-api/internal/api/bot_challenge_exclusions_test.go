package api

// GH #2328: an admin lists, per domain, paths that skip the bot-detection
// challenge.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type botExclDomains struct {
	repository.DomainRepository
	byID map[string]*models.Domain
}

func (d *botExclDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if dom, ok := d.byID[id]; ok {
		return dom, nil
	}
	return nil, repository.ErrNotFound
}

type botExclStore struct{ rows []models.BotChallengeExclusion }

func (s *botExclStore) List(context.Context) ([]models.BotChallengeExclusion, error) {
	return s.rows, nil
}

func (s *botExclStore) ListByDomain(_ context.Context, domainID string) ([]models.BotChallengeExclusion, error) {
	var out []models.BotChallengeExclusion
	for _, r := range s.rows {
		if r.DomainID == domainID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *botExclStore) CountByDomain(ctx context.Context, domainID string) (int64, error) {
	rows, _ := s.ListByDomain(ctx, domainID)
	return int64(len(rows)), nil
}

func (s *botExclStore) Create(_ context.Context, e *models.BotChallengeExclusion) error {
	for _, r := range s.rows {
		if r.DomainID == e.DomainID && r.PathPrefix == e.PathPrefix && r.Methods == e.Methods {
			return repository.ErrConflict
		}
	}
	s.rows = append(s.rows, *e)
	return nil
}

func (s *botExclStore) DeleteForDomain(_ context.Context, domainID, id string) error {
	for i, r := range s.rows {
		if r.ID == id && r.DomainID == domainID {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

func newBotExclRouter(store *botExclStore, isAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: isAdmin})
		c.Next()
	})
	domains := &botExclDomains{byID: map[string]*models.Domain{
		"d1": {ID: "d1", Name: "api.example", UserID: "u1"},
		"d2": {ID: "d2", Name: "other.example", UserID: "u2"},
	}}
	RegisterBotChallengeExclusionRoutes(r.Group(""), BotChallengeExclusionHandlerConfig{Domains: domains, Exclusions: store})
	return r
}

func TestBotChallengeExclusions_AdminOnly(t *testing.T) {
	store := &botExclStore{}
	r := newBotExclRouter(store, false) // the domain's owner, not an admin
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/domains/d1/bot-challenge-exclusions", ""},
		{http.MethodPost, "/domains/d1/bot-challenge-exclusions", `{"path_prefix":"/api/"}`},
		{http.MethodDelete, "/domains/d1/bot-challenge-exclusions/x", ""},
	} {
		if rec := doReq(t, r, c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as owner = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if len(store.rows) != 0 {
		t.Fatalf("a non-admin added %d rows", len(store.rows))
	}
}

func TestBotChallengeExclusions_AddListDelete(t *testing.T) {
	store := &botExclStore{}
	r := newBotExclRouter(store, true)

	rec := doReq(t, r, http.MethodPost, "/domains/d1/bot-challenge-exclusions",
		`{"path_prefix":" /api/endpoint ","methods":["post","get","POST"],"note":"mobile app"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created BotChallengeExclusionView
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.PathPrefix != "/api/endpoint" || fmt.Sprint(created.Methods) != "[GET POST]" || created.Note != "mobile app" {
		t.Fatalf("created = %+v", created)
	}
	if store.rows[0].Methods != "GET,POST" {
		t.Fatalf("stored methods = %q, want GET,POST", store.rows[0].Methods)
	}

	// The same path and methods again: a conflict, not a second row.
	rec = doReq(t, r, http.MethodPost, "/domains/d1/bot-challenge-exclusions", `{"path_prefix":"/api/endpoint","methods":["GET","POST"]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409", rec.Code)
	}

	rec = doReq(t, r, http.MethodGet, "/domains/d1/bot-challenge-exclusions", "")
	var list struct {
		Data  []BotChallengeExclusionView `json:"data"`
		Total int                         `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || list.Total != 1 || list.Data[0].ID != created.ID {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}

	// Another domain's page cannot delete it by its id.
	if rec := doReq(t, r, http.MethodDelete, "/domains/d2/bot-challenge-exclusions/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete via another domain = %d, want 404", rec.Code)
	}
	if rec := doReq(t, r, http.MethodDelete, "/domains/d1/bot-challenge-exclusions/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if len(store.rows) != 0 {
		t.Fatalf("rows left: %+v", store.rows)
	}
}

func TestBotChallengeExclusions_RefusesBadInput(t *testing.T) {
	store := &botExclStore{}
	r := newBotExclRouter(store, true)
	for _, body := range []string{
		`{"path_prefix":"/a\" || true || \""}`,
		`{"path_prefix":"/api?x=1"}`,
		`{"path_prefix":"/"}`,
		`{"path_prefix":"api"}`,
		`{"path_prefix":"/api/../admin"}`,
		`{"path_prefix":"/api","methods":["TRACE"]}`,
		`{"path_prefix":"/api","note":"two\nlines"}`,
	} {
		if rec := doReq(t, r, http.MethodPost, "/domains/d1/bot-challenge-exclusions", body); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, rec.Code)
		}
	}
	if len(store.rows) != 0 {
		t.Fatalf("bad input stored %d rows", len(store.rows))
	}
	if rec := doReq(t, r, http.MethodGet, "/domains/nope/bot-challenge-exclusions", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown domain = %d, want 404", rec.Code)
	}
}

func TestBotChallengeExclusions_PerDomainCap(t *testing.T) {
	store := &botExclStore{}
	for i := 0; i < 50; i++ {
		store.rows = append(store.rows, models.BotChallengeExclusion{ID: fmt.Sprint(i), DomainID: "d1", PathPrefix: fmt.Sprintf("/p%d", i)})
	}
	r := newBotExclRouter(store, true)
	if rec := doReq(t, r, http.MethodPost, "/domains/d1/bot-challenge-exclusions", `{"path_prefix":"/one-more"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("51st path = %d, want 400", rec.Code)
	}
	if rec := doReq(t, r, http.MethodPost, "/domains/d2/bot-challenge-exclusions", `{"path_prefix":"/one-more"}`); rec.Code != http.StatusCreated {
		t.Fatalf("another domain's first path = %d, want 201", rec.Code)
	}
}
