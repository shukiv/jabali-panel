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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1686 item 2 — admins edit a tenant's cron job from the admin Cron Jobs
// screen. The edit rides the existing PATCH /cron/:id (fetchAndAuthorize's
// IsAdmin bypass); these tests pin the two things the admin editor relies on.

// The admin list must carry run_as_root: a root job's user_id is the creating
// admin's, so without the flag the editor cannot tell a root job from a tenant
// job and would show tenant copy for a root job.
func TestCronResponse_SurfacesRunAsRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		c.Next()
	})
	RegisterCronRoutes(v1, CronHandlerConfig{
		CronJobs: &stubCronJobsForAdmin{rows: []*models.CronJob{
			{ID: "j-root", UserID: "admin1", Name: "rootjob", Command: "php /root/x.php", Schedule: "0 3 * * *", Enabled: true, RunAsRoot: true},
			{ID: "j-tenant", UserID: "u1", Name: "tenantjob", Command: "php /home/alice/x.php", Schedule: "0 3 * * *", Enabled: true},
		}},
		Users: &userRepoAdapterForCronAdminTest{},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/cron", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(resp.Items))
	}
	want := map[string]bool{"j-root": true, "j-tenant": false}
	for _, item := range resp.Items {
		id, _ := item["id"].(string)
		v, present := item["run_as_root"]
		if !present {
			t.Fatalf("job %s: run_as_root missing from the response: %v", id, item)
		}
		if v != want[id] {
			t.Errorf("job %s: run_as_root = %v, want %v", id, v, want[id])
		}
	}
}

// cronEditUsers resolves only FindByID — the one method cronops.Update's
// Linux-account lookup uses.
type cronEditUsers struct {
	repository.UserRepository
	byID map[string]*models.User
}

func (u cronEditUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if x, ok := u.byID[id]; ok {
		return x, nil
	}
	return nil, repository.ErrNotFound
}

// cronEditDomains resolves only ListByUserID — cronops.ownedTargets.
type cronEditDomains struct {
	repository.DomainRepository
	byUser map[string][]models.Domain
}

func (d cronEditDomains) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	rows := d.byUser[userID]
	return rows, int64(len(rows)), nil
}

// cronEditAgent records the last agent call so the test can see which
// account's targets the apply carried.
type cronEditAgent struct {
	method string
	params cronApplyProbe
}

type cronApplyProbe struct {
	UserID        string   `json:"user_id"`
	Username      string   `json:"username"`
	OwnedDocroots []string `json:"owned_docroots"`
	RunAsRoot     bool     `json:"run_as_root"`
}

func (a *cronEditAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.method = method
	raw, _ := json.Marshal(params)
	_ = json.Unmarshal(raw, &a.params)
	return json.RawMessage(`{"ok":true}`), nil
}

// An admin editing a tenant's job edits it AS THE TENANT's job: owner and
// run_as are immutable on PATCH (a smuggled user_id / run_as_root is dropped),
// and the command is validated against the job owner's docroots — never the
// editing admin's. This is the security posture the admin Edit action relies on.
func TestCronUpdate_AdminEditsTenantJobWithinOwnerTargets(t *testing.T) {
	alice, admin := "alice", "adminuser"
	newRouter := func() (*gin.Engine, *stubCronJobsForAdmin, *cronEditAgent) {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		v1 := r.Group("/api/v1")
		v1.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
			c.Next()
		})
		jobs := &stubCronJobsForAdmin{rows: []*models.CronJob{{
			ID: "j1", UserID: "u1", Name: "old", Command: "php /home/alice/example.com/public_html/old.php",
			Schedule: "0 3 * * *", Enabled: true,
		}}}
		ag := &cronEditAgent{}
		RegisterCronRoutes(v1, CronHandlerConfig{
			CronJobs: jobs,
			Users: cronEditUsers{byID: map[string]*models.User{
				"u1":     {ID: "u1", Username: &alice},
				"admin1": {ID: "admin1", Username: &admin, IsAdmin: true},
			}},
			Domains: cronEditDomains{byUser: map[string][]models.Domain{
				"u1":     {{Name: "example.com", DocRoot: "/home/alice/example.com/public_html"}},
				"admin1": {{Name: "admin.test", DocRoot: "/home/adminuser/admin.test/public_html"}},
			}},
			Agent: ag,
		})
		return r, jobs, ag
	}
	patch := func(r *gin.Engine, body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/cron/j1", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("edit lands on the tenant's job; owner and run_as unchanged", func(t *testing.T) {
		r, jobs, ag := newRouter()
		w := patch(r, map[string]any{
			"name":        "nightly",
			"command":     "php /home/alice/example.com/public_html/cron.php",
			"schedule":    "0 4 * * *",
			"user_id":     "admin1", // not a PATCH field — must be ignored
			"run_as_root": true,     // not a PATCH field — must be ignored
		})
		if w.Code != http.StatusOK {
			t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
		}
		job := jobs.rows[0]
		if job.UserID != "u1" || job.RunAsRoot {
			t.Fatalf("owner/run_as changed by an admin edit: user_id=%q run_as_root=%v", job.UserID, job.RunAsRoot)
		}
		if job.Name != "nightly" || job.Schedule != "0 4 * * *" {
			t.Errorf("edit not persisted: name=%q schedule=%q", job.Name, job.Schedule)
		}
		if ag.method != "cron.apply" || ag.params.Username != "alice" || ag.params.RunAsRoot {
			t.Errorf("apply must run as the tenant: method=%q username=%q run_as_root=%v", ag.method, ag.params.Username, ag.params.RunAsRoot)
		}
		if len(ag.params.OwnedDocroots) != 1 || ag.params.OwnedDocroots[0] != "/home/alice/example.com/public_html" {
			t.Errorf("apply must carry the owner's docroots, got %v", ag.params.OwnedDocroots)
		}
	})

	t.Run("command pointing at the editing admin's docroot is rejected", func(t *testing.T) {
		r, jobs, ag := newRouter()
		w := patch(r, map[string]any{"command": "php /home/adminuser/admin.test/public_html/cron.php"})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400; body=%s", w.Code, w.Body.String())
		}
		var body struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body.Field != "command" || body.Code != "bad_path_arg" {
			t.Errorf("want command/bad_path_arg, got field=%q code=%q", body.Field, body.Code)
		}
		if jobs.rows[0].Command != "php /home/alice/example.com/public_html/old.php" {
			t.Errorf("rejected edit mutated the job: %q", jobs.rows[0].Command)
		}
		if ag.method != "" {
			t.Errorf("rejected edit reached the agent: %q", ag.method)
		}
	})
}
