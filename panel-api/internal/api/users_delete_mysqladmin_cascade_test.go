package api_test

// Deleting a user must also drop the per-user MariaDB shadow-admin account
// (<osuser>_mysqladmin). It is not a database_users row, so the generic
// db_user.drop loop misses it — leaving an orphaned MySQL login with a valid
// password after the account is gone (found live: ~20 orphans on a box).

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// dbUserDropAgent records every db_user.drop name (thread-safe: the cascade
// may fire from background goroutines).
type dbUserDropAgent struct {
	mu   sync.Mutex
	seen []string
}

func (a *dbUserDropAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd == "db_user.drop" {
		if m, ok := params.(map[string]any); ok {
			if s, ok := m["db_user_name"].(string); ok {
				a.mu.Lock()
				a.seen = append(a.seen, s)
				a.mu.Unlock()
			}
		}
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (a *dbUserDropAgent) dropped(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.seen {
		if s == name {
			return true
		}
	}
	return false
}

func TestUserDelete_DropsMysqladminShadow(t *testing.T) {
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	victim := makeUser(t, "victim@example.com", false, "victimpassword")
	uname := "victim"
	victim.Username = &uname
	repo.seed(victim)

	ag := &dbUserDropAgent{}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	claims := &auth.AccessClaims{UserID: admin.ID, IsAdmin: true}
	g := r.Group("/api/v1", func(c *gin.Context) {
		ginctx.SetClaims(c, claims)
		c.Next()
	})
	api.RegisterUserRoutes(g, api.UserHandlerConfig{
		Repo:  repo,
		Agent: ag,
	})

	rec := doJSON(t, r, http.MethodDelete, "/api/v1/users/"+victim.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.True(t, ag.dropped("victim_mysqladmin"),
		"user delete must drop the <osuser>_mysqladmin shadow account")
}
