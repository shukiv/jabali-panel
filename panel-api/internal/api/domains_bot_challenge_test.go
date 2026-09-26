package api

import (
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func botChallengeHarness(t *testing.T, claims *auth.AccessClaims) (*models.Domain, *mockDomainRepo, func(string) int) {
	t.Helper()
	dom := &models.Domain{ID: "d1", UserID: "u1", Name: "example.com", MailProvider: models.MailProviderJabali}
	ips := &fakeManagedIPsForDomain{rows: []models.ManagedIP{{ID: 1, Address: "203.0.113.1", Family: "ipv4", IsDefault: true}}}
	r, repo := setupDomainListenIPHarness(t, claims, ips, dom)
	return dom, repo, func(body string) int { return patchDomainBody(r, "d1", body).Code }
}

// The owner may opt their own domain into the bot challenge (#1467: opting in
// only adds protection), but the opt-out stays admin-only: a tenant must not
// weaken the operator's challenge. The exempt flag in a tenant PATCH is
// ignored, not refused.
func TestDomainPatch_OwnerSetsBotChallengeIncludeButNotExempt(t *testing.T) {
	_, repo, patch := botChallengeHarness(t, &auth.AccessClaims{UserID: "u1"})
	if code := patch(`{"bot_challenge_include":true,"bot_challenge_exempt":true}`); code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	got := repo.domains["d1"]
	if !got.BotChallengeInclude {
		t.Error("owner's opt-in was not stored")
	}
	if got.BotChallengeExempt {
		t.Error("a tenant set bot_challenge_exempt (admin-only)")
	}
}

// An admin may set the opt-out.
func TestDomainPatch_AdminSetsBotChallengeExempt(t *testing.T) {
	_, repo, patch := botChallengeHarness(t, &auth.AccessClaims{UserID: "admin", IsAdmin: true})
	if code := patch(`{"bot_challenge_exempt":true}`); code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if !repo.domains["d1"].BotChallengeExempt {
		t.Error("admin's opt-out was not stored")
	}
}

// Another tenant cannot touch either flag on a domain they do not own.
func TestDomainPatch_OtherTenantCannotSetBotChallenge(t *testing.T) {
	_, repo, patch := botChallengeHarness(t, &auth.AccessClaims{UserID: "u2"})
	if code := patch(`{"bot_challenge_include":true}`); code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", code)
	}
	if repo.domains["d1"].BotChallengeInclude {
		t.Error("another tenant's PATCH changed the opt-in")
	}
}
