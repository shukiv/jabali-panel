package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-11: GET /applications/:id/cache-health runs the agent per-install drift
// check and derives `drifted` = cache_enabled && !healthy — the field the GUI
// badges when the DB's cache_enabled flag disagrees with the live stack.

func cacheHealthResp(t *testing.T, r http.Handler, id string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/applications/"+id+"/cache-health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestCacheHealth_DriftedWhenUnhealthy(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	wpRepo.installs = map[string]*models.WordPressInstall{
		"inst1": {ID: "inst1", UserID: "user1", DomainID: "domain1", AppType: "wordpress", CacheEnabled: true, Status: "ready"},
	}
	r, ag, _ := applicationsRouter(t, "user1", true, wpRepo, domainRepo, userRepo, nil)
	ag.callFn = func(_ context.Context, command string, _ any) (json.RawMessage, error) {
		if command != "wordpress.cache_health" {
			return json.RawMessage(`{}`), nil
		}
		return json.RawMessage(`{"healthy": false, "detail": "jabali-cache plugin inactive"}`), nil
	}

	out := cacheHealthResp(t, r, "inst1")
	if out["healthy"] != false {
		t.Errorf("healthy = %v, want false", out["healthy"])
	}
	if out["drifted"] != true {
		t.Errorf("drifted = %v, want true (cache_enabled && !healthy)", out["drifted"])
	}
	if out["detail"] != "jabali-cache plugin inactive" {
		t.Errorf("detail = %v", out["detail"])
	}
}

func TestCacheHealth_NotDriftedWhenHealthy(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	wpRepo.installs = map[string]*models.WordPressInstall{
		"inst1": {ID: "inst1", UserID: "user1", DomainID: "domain1", AppType: "wordpress", CacheEnabled: true, Status: "ready"},
	}
	r, ag, _ := applicationsRouter(t, "user1", true, wpRepo, domainRepo, userRepo, nil)
	ag.callFn = func(_ context.Context, command string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"healthy": true, "detail": "ok"}`), nil
	}

	out := cacheHealthResp(t, r, "inst1")
	if out["healthy"] != true {
		t.Errorf("healthy = %v, want true", out["healthy"])
	}
	if out["drifted"] != false {
		t.Errorf("drifted = %v, want false", out["drifted"])
	}
}
