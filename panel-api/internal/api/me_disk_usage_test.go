package api

import (
	"context"
	"encoding/json"
	"fmt"
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

type duUserRepo struct {
	repository.UserRepository
	u *models.User
}

func (r *duUserRepo) FindByID(context.Context, string) (*models.User, error) { return r.u, nil }

type duDomainRepo struct {
	repository.DomainRepository
	doms []models.Domain
}

func (r *duDomainRepo) ListByUserID(context.Context, string, repository.ListOptions) ([]models.Domain, int64, error) {
	return r.doms, int64(len(r.doms)), nil
}

type duMailboxRepo struct {
	repository.MailboxRepository
	mbs []models.Mailbox
}

func (r *duMailboxRepo) ListByDomainID(context.Context, string, repository.ListOptions) ([]models.Mailbox, int64, error) {
	return r.mbs, int64(len(r.mbs)), nil
}

type duDatabaseRepo struct {
	repository.DatabaseRepository
	dbs []models.Database
}

func (r *duDatabaseRepo) ListByUserID(context.Context, string, repository.ListOptions) ([]models.Database, int64, error) {
	return r.dbs, int64(len(r.dbs)), nil
}

// duAgent mocks the agent. Zero value: quota answers (used_kb 1024, limit
// 10240), db.size answers 2048, and files.du ERRORS — so the summary falls
// back to the quota figure (the pre-#1439 number). Set duOK to have files.du
// answer duTotalBytes (the new primary path); set quotaErr to fail the quota
// report too.
type duAgent struct {
	duOK         bool
	duTotalBytes int64
	quotaErr     bool
}

func (a duAgent) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	switch method {
	case "user.limits.report":
		if a.quotaErr {
			return nil, fmt.Errorf("quota unavailable")
		}
		return json.Marshal(map[string]any{"disk": map[string]uint64{"used_kb": 1024, "limit_kb": 10240}})
	case "files.du":
		if !a.duOK {
			return nil, fmt.Errorf("du timed out")
		}
		return json.Marshal(map[string]int64{"total": a.duTotalBytes})
	case "db.size":
		return json.Marshal(map[string]int64{"size_bytes": 2048})
	}
	return nil, fmt.Errorf("unexpected agent call %q", method)
}

// duSnapRepo is a minimal in-memory snapshot repo for the preserve test.
type duSnapRepo struct {
	repository.DiskUsageSnapshotRepository
	snap *models.DiskUsageSnapshot
}

func (r *duSnapRepo) Get(context.Context, string) (*models.DiskUsageSnapshot, error) {
	return r.snap, nil
}
func (r *duSnapRepo) Upsert(_ context.Context, userID, payload string, at time.Time) error {
	r.snap = &models.DiskUsageSnapshot{UserID: userID, Payload: payload, ComputedAt: at}
	return nil
}

func TestMeDiskUsage_Aggregates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	uname := "alice"
	cfg := DiskUsageConfig{
		Users:      &duUserRepo{u: &models.User{ID: "u1", Username: &uname}},
		Domains:    &duDomainRepo{doms: []models.Domain{{ID: "d1"}}},
		Mailboxes:  &duMailboxRepo{mbs: []models.Mailbox{{EmailCached: "a@x", LastUsageBytes: 3000}, {EmailCached: "b@x", LastUsageBytes: 500}}},
		Databases:  &duDatabaseRepo{dbs: []models.Database{{Name: "db1", Engine: "mariadb"}}},
		Agent:      duAgent{},
		QuotaMount: "/home",
	}

	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	RegisterMeDiskUsageRoutes(v1, cfg)

	// GET before any refresh returns an empty snapshot (computed_at nil) — the
	// page never auto-computes on entry.
	greq := httptest.NewRequest(http.MethodGet, "/api/v1/me/disk-usage", nil)
	grec := httptest.NewRecorder()
	r.ServeHTTP(grec, greq)
	var empty diskUsageResponse
	_ = json.Unmarshal(grec.Body.Bytes(), &empty)
	if empty.ComputedAt != nil || empty.TotalBytes != 0 {
		t.Errorf("GET before refresh must be empty, got total=%d computed_at=%v", empty.TotalBytes, empty.ComputedAt)
	}

	// POST refresh recomputes live (where the aggregation now lives).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/disk-usage/refresh", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}

	var resp diskUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ComputedAt == nil {
		t.Error("refresh must set computed_at")
	}
	if resp.Files.Bytes != 1024*1024 {
		t.Errorf("files: want %d, got %d", 1024*1024, resp.Files.Bytes)
	}
	if resp.QuotaBytes != 10240*1024 {
		t.Errorf("quota: want %d, got %d", 10240*1024, resp.QuotaBytes)
	}
	if resp.Email.Bytes != 3500 || len(resp.Email.Items) != 2 {
		t.Errorf("email: want 3500/2, got %d/%d", resp.Email.Bytes, len(resp.Email.Items))
	}
	if resp.Databases.Bytes != 2048 || len(resp.Databases.Items) != 1 {
		t.Errorf("databases: want 2048/1, got %d/%d", resp.Databases.Bytes, len(resp.Databases.Items))
	}
	want := uint64(1024*1024 + 3500 + 2048)
	if resp.TotalBytes != want {
		t.Errorf("total: want %d, got %d", want, resp.TotalBytes)
	}
}

