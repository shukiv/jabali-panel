package reconciler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestReconcileAll_RecursorAddZoneForEnabledDomain verifies Step 5 of
// the M6.3 plan: every enabled DB domain triggers pdns.recursor_add_zone
// on the agent per reconciler pass. The agent command is idempotent
// (no-ops on unchanged entries), so calling it every tick is safe and
// self-healing.
func TestReconcileAll_RecursorAddZoneForEnabledDomain(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	now := time.Now().UTC()
	username := "alice"
	user := &models.User{ID: "user-1", Email: "alice@example.com", Username: &username}
	userRepo.users[user.ID] = user

	domain := &models.Domain{
		OwnershipState: verifiedOwnership,
		ID:             "domain-1",
		UserID:         user.ID,
		Name:           "alice-site.com",
		DocRoot:        "/home/alice/domains/alice-site.com/public_html",
		IsEnabled:      true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})
	require.NoError(t, r.ReconcileAll(ctx))

	// Find pdns.recursor_add_zone calls.
	var addCalls []fakeCall
	for _, c := range agent.calls {
		if c.method == "pdns.recursor_add_zone" {
			addCalls = append(addCalls, c)
		}
	}
	require.NotEmpty(t, addCalls, "expected at least one pdns.recursor_add_zone call; got calls: %+v", agent.calls)

	// Assert one call was for alice-site.com with the jabali forwarder target.
	found := false
	for _, c := range addCalls {
		m, ok := c.params.(map[string]any)
		if !ok {
			continue
		}
		if m["zone"] == "alice-site.com" {
			require.Equal(t, "127.0.0.1", m["addr"], "forwarder addr should be loopback")
			require.EqualValues(t, 5300, m["port"], "forwarder port should be 5300")
			found = true
		}
	}
	require.True(t, found, "no recursor_add_zone for alice-site.com; got %+v", addCalls)
}

// TestReconcileAll_RecursorRemoveZoneForOrphan verifies that a site
// present on the agent but absent from the DB triggers
// pdns.recursor_remove_zone — keeps stale forwarders from piling up
// after manual DB deletes. Idempotent: remove of an absent forwarder
// is a no-op on the agent side.
func TestReconcileAll_RecursorRemoveZoneForOrphan(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// fakeAgent's default domain.list returns "example.com" + "foo.bar.com".
	// No DB rows → both are orphans.
	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})
	require.NoError(t, r.ReconcileAll(ctx))

	var removeCalls []fakeCall
	for _, c := range agent.calls {
		if c.method == "pdns.recursor_remove_zone" {
			removeCalls = append(removeCalls, c)
		}
	}
	// Both orphans should produce remove calls.
	require.Len(t, removeCalls, 2, "expected 2 remove_zone calls (one per orphan); got %+v", agent.calls)

	zones := map[string]bool{}
	for _, c := range removeCalls {
		m, ok := c.params.(map[string]any)
		if !ok {
			continue
		}
		if z, ok := m["zone"].(string); ok {
			zones[z] = true
		}
	}
	require.True(t, zones["example.com"], "missing remove for example.com; zones=%v", zones)
	require.True(t, zones["foo.bar.com"], "missing remove for foo.bar.com; zones=%v", zones)
}

// TestReconcileAll_RecursorRemoveZoneSkipsDisabled confirms disabled-but-
// present-in-DB domains keep their forwarders. A disabled domain still
// needs local resolution so the disabled_vhost static page can be
// served, and so reactivation converges fast.
func TestReconcileAll_RecursorRemoveZoneSkipsDisabled(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	now := time.Now().UTC()
	username := "bob"
	user := &models.User{ID: "user-1", Email: "bob@example.com", Username: &username}
	userRepo.users[user.ID] = user

	// Disabled domain whose name matches a site the fakeAgent reports
	// (example.com). Without this DB row it'd be an orphan; with it
	// the domain is "disabled but tracked" and remove_zone MUST NOT fire.
	domain := &models.Domain{
		OwnershipState: verifiedOwnership,
		ID:             "domain-disabled",
		UserID:         user.ID,
		Name:           "example.com",
		DocRoot:        "/home/bob/domains/example.com/public_html",
		IsEnabled:      false,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})
	require.NoError(t, r.ReconcileAll(ctx))

	for _, c := range agent.calls {
		if c.method != "pdns.recursor_remove_zone" {
			continue
		}
		m, ok := c.params.(map[string]any)
		if !ok {
			continue
		}
		if z, ok := m["zone"].(string); ok && z == "example.com" {
			t.Fatalf("disabled domain example.com should NOT trigger remove_zone; got params=%+v", m)
		}
	}
}
