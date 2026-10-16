package app_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/app"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2159: the Personal API Tokens routes were registered on the v1 group
// before ResolveImpersonation (and the audit, standby and login-IP
// middleware) was added to it. gin applies a group's middleware only to the
// routes registered after the Use, so an admin acting as a user (ADR-0128)
// listed, minted and revoked the ADMIN's own tokens on that user's page. A
// token minted there was an admin token.

const (
	actAsAdminID   = "01ADMIN0000000000000000000"
	actAsTenantID  = "01TENANT000000000000000000"
	actAsGrantID   = "01GRANT0000000000000000000"
	actAsKratosID  = "kratos-admin"
	actAsTenantTok = "01TOKEN0000000000000000000"
)

type actAsUsers struct {
	repository.UserRepository
	admin, tenant *models.User
}

func (f *actAsUsers) FindByKratosIdentityID(_ context.Context, id string) (*models.User, error) {
	if id == actAsKratosID {
		return f.admin, nil
	}
	return nil, repository.ErrNotFound
}

func (f *actAsUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	switch id {
	case f.admin.ID:
		return f.admin, nil
	case f.tenant.ID:
		return f.tenant, nil
	}
	return nil, repository.ErrNotFound
}

type actAsTokens struct {
	repository.UserAPITokenRepository
	mu        sync.Mutex
	listedFor []string
	created   []models.UserAPIToken
	revoked   []string
}

func (f *actAsTokens) ListByUser(_ context.Context, userID string) ([]models.UserAPIToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listedFor = append(f.listedFor, userID)
	return []models.UserAPIToken{}, nil
}

func (f *actAsTokens) Create(_ context.Context, t *models.UserAPIToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, *t)
	return nil
}

func (f *actAsTokens) FindByID(_ context.Context, id string) (*models.UserAPIToken, error) {
	if id != actAsTenantTok {
		return nil, repository.ErrNotFound
	}
	return &models.UserAPIToken{ID: id, UserID: actAsTenantID, Name: "tenant token"}, nil
}

func (f *actAsTokens) Revoke(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, id)
	return nil
}

// newActAsApp builds the real router with an admin Kratos session and one
// live act-as grant (admin → tenant) behind ResolveImpersonation.
func newActAsApp(t *testing.T) (http.Handler, *actAsTokens) {
	t.Helper()

	kratos := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sessions/whoami") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"sess","active":true,"identity":{"id":"`+actAsKratosID+`","traits":{"email":"admin@example.test"}}}`)
	}))
	t.Cleanup(kratos.Close)

	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: true}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	now := time.Now().UTC()
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 8; i++ {
		mock.ExpectQuery(`FROM .impersonation_grants.`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "admin_user_id", "target_user_id", "created_at", "expires_at", "ended_at"}).
				AddRow(actAsGrantID, actAsAdminID, actAsTenantID, now, now.Add(time.Hour), nil))
	}

	tenantName := "tenant1"
	adminName := "admin"
	users := &actAsUsers{
		admin:  &models.User{ID: actAsAdminID, Email: "admin@example.test", IsAdmin: true, Username: &adminName},
		tenant: &models.User{ID: actAsTenantID, Email: "tenant@example.test", Username: &tenantName},
	}
	tokens := &actAsTokens{}

	cfg := config.Defaults()
	cfg.Auth.Kratos.PublicURL = kratos.URL
	cfg.Auth.Kratos.AdminURL = kratos.URL
	r := app.NewWithDeps(cfg, app.Deps{
		DB:            db,
		Users:         users,
		UserAPITokens: tokens,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return r, tokens
}

func actAsRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "panel.example.test"
	req.Header.Set("Origin", "http://panel.example.test")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Jabali-Act-As", actAsGrantID)
	req.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "admin-session"})
	return req
}

func TestMeAPITokens_ActAsServesTheUser(t *testing.T) {
	r, tokens := newActAsApp(t)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, actAsRequest(http.MethodGet, "/api/v1/me/api-tokens", ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{actAsTenantID}, tokens.listedFor,
		"acting as a user, the token page must list that user's tokens, not the admin's")

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, actAsRequest(http.MethodPost, "/api/v1/me/api-tokens", `{"name":"redis","scopes":["write:redis"]}`))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Len(t, tokens.created, 1)
	require.Equal(t, actAsTenantID, tokens.created[0].UserID,
		"acting as a user, a minted token must belong to that user, never to the admin")

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, actAsRequest(http.MethodDelete, "/api/v1/me/api-tokens/"+actAsTenantTok, ""))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Equal(t, []string{actAsTenantTok}, tokens.revoked)
}
