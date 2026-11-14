package api

// GH #2505: the backups list handlers read the page size from ?page_size=
// (the repo-wide list convention every UI caller sends), not ?limit=. The
// requested size was ignored and the default applied, so lists showed at
// most 25 rows. These pin the paging each list passes to the jobs repo:
// page_size honoured, an over-max size clamped DOWN (GH #1796), and the
// legacy ?limit= still accepted for older clients.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// pagingJobsRepo records the paging each list query was called with.
type pagingJobsRepo struct {
	repository.BackupJobRepository
	runsLimit, runsOffset       int
	manualLimit, manualOffset   int
	forUserID                   string
	forUserLimit, forUserOffset int
	allLimit, allOffset         int
}

func (r *pagingJobsRepo) ListRuns(_ context.Context, limit, offset int) ([]repository.BackupRunSummary, int64, error) {
	r.runsLimit, r.runsOffset = limit, offset
	return nil, 0, nil
}

func (r *pagingJobsRepo) ListManual(_ context.Context, limit, offset int) ([]models.BackupJob, int64, error) {
	r.manualLimit, r.manualOffset = limit, offset
	return nil, 0, nil
}

func (r *pagingJobsRepo) ListForUser(_ context.Context, userID string, limit, offset int) ([]models.BackupJob, int64, error) {
	r.forUserID, r.forUserLimit, r.forUserOffset = userID, limit, offset
	return nil, 0, nil
}

func (r *pagingJobsRepo) ListAll(_ context.Context, limit, offset int) ([]models.BackupJob, int64, error) {
	r.allLimit, r.allOffset = limit, offset
	return nil, 0, nil
}

func pagingCall(t *testing.T, handler func(*gin.Context), target string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "01J5USER0000000000000000001"})
	handler(c)
	return rec
}

func TestBackupRunsList_Paging(t *testing.T) {
	cases := []struct {
		name  string
		query string
		limit int
	}{
		{"page_size reaches both the runs and the manual query", "?page_size=1000", 1000},
		{"an over-max page_size clamps down to the max", "?page_size=5000", 1000},
		{"no paging params keep the default", "", 25},
		{"a legacy limit without page_size is still honoured", "?limit=40", 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jobs := &pagingJobsRepo{}
			h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs}}

			rec := pagingCall(t, h.listRuns, "/admin/backup-runs"+tc.query)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
			}
			if jobs.runsLimit != tc.limit || jobs.runsOffset != 0 {
				t.Fatalf("ListRuns called with (%d, %d), want (%d, 0)", jobs.runsLimit, jobs.runsOffset, tc.limit)
			}
			if jobs.manualLimit != tc.limit || jobs.manualOffset != 0 {
				t.Fatalf("ListManual called with (%d, %d), want (%d, 0)", jobs.manualLimit, jobs.manualOffset, tc.limit)
			}
		})
	}
}

func TestMeBackupsList_Paging(t *testing.T) {
	jobs := &pagingJobsRepo{}
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{Jobs: jobs}}

	rec := pagingCall(t, h.list, "/me/backups?page=3&page_size=10")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if jobs.forUserID != "01J5USER0000000000000000001" {
		t.Fatalf("ListForUser called for %q, want the session user", jobs.forUserID)
	}
	if jobs.forUserLimit != 10 || jobs.forUserOffset != 20 {
		t.Fatalf("ListForUser called with (%d, %d), want (10, 20)", jobs.forUserLimit, jobs.forUserOffset)
	}
}

func TestListAllBackups_Paging(t *testing.T) {
	jobs := &pagingJobsRepo{}
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs}}

	rec := pagingCall(t, h.listAll, "/admin/backups?page_size=200")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if jobs.allLimit != 200 || jobs.allOffset != 0 {
		t.Fatalf("ListAll called with (%d, %d), want (200, 0)", jobs.allLimit, jobs.allOffset)
	}
}
