package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-13: the admin mature-soak endpoints wrap egressops.FlipMature. These
// tests cover the HTTP shape — age_days derivation, dry-run vs promote, and the
// pin passthrough — via a minimal embedded-interface fake.

type fmPolicyRepo struct {
	repository.UserEgressPolicyRepository
	mature      []models.UserEgressPolicy
	upsertCalls int
}

func (r *fmPolicyRepo) ListMatureLearning(context.Context, time.Duration) ([]models.UserEgressPolicy, error) {
	return r.mature, nil
}

func (r *fmPolicyRepo) Upsert(context.Context, *models.UserEgressPolicy) error {
	r.upsertCalls++
	return nil
}

func fmServe(t *testing.T, method, path string, body string, repo *fmPolicyRepo) flipMatureResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1/admin")
	// PinPath points at a nonexistent file → never pinned.
	RegisterAdminUserEgressRoutes(g, UserEgressHandlerConfig{Policies: repo, PinPath: "/nonexistent/pin"})

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var out flipMatureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestMaturePreview_DryRunAndAge(t *testing.T) {
	started := time.Now().UTC().Add(-10 * 24 * time.Hour)
	repo := &fmPolicyRepo{mature: []models.UserEgressPolicy{
		{UserID: "u1", State: models.UserEgressStateLearning, LearningStartedAt: &started, DropCount24h: 5},
	}}
	out := fmServe(t, http.MethodGet, "/api/v1/admin/egress/mature-preview?soak_days=7", "", repo)
	if !out.DryRun || out.EligibleCount != 1 {
		t.Fatalf("preview should be dry-run with 1 eligible, got %+v", out)
	}
	if repo.upsertCalls != 0 {
		t.Errorf("preview must not write, got %d upserts", repo.upsertCalls)
	}
	e := out.Eligible[0]
	if e.UserID != "u1" || e.DropCount24h != 5 {
		t.Errorf("eligible row wrong: %+v", e)
	}
	if e.AgeDays < 9 || e.AgeDays > 11 {
		t.Errorf("age_days ~10 expected, got %d", e.AgeDays)
	}
}

func TestFlipMature_Promotes(t *testing.T) {
	repo := &fmPolicyRepo{mature: []models.UserEgressPolicy{
		{UserID: "u1", State: models.UserEgressStateLearning},
		{UserID: "u2", State: models.UserEgressStateLearning},
	}}
	out := fmServe(t, http.MethodPost, "/api/v1/admin/egress/flip-mature", `{"soak_days":7}`, repo)
	if out.DryRun {
		t.Fatal("flip must not be a dry-run")
	}
	if out.FlippedCount != 2 || repo.upsertCalls != 2 {
		t.Fatalf("expected 2 flips + 2 upserts, got flipped=%d upserts=%d", out.FlippedCount, repo.upsertCalls)
	}
}
