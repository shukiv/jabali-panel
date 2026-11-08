package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1625: what the Aliases tab and the Web Domains list read.

type aliasListFake struct {
	repository.WebDomainAliasRepository
	byDomain map[string][]models.WebDomainAlias
}

func (f aliasListFake) ListByDomain(_ context.Context, id string) ([]models.WebDomainAlias, error) {
	return f.byDomain[id], nil
}

func (f aliasListFake) ListByDomainIDs(_ context.Context, ids []string) ([]models.WebDomainAlias, error) {
	var out []models.WebDomainAlias
	for _, id := range ids {
		out = append(out, f.byDomain[id]...)
	}
	return out, nil
}

// The alias list carries each alias's ownership record, so a tenant sees the
// TXT record to publish next to the alias it proves.
func TestAliasList_CarriesOwnershipRecord(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "example.com", UserID: "u1"}
	token := strings.Repeat("a", 64)
	h := &domainAliasHandler{cfg: DomainAliasHandlerConfig{
		Domains: aliasOwnershipDomains{byID: dom},
		Aliases: aliasListFake{byDomain: map[string][]models.WebDomainAlias{"d1": {
			{ID: "a1", DomainID: "d1", Hostname: "shop.brandy.io",
				OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipPending, OwnershipToken: token}},
			{ID: "a2", DomainID: "d1", Hostname: "blog.example.com",
				OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified, OwnershipMethod: models.OwnershipMethodParent}},
		}}},
	}}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	r.GET("/domains/:id/aliases", h.list)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/domains/d1/aliases", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			Hostname  string `json:"hostname"`
			Ownership struct {
				Status         string `json:"status"`
				Method         string `json:"method"`
				ChallengeName  string `json:"challenge_name"`
				ChallengeValue string `json:"challenge_value"`
			} `json:"ownership"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("want 2 aliases, got %d: %s", len(resp.Data), rec.Body.String())
	}
	pending := resp.Data[0].Ownership
	if pending.Status != models.OwnershipPending ||
		pending.ChallengeName != models.OwnershipChallengePrefix+"shop.brandy.io" ||
		pending.ChallengeValue != models.OwnershipValuePrefix+token {
		t.Errorf("pending alias ownership = %+v", pending)
	}
	if v := resp.Data[1].Ownership; v.Status != models.OwnershipVerified || v.Method != models.OwnershipMethodParent {
		t.Errorf("verified alias ownership = %+v", v)
	}
}

// The create response carries the record too: the tab shows it right after Add.
func TestAliasCreate_ResponseCarriesOwnershipRecord(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "example.com", UserID: "u1",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}}
	h := &domainAliasHandler{cfg: DomainAliasHandlerConfig{
		Domains:   aliasOwnershipDomains{aliasTestDomains: aliasTestDomains{names: map[string]*models.Domain{"example.com": dom}}, byID: dom},
		Aliases:   &aliasOwnershipAliases{},
		Ownership: ownershipStub{require: true},
	}}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	r.POST("/domains/:id/aliases", h.create)
	req := httptest.NewRequest(http.MethodPost, "/domains/d1/aliases", strings.NewReader(`{"hostname":"shop.brandy.io"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Ownership struct {
			Status         string `json:"status"`
			ChallengeName  string `json:"challenge_name"`
			ChallengeValue string `json:"challenge_value"`
		} `json:"ownership"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Ownership.Status != models.OwnershipPending ||
		resp.Ownership.ChallengeName != models.OwnershipChallengePrefix+"shop.brandy.io" ||
		!strings.HasPrefix(resp.Ownership.ChallengeValue, models.OwnershipValuePrefix) ||
		len(resp.Ownership.ChallengeValue) <= len(models.OwnershipValuePrefix) {
		t.Errorf("create response ownership = %+v", resp.Ownership)
	}
}

// GET /domains puts each domain's aliases on its row; a domain without any
// ships no key, and an unset status reads as pending.
func TestDomainList_EmbedsAliasSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
		c.Next()
	})
	base := newMockDomainRepo()
	base.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "a.com"})
	base.Create(context.Background(), &models.Domain{ID: "d2", UserID: "u1", Name: "b.com"})
	domains := &domainListWithSeedData{mockDomainRepo: base}
	aliases := aliasListFake{byDomain: map[string][]models.WebDomainAlias{"d1": {
		{ID: "a1", DomainID: "d1", Hostname: "a.net",
			OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}},
		{ID: "a2", DomainID: "d1", Hostname: "www.a.net"},
	}}}
	RegisterDomainRoutes(v1, DomainHandlerConfig{Domains: domains, WebDomainAliases: aliases})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/domains", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			ID      string            `json:"id"`
			Aliases []json.RawMessage `json:"aliases"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string][]string{}
	for _, row := range resp.Data {
		for _, a := range row.Aliases {
			got[row.ID] = append(got[row.ID], string(a))
		}
	}
	want := []string{
		`{"id":"a1","hostname":"a.net","ownership_status":"verified"}`,
		`{"id":"a2","hostname":"www.a.net","ownership_status":"pending"}`,
	}
	if strings.Join(got["d1"], " ") != strings.Join(want, " ") {
		t.Errorf("d1 aliases = %v, want %v", got["d1"], want)
	}
	if len(got["d2"]) != 0 {
		t.Errorf("d2 must ship no aliases, got %v", got["d2"])
	}
	if strings.Count(w.Body.String(), `"aliases"`) != 1 {
		t.Errorf("only d1's row may carry an aliases key: %s", w.Body.String())
	}
}
