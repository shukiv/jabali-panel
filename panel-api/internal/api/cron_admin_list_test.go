package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- minimal stubs ----------------------------------------------------------

type stubCronJobsForAdmin struct{ rows []*models.CronJob }

func (s *stubCronJobsForAdmin) Create(context.Context, *models.CronJob) error { return nil }
func (s *stubCronJobsForAdmin) FindByID(_ context.Context, id string) (*models.CronJob, error) {
	for _, j := range s.rows {
		if j.ID == id {
			return j, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (s *stubCronJobsForAdmin) ListByUserID(_ context.Context, uid string) ([]*models.CronJob, error) {
	out := make([]*models.CronJob, 0)
	for _, j := range s.rows {
		if j.UserID == uid {
			out = append(out, j)
		}
	}
	return out, nil
}
func (s *stubCronJobsForAdmin) ListAll(context.Context) ([]*models.CronJob, error) {
	return s.rows, nil
}
func (s *stubCronJobsForAdmin) Update(context.Context, *models.CronJob) error { return nil }
func (s *stubCronJobsForAdmin) UpdateStatus(context.Context, string, time.Time, int, string) error {
	return nil
}
func (s *stubCronJobsForAdmin) Delete(context.Context, string) error { return nil }

// --- regression: GH#127 admin list-all endpoint -----------------------------

func TestAdminCronListAll_ReturnsAllJobsWithUsernameJoined(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		c.Next()
	})

	uName := "alice"
	users := &userRepoAdapterForCronAdminTest{
		users: []models.User{
			{ID: "u1", Username: &uName},
		},
	}
	cron := &stubCronJobsForAdmin{rows: []*models.CronJob{
		{ID: "j1", UserID: "u1", Name: "backup", Command: "rsync -a", Schedule: "0 3 * * *", Enabled: true},
		{ID: "j2", UserID: "u2", Name: "purge", Command: "find /tmp -mtime +1 -delete", Schedule: "0 4 * * *", Enabled: false},
	}}

	RegisterCronRoutes(v1, CronHandlerConfig{
		CronJobs: cron,
		Users:    users,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/cron", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []struct {
			ID       string `json:"id"`
			UserID   string `json:"user_id"`
			Username string `json:"username"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 jobs, got %d: %+v", len(resp.Items), resp.Items)
	}
	var found bool
	for _, j := range resp.Items {
		if j.ID == "j1" && j.Username == "alice" {
			found = true
		}
	}
	if !found {
		t.Errorf("j1's username was not denormalised; got: %+v", resp.Items)
	}
}

func TestAdminCronListAll_NonAdminGets403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: false})
		c.Next()
	})

	RegisterCronRoutes(v1, CronHandlerConfig{
		CronJobs: &stubCronJobsForAdmin{},
		Users:    &userRepoAdapterForCronAdminTest{},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/cron", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for tenant; got %d body=%s", w.Code, w.Body.String())
	}
}

// userRepoAdapterForCronAdminTest is a UserRepository-shaped stub that
// only implements FindByIDs meaningfully. All other methods return
// zero values; widening the surface here as the UserRepository
// interface grows is cheaper than building a real mock.
type userRepoAdapterForCronAdminTest struct{ users []models.User }

func (u *userRepoAdapterForCronAdminTest) Create(context.Context, *models.User) error { return nil }
func (u *userRepoAdapterForCronAdminTest) FindByID(context.Context, string) (*models.User, error) {
	return nil, repository.ErrNotFound
}
func (u *userRepoAdapterForCronAdminTest) FindByEmail(context.Context, string) (*models.User, error) {
	return nil, repository.ErrNotFound
}
func (u *userRepoAdapterForCronAdminTest) FindByUsername(context.Context, string) (*models.User, error) {
	return nil, repository.ErrNotFound
}
func (u *userRepoAdapterForCronAdminTest) FindByKratosIdentityID(context.Context, string) (*models.User, error) {
	return nil, repository.ErrNotFound
}
func (u *userRepoAdapterForCronAdminTest) FindByIDs(_ context.Context, ids []string) ([]models.User, error) {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	out := make([]models.User, 0)
	for _, x := range u.users {
		if _, ok := set[x.ID]; ok {
			out = append(out, x)
		}
	}
	return out, nil
}
func (u *userRepoAdapterForCronAdminTest) List(context.Context, repository.ListOptions) ([]models.User, int64, error) {
	return u.users, int64(len(u.users)), nil
}
func (u *userRepoAdapterForCronAdminTest) Update(context.Context, *models.User) error { return nil }
func (u *userRepoAdapterForCronAdminTest) LinkKratosIdentity(context.Context, string, string) error {
	return nil
}

func (u *userRepoAdapterForCronAdminTest) UpdateCLIPHPVersion(context.Context, string, *string) error {
	return nil
}
func (u *userRepoAdapterForCronAdminTest) SetAdmin(context.Context, string, bool) error { return nil }
func (u *userRepoAdapterForCronAdminTest) CountAdmins(context.Context) (int64, error) {
	return 0, nil
}
func (u *userRepoAdapterForCronAdminTest) FindAdminsByEmail(context.Context) ([]*models.User, error) {
	return nil, nil
}
func (u *userRepoAdapterForCronAdminTest) SetSuspended(context.Context, string, bool, string) error {
	return nil
}
func (u *userRepoAdapterForCronAdminTest) Delete(context.Context, string) error { return nil }

func (r *userRepoAdapterForCronAdminTest) UpdateUsername(context.Context, string, string) error {
	return nil
}
