package api

// GH #1238 — admin rename endpoint. The rename logic itself is covered in the
// userops package; these pin the handler's contract: the JAB-380 step-up gate,
// request validation, and that a userops refusal is surfaced with its message.

import (
	"context"
	"encoding/json"
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

// --- minimal mocks (embed the interface, override only what rename touches) ---

type renameUsersRepo struct {
	repository.UserRepository
	byID   *models.User
	byName *models.User
}

func (r renameUsersRepo) FindByID(context.Context, string) (*models.User, error) {
	if r.byID == nil {
		return nil, repository.ErrNotFound
	}
	return r.byID, nil
}

func (r renameUsersRepo) FindByUsername(context.Context, string) (*models.User, error) {
	if r.byName == nil {
		return nil, repository.ErrNotFound
	}
	return r.byName, nil
}

type renameFtpRepo struct {
	repository.FtpAccountRepository
	count int64
}

func (r renameFtpRepo) CountByUserID(context.Context, string) (int64, error) { return r.count, nil }

type renameStubAgent struct{}

func (renameStubAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	return []byte("{}"), nil
}

func rsp(s string) *string { return &s }

func renameCtx(claims *auth.AccessClaims, id, body string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/"+id+"/rename", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: id}}
	if claims != nil {
		ginctx.SetClaims(c, claims)
	}
	return c, w
}

func recentAdmin() *auth.AccessClaims {
	return &auth.AccessClaims{UserID: "admin1", IsAdmin: true, Source: auth.SourceKratos, AuthenticatedAt: time.Now().Add(-1 * time.Minute)}
}

func TestRename_StepUpRequiredWhenStale(t *testing.T) {
	stale := &auth.AccessClaims{UserID: "admin1", IsAdmin: true, Source: auth.SourceKratos, AuthenticatedAt: time.Now().Add(-2 * time.Hour)}
	h := &userHandler{cfg: UserHandlerConfig{}} // KratosClient nil: stale fast-path → 403
	c, w := renameCtx(stale, "u1", `{"new_username":"bob"}`)
	h.rename(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "stepup_required")
}

func TestRename_InvalidBody(t *testing.T) {
	h := &userHandler{cfg: UserHandlerConfig{}}
	c, w := renameCtx(recentAdmin(), "u1", `not json`)
	h.rename(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_body")
}

func TestRename_SurfacesFtpRefusal(t *testing.T) {
	uid := uint32(1001)
	target := &models.User{ID: "u1", Username: rsp("alice"), LinuxUID: &uid}
	h := &userHandler{cfg: UserHandlerConfig{
		Repo:        renameUsersRepo{byID: target},
		Agent:       renameStubAgent{},
		FtpAccounts: renameFtpRepo{count: 2},
	}}
	c, w := renameCtx(recentAdmin(), "u1", `{"new_username":"bob"}`)
	h.rename(c)
	// Refused before the agent runs → 422 with the user-actionable reason.
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "rename_failed")
	assert.Contains(t, w.Body.String(), "FTP")
}

func TestRename_UserNotFound(t *testing.T) {
	h := &userHandler{cfg: UserHandlerConfig{Repo: renameUsersRepo{}}} // byID nil → not found
	c, w := renameCtx(recentAdmin(), "ghost", `{"new_username":"bob"}`)
	h.rename(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// GH #1238 stub.
func (r renameUsersRepo) UpdateShadowDBUsernames(context.Context, string, *string, *string) error {
	return nil
}
