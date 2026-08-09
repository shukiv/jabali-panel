package api_test

// Deleting a user CASCADEs its databases/database_users rows away, so those
// rows are the only remaining handle on the MariaDB objects while the drops
// are still in flight. If a drop fails and the user row goes anyway, the
// schema and login survive on the host unnamed: absent from `jabali db list`,
// excluded from every backup, grants still live.
//
// The cascade used to log the failure and continue ("best-effort: never
// blocks the user delete"). It now aborts before the point of no return, so
// the operator can retry once the agent is healthy — the domain/docker/ACL
// steps ahead of it are idempotent and re-list empty on the second run.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// cascadeDropAgent fails the named command; everything else succeeds.
type cascadeDropAgent struct {
	failCmd string
	mu      sync.Mutex
	calls   []string
}

func (a *cascadeDropAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.mu.Lock()
	a.calls = append(a.calls, cmd)
	a.mu.Unlock()
	if cmd == a.failCmd {
		return nil, errors.New("agent unavailable")
	}
	return json.RawMessage(`{"ok":true}`), nil
}

// oneDatabaseRepo reports a single tenant database for any user.
type oneDatabaseRepo struct {
	repository.DatabaseRepository
	name string
}

func (r *oneDatabaseRepo) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Database, int64, error) {
	return []models.Database{{ID: "db-1", UserID: userID, Name: r.name}}, 1, nil
}

// oneDatabaseUserRepo reports a single tenant MySQL login for any user.
type oneDatabaseUserRepo struct {
	repository.DatabaseUserRepository
	username string
}

func (r *oneDatabaseUserRepo) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	return []models.DatabaseUser{{ID: "dbuser-1", UserID: userID, Username: r.username}}, 1, nil
}

func runUserDelete(t *testing.T, cfg api.UserHandlerConfig, adminID, victimID string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	claims := &auth.AccessClaims{UserID: adminID, IsAdmin: true}
	g := r.Group("/api/v1", func(c *gin.Context) {
		ginctx.SetClaims(c, claims)
		c.Next()
	})
	api.RegisterUserRoutes(g, cfg)
	return doJSON(t, r, http.MethodDelete, "/api/v1/users/"+victimID, nil).Code
}

func seedAdminAndVictim(t *testing.T) (*memUserRepo, *models.User, *models.User) {
	t.Helper()
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	victim := makeUser(t, "victim@example.com", false, "victimpassword")
	uname := "victim"
	victim.Username = &uname
	repo.seed(victim)
	return repo, admin, victim
}

func TestUserDelete_AbortsWhenDatabaseDropFails(t *testing.T) {
	repo, admin, victim := seedAdminAndVictim(t)

	code := runUserDelete(t, api.UserHandlerConfig{
		Repo:          repo,
		Agent:         &cascadeDropAgent{failCmd: "db.drop"},
		Databases:     &oneDatabaseRepo{name: "victim_shop"},
		DatabaseUsers: &oneDatabaseUserRepo{username: "victim_shop"},
	}, admin.ID, victim.ID)

	require.Equal(t, http.StatusBadGateway, code,
		"a failed db.drop must abort the delete, not report success")

	got, err := repo.FindByID(context.Background(), victim.ID)
	require.NoError(t, err, "the user row must survive — it is what keeps the databases row addressable")
	require.NotNil(t, got)
}

func TestUserDelete_AbortsWhenDatabaseUserDropFails(t *testing.T) {
	repo, admin, victim := seedAdminAndVictim(t)

	code := runUserDelete(t, api.UserHandlerConfig{
		Repo:          repo,
		Agent:         &cascadeDropAgent{failCmd: "db_user.drop"},
		Databases:     &oneDatabaseRepo{name: "victim_shop"},
		DatabaseUsers: &oneDatabaseUserRepo{username: "victim_shop"},
	}, admin.ID, victim.ID)

	require.Equal(t, http.StatusBadGateway, code,
		"a failed db_user.drop must abort the delete")

	got, err := repo.FindByID(context.Background(), victim.ID)
	require.NoError(t, err, "the user row must survive so the orphaned login stays reachable")
	require.NotNil(t, got)
}

// The per-user shadow admin (<osuser>_mysqladmin) is not a database_users
// row, so the loops above never see it. Leaving Databases/DatabaseUsers nil
// isolates it: the only db_user.drop the handler issues is the shadow one.
func TestUserDelete_AbortsWhenMysqladminShadowDropFails(t *testing.T) {
	repo, admin, victim := seedAdminAndVictim(t)

	code := runUserDelete(t, api.UserHandlerConfig{
		Repo:  repo,
		Agent: &cascadeDropAgent{failCmd: "db_user.drop"},
	}, admin.ID, victim.ID)

	require.Equal(t, http.StatusBadGateway, code,
		"a failed <osuser>_mysqladmin drop must abort — that login has a valid password and no row would be left to find it")

	got, err := repo.FindByID(context.Background(), victim.ID)
	require.NoError(t, err, "the user row must survive")
	require.NotNil(t, got)
}

func TestUserDelete_ProceedsWhenDropsSucceed(t *testing.T) {
	repo, admin, victim := seedAdminAndVictim(t)

	code := runUserDelete(t, api.UserHandlerConfig{
		Repo:          repo,
		Agent:         &cascadeDropAgent{},
		Databases:     &oneDatabaseRepo{name: "victim_shop"},
		DatabaseUsers: &oneDatabaseUserRepo{username: "victim_shop"},
	}, admin.ID, victim.ID)

	require.Equal(t, http.StatusNoContent, code,
		"the abort must not fire when every host-side drop succeeded")

	_, err := repo.FindByID(context.Background(), victim.ID)
	require.Error(t, err, "the user row should be gone on the success path")
}
