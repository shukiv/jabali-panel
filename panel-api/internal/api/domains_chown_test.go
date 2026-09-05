package api

// GH #1238 — admin change-owner endpoint. The chown logic itself is covered in
// the userops package; these pin the handler's contract: the JAB-380 step-up
// gate, the fail-closed AppInstalls guard, request validation, not-found paths,
// and that a userops refusal is surfaced with its message.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- minimal mocks (embed the interface, override only what chown touches) ---

type chownDomainsRepo struct {
	repository.DomainRepository
	byID          *models.Domain
	transferErr   error
	transferCalls int
}

func (r *chownDomainsRepo) FindByID(context.Context, string) (*models.Domain, error) {
	if r.byID == nil {
		return nil, repository.ErrNotFound
	}
	return r.byID, nil
}

func (r *chownDomainsRepo) TransferOwner(context.Context, string, string, string) error {
	r.transferCalls++
	return r.transferErr
}

type chownUsersRepo struct {
	repository.UserRepository
	byID map[string]*models.User
}

func (r chownUsersRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

type chownAppInstallsRepo struct {
	repository.ApplicationInstallRepository
	install *models.ApplicationInstall
}

func (r chownAppInstallsRepo) FindByDomainID(context.Context, string) (*models.ApplicationInstall, error) {
	return r.install, nil
}

func chownCtx(claims *auth.AccessClaims, id, body string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/domains/"+id+"/chown", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: id}}
	if claims != nil {
		ginctx.SetClaims(c, claims)
	}
	return c, w
}

func TestChown_StepUpRequiredWhenStale(t *testing.T) {
	stale := &auth.AccessClaims{UserID: "admin1", IsAdmin: true, Source: auth.SourceKratos, AuthenticatedAt: time.Now().Add(-2 * time.Hour)}
	h := &domainHandler{cfg: DomainHandlerConfig{}} // KratosClient nil: stale fast-path → 403
	c, w := chownCtx(stale, "d1", `{"new_owner_id":"u2"}`)
	h.chown(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "stepup_required")
}

func TestChown_AppInstallsNilFailsClosed(t *testing.T) {
	// AppInstalls unwired must 503, never silently skip the cross-tenant refusal.
	h := &domainHandler{cfg: DomainHandlerConfig{}}
	c, w := chownCtx(recentAdmin(), "d1", `{"new_owner_id":"u2"}`)
	h.chown(c)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "chown_unavailable")
}

func TestChown_InvalidBody(t *testing.T) {
	h := &domainHandler{cfg: DomainHandlerConfig{AppInstalls: chownAppInstallsRepo{}}}
	c, w := chownCtx(recentAdmin(), "d1", `not json`)
	h.chown(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_body")
}

func TestChown_MissingNewOwner(t *testing.T) {
	h := &domainHandler{cfg: DomainHandlerConfig{AppInstalls: chownAppInstallsRepo{}}}
	c, w := chownCtx(recentAdmin(), "d1", `{}`)
	h.chown(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "new_owner_id")
}

func TestChown_DomainNotFound(t *testing.T) {
	h := &domainHandler{cfg: DomainHandlerConfig{
		AppInstalls: chownAppInstallsRepo{},
		Domains:     &chownDomainsRepo{}, // byID nil → not found
	}}
	c, w := chownCtx(recentAdmin(), "ghost", `{"new_owner_id":"u2"}`)
	h.chown(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "domain not found")
}

func TestChown_OwnerNotFound(t *testing.T) {
	dom := &models.Domain{ID: "d1", UserID: "old", DocRoot: "/home/alice/example.com", Name: "example.com"}
	h := &domainHandler{cfg: DomainHandlerConfig{
		AppInstalls: chownAppInstallsRepo{},
		Domains:     &chownDomainsRepo{byID: dom},
		Users:       chownUsersRepo{byID: map[string]*models.User{}}, // new owner absent
	}}
	c, w := chownCtx(recentAdmin(), "d1", `{"new_owner_id":"ghost"}`)
	h.chown(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "new owner not found")
}

func TestChown_SurfacesAppInstallRefusal(t *testing.T) {
	uid := uint32(1002)
	dom := &models.Domain{ID: "d1", UserID: "old", DocRoot: "/home/alice/example.com", Name: "example.com"}
	newOwner := &models.User{ID: "new", Username: rsp("bob"), LinuxUID: &uid}
	h := &domainHandler{cfg: DomainHandlerConfig{
		// Non-WP app: the refusal is app-type-agnostic — FindByDomainID queries
		// the unified application_installs table with no app_type filter, so any
		// catalog app (Drupal, Moodle, …) is covered, not just WordPress.
		AppInstalls: chownAppInstallsRepo{install: &models.ApplicationInstall{AppType: "drupal"}},
		Domains:     &chownDomainsRepo{byID: dom},
		Users:       chownUsersRepo{byID: map[string]*models.User{"new": newOwner}},
		Agent:       renameStubAgent{},
	}}
	c, w := chownCtx(recentAdmin(), "d1", `{"new_owner_id":"new"}`)
	h.chown(c)
	// Refused before the agent moves anything → 422 with the user-actionable reason.
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "chown_failed")
	assert.Contains(t, w.Body.String(), "app install")
}

func TestChown_HappyPath(t *testing.T) {
	uid := uint32(1002)
	dom := &models.Domain{ID: "d1", UserID: "old", DocRoot: "/home/alice/example.com", Name: "example.com"}
	oldOwner := &models.User{ID: "old", Username: rsp("alice")}
	newOwner := &models.User{ID: "new", Username: rsp("bob"), LinuxUID: &uid}
	domains := &chownDomainsRepo{byID: dom}
	h := &domainHandler{cfg: DomainHandlerConfig{
		AppInstalls: chownAppInstallsRepo{}, // no install → not refused
		Domains:     domains,
		Users:       chownUsersRepo{byID: map[string]*models.User{"new": newOwner, "old": oldOwner}},
		Agent:       renameStubAgent{},
	}}
	c, w := chownCtx(recentAdmin(), "d1", `{"new_owner_id":"new"}`)
	h.chown(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, domains.transferCalls)
	assert.Contains(t, w.Body.String(), `"user_id":"new"`)
	assert.Contains(t, w.Body.String(), "/home/bob/example.com")
}
