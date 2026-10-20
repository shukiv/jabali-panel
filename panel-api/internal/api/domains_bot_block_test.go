package api

import (
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func botBlockHarness(t *testing.T, admin bool) (*mockDomainRepo, func(body string) int) {
	t.Helper()
	dom := &models.Domain{ID: "d1", UserID: "u1", Name: "example.com", MailProvider: models.MailProviderJabali,
		RateLimitRPS: 5, ConnectionLimit: 7}
	claims := &auth.AccessClaims{UserID: "u1"}
	if admin {
		claims = &auth.AccessClaims{UserID: "admin", IsAdmin: true}
	}
	r, repo := setupDomainListenIPHarness(t, claims, &fakeManagedIPsForDomain{rows: []models.ManagedIP{
		{ID: 1, Address: "203.0.113.1", Family: "ipv4", IsDefault: true},
	}}, dom)
	return repo, func(body string) int { return patchDomainBody(r, "d1", body).Code }
}

// The owner turns AI-crawler blocking on and off for their own domain.
func TestDomainPatch_OwnerSetsBlockAICrawlers(t *testing.T) {
	repo, patch := botBlockHarness(t, false)
	if code := patch(`{"block_ai_crawlers": true}`); code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if !repo.domains["d1"].BlockAICrawlers {
		t.Fatal("block_ai_crawlers not saved")
	}
	if code := patch(`{"block_ai_crawlers": false}`); code != http.StatusOK || repo.domains["d1"].BlockAICrawlers {
		t.Fatalf("turning it off: code %d, saved %v", code, repo.domains["d1"].BlockAICrawlers)
	}
}

// Request limits are admin-only: a tenant's values are ignored, like the
// other admin fields, and an admin's are written.
func TestDomainPatch_RateLimitsAreAdminOnly(t *testing.T) {
	repo, patch := botBlockHarness(t, false)
	if code := patch(`{"rate_limit_rps": 0, "connection_limit": 0}`); code != http.StatusOK {
		t.Fatalf("tenant code %d", code)
	}
	if d := repo.domains["d1"]; d.RateLimitRPS != 5 || d.ConnectionLimit != 7 {
		t.Fatalf("tenant changed the limits to %d / %d", d.RateLimitRPS, d.ConnectionLimit)
	}

	repo, patch = botBlockHarness(t, true)
	if code := patch(`{"rate_limit_rps": 20}`); code != http.StatusOK {
		t.Fatalf("admin code %d", code)
	}
	if d := repo.domains["d1"]; d.RateLimitRPS != 20 || d.ConnectionLimit != 7 {
		t.Fatalf("admin limits = %d / %d, want 20 / 7 (connection limit untouched)", d.RateLimitRPS, d.ConnectionLimit)
	}
	if code := patch(`{"connection_limit": 0}`); code != http.StatusOK || repo.domains["d1"].ConnectionLimit != 0 {
		t.Fatalf("clearing the connection limit: code %d, now %d", code, repo.domains["d1"].ConnectionLimit)
	}
}

func TestDomainPatch_RateLimitOutOfRange(t *testing.T) {
	repo, patch := botBlockHarness(t, true)
	if code := patch(`{"rate_limit_rps": 10001}`); code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400", code)
	}
	if repo.domains["d1"].RateLimitRPS != 5 {
		t.Fatal("a refused limit was saved")
	}
}