// duRefresh is a small harness: POST /refresh with the given agent + optional
// snapshot repo, returning the decoded response.
func duRefresh(t *testing.T, ag duAgent, snaps repository.DiskUsageSnapshotRepository) diskUsageResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	uname := "alice"
	cfg := DiskUsageConfig{
		Users:      &duUserRepo{u: &models.User{ID: "u1", Username: &uname}},
		Domains:    &duDomainRepo{},
		Mailboxes:  &duMailboxRepo{},
		Databases:  &duDatabaseRepo{},
		Agent:      ag,
		QuotaMount: "/home",
		Snapshots:  snaps,
	}
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) { ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"}); c.Next() })
	RegisterMeDiskUsageRoutes(v1, cfg)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/me/disk-usage/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp diskUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// GH #1439: Files must be the ACTUAL du of the home dir, not the quota report
// (which over-counts the whole-UID footprint across the device).
func TestMeDiskUsage_FilesFromDu(t *testing.T) {
	resp := duRefresh(t, duAgent{duOK: true, duTotalBytes: 5 * 1024 * 1024}, nil)
	if resp.Files.Bytes != 5*1024*1024 {
		t.Errorf("Files must be the du figure (5 MiB), got %d", resp.Files.Bytes)
	}
	// QuotaBytes still comes from the quota report's limit.
	if resp.QuotaBytes != 10240*1024 {
		t.Errorf("QuotaBytes must be the quota limit, got %d", resp.QuotaBytes)
	}
}

func TestMeDiskUsage_FilesFallBackToQuotaWhenDuFails(t *testing.T) {
	resp := duRefresh(t, duAgent{duOK: false}, nil) // du errors → quota used_kb
	if resp.Files.Bytes != 1024*1024 {
		t.Errorf("Files must fall back to quota used (1 MiB), got %d", resp.Files.Bytes)
	}
}

// A truly-empty home (du=0, measured) must report 0 — NOT preserve a stale
// prior value.
func TestMeDiskUsage_EmptyHomeReportsZero(t *testing.T) {
	prior, _ := json.Marshal(diskUsageResponse{Files: diskUsageCategory{Bytes: 9 * 1024 * 1024, Items: []diskUsageItem{}}})
	snaps := &duSnapRepo{snap: &models.DiskUsageSnapshot{UserID: "u1", Payload: string(prior)}}
	resp := duRefresh(t, duAgent{duOK: true, duTotalBytes: 0}, snaps)
	if resp.Files.Bytes != 0 {
		t.Errorf("empty home must report 0, got %d (must not preserve prior)", resp.Files.Bytes)
	}
}

// When BOTH du and quota fail (agent hiccup), the last real Files figure must
// be preserved rather than clobbered with 0.
func TestMeDiskUsage_PreservePriorWhenBothFail(t *testing.T) {
	prior, _ := json.Marshal(diskUsageResponse{Files: diskUsageCategory{Bytes: 7 * 1024 * 1024, Items: []diskUsageItem{{Name: "Home directory", Bytes: 7 * 1024 * 1024}}}})
	snaps := &duSnapRepo{snap: &models.DiskUsageSnapshot{UserID: "u1", Payload: string(prior)}}
	resp := duRefresh(t, duAgent{duOK: false, quotaErr: true}, snaps)
	if resp.Files.Bytes != 7*1024*1024 {
		t.Errorf("both-fail must preserve prior Files (7 MiB), got %d", resp.Files.Bytes)
	}
}
