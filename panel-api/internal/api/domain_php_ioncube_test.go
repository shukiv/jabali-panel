package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- minimal repo fakes (embed the interface, override what the handler uses) ---

type icDomainRepo struct {
	repository.DomainRepository
	dom *models.Domain
}

func (r *icDomainRepo) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if r.dom == nil || r.dom.ID != id {
		return nil, repository.ErrNotFound
	}
	return r.dom, nil
}

type icPoolRepo struct {
	repository.PHPPoolRepository
	pool *models.PHPPool
}

func (r *icPoolRepo) FindByID(_ context.Context, _ string) (*models.PHPPool, error) {
	return r.pool, nil
}
func (r *icPoolRepo) FindByUserID(_ context.Context, _ string) (*models.PHPPool, error) {
	return r.pool, nil
}

type icUserRepo struct {
	repository.UserRepository
	user *models.User
}

func (r *icUserRepo) FindByID(_ context.Context, _ string) (*models.User, error) {
	return r.user, nil
}

func strp(s string) *string { return &s }

func icRouter(cfg DomainIonCubeHandlerConfig, userID string, admin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: admin})
		c.Next()
	})
	RegisterDomainIonCubeRoutes(v1, cfg)
	return r
}

func doPut(r *gin.Engine, id string, enabled bool) *httptest.ResponseRecorder {
	body := `{"enabled":` + map[bool]string{true: "true", false: "false"}[enabled] + `}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/domains/"+id+"/php-ioncube", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func baseCfg(mock agent.AgentInterface) DomainIonCubeHandlerConfig {
	return DomainIonCubeHandlerConfig{
		Domains:  &icDomainRepo{dom: &models.Domain{ID: "dom1", UserID: "owner1", PHPPoolID: strp("pool1")}},
		PHPPools: &icPoolRepo{pool: &models.PHPPool{PHPVersion: "8.4"}},
		Users:    &icUserRepo{user: &models.User{Username: strp("arizote")}},
		Agent:    mock,
	}
}

// TestIonCube_Toggle_HappyPath: owner enables → pool_set called with the
// resolved username + version.
func TestIonCube_Toggle_HappyPath(t *testing.T) {
	mock := agent.NewMockClient().On("php.ioncube.pool_set", map[string]any{"username": "arizote", "version": "8.4", "enabled": true, "changed": true})
	r := icRouter(baseCfg(mock), "owner1", false)
	rec := doPut(r, "dom1", true)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "arizote", body["username"])
	assert.Equal(t, "8.4", body["version"])
}

// TestIonCube_Toggle_ForbiddenForNonOwner: a tenant cannot toggle another's domain.
func TestIonCube_Toggle_ForbiddenForNonOwner(t *testing.T) {
	mock := agent.NewMockClient() // must NOT be called
	r := icRouter(baseCfg(mock), "intruder", false)
	rec := doPut(r, "dom1", true)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestIonCube_Toggle_AdminCanToggleAny: admin may toggle any domain.
func TestIonCube_Toggle_AdminCanToggleAny(t *testing.T) {
	mock := agent.NewMockClient().On("php.ioncube.pool_set", map[string]any{"ok": true})
	r := icRouter(baseCfg(mock), "someadmin", true)
	rec := doPut(r, "dom1", true)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestIonCube_Toggle_NoVersion409: a domain whose owner has no pool → 409.
func TestIonCube_Toggle_NoVersion409(t *testing.T) {
	cfg := baseCfg(agent.NewMockClient())
	cfg.Domains = &icDomainRepo{dom: &models.Domain{ID: "dom1", UserID: "owner1"}} // no PHPPoolID
	cfg.PHPPools = &icPoolRepo{pool: nil}
	r := icRouter(cfg, "owner1", false)
	rec := doPut(r, "dom1", true)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// TestIonCube_Toggle_LoaderNotInstalled409: the agent's precondition
// (loader not installed for the version) surfaces as 409.
func TestIonCube_Toggle_LoaderNotInstalled409(t *testing.T) {
	mock := agent.NewMockClient().OnError("php.ioncube.pool_set", &agent.AgentError{
		Code:    agent.CodeFailedPrecondition,
		Message: "ionCube Loader is not installed for PHP 8.4",
	})
	r := icRouter(baseCfg(mock), "owner1", false)
	rec := doPut(r, "dom1", true)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// TestIonCube_Toggle_NotFound: unknown domain → 404.
func TestIonCube_Toggle_NotFound(t *testing.T) {
	r := icRouter(baseCfg(agent.NewMockClient()), "owner1", false)
	rec := doPut(r, "nope", true)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
