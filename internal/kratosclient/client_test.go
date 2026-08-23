package kratosclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
)

func TestWhoami_ValidSessionReturnsIdentity(t *testing.T) {
	t.Parallel()

	// Mock Kratos server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/sessions/whoami", r.URL.Path)

		// Check that the session cookie was sent.
		cookie, err := r.Cookie("ory_kratos_session")
		require.NoError(t, err)
		assert.Equal(t, "test-session-123", cookie.Value)

		w.Header().Set("Content-Type", "application/json")
		// Real Kratos shape: a Session envelope with `identity` nested. The
		// top-level `id` is the SESSION id and MUST NOT leak into Identity.ID
		// (which is what our users.kratos_identity_id column references).
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "session-abc",
			"active": true,
			"identity": map[string]any{
				"id": "user-456",
				"traits": map[string]any{
					"email":    "user@example.com",
					"username": "testuser",
					"is_admin": true,
				},
			},
		})
	}))
	defer server.Close()

	client := kratosclient.NewClient(server.URL, server.URL)
	identity, err := client.Whoami(context.Background(), "test-session-123")

	require.NoError(t, err)
	assert.Equal(t, "user-456", identity.ID, "must be identity.id, not session.id")
	assert.Equal(t, "user@example.com", identity.GetTraitEmail())
	assert.Equal(t, "testuser", identity.GetTraitUsername())
	assert.True(t, identity.GetTraitIsAdmin())
}

func TestWhoami_InvalidSessionReturns401(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthenticated"})
	}))
	defer server.Close()

	client := kratosclient.NewClient(server.URL, server.URL)
	identity, err := client.Whoami(context.Background(), "invalid-session")

	assert.ErrorIs(t, err, kratosclient.ErrUnauthenticated)
	assert.Nil(t, identity)
}

func TestWhoami_EmptyCookieReturnsError(t *testing.T) {
	t.Parallel()

	client := kratosclient.NewClient("http://localhost:4433", "http://localhost:4434")
	identity, err := client.Whoami(context.Background(), "")

	assert.ErrorIs(t, err, kratosclient.ErrUnauthenticated)
	assert.Nil(t, identity)
}

func TestWhoami_CacheHitSkipsRemoteCall(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "session-cache-test",
			"identity": map[string]any{
				"id":     "user-123",
				"traits": map[string]any{"email": "user@example.com"},
			},
		})
	}))
	defer server.Close()

	client := kratosclient.NewClient(server.URL, server.URL)

	// First call: cache miss, remote call.
	identity1, err := client.Whoami(context.Background(), "session-1")
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	// Second call: cache hit, no remote call.
	identity2, err := client.Whoami(context.Background(), "session-1")
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "should not increment on cache hit")

	assert.Equal(t, identity1.ID, identity2.ID)
}

func TestWhoami_DifferentCookiesRequireSeparateCalls(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		cookie, _ := r.Cookie("ory_kratos_session")
		w.Header().Set("Content-Type", "application/json")

		userID := "user-for-" + cookie.Value
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "session-" + cookie.Value,
			"identity": map[string]any{
				"id":     userID,
				"traits": map[string]any{"email": "user@example.com"},
			},
		})
	}))
	defer server.Close()

	client := kratosclient.NewClient(server.URL, server.URL)

	identity1, err := client.Whoami(context.Background(), "session-1")
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	identity2, err := client.Whoami(context.Background(), "session-2")
	require.NoError(t, err)
	assert.Equal(t, 2, callCount)

	assert.NotEqual(t, identity1.ID, identity2.ID)
}

func TestIdentity_TraitExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		traits map[string]interface{}
		email  string
		user   string
		admin  bool
	}{
		{
			name:   "all_fields_present",
			traits: map[string]interface{}{"email": "user@example.com", "username": "alice", "is_admin": true},
			email:  "user@example.com",
			user:   "alice",
			admin:  true,
		},
		{
			name:   "missing_fields",
			traits: map[string]interface{}{},
			email:  "",
			user:   "",
			admin:  false,
		},
		{
			name:   "is_admin_string_true",
			traits: map[string]interface{}{"is_admin": "true"},
			admin:  true,
		},
		{
			name:   "is_admin_string_false",
			traits: map[string]interface{}{"is_admin": "false"},
			admin:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := &kratosclient.Identity{ID: "user-123", Traits: tt.traits}

			assert.Equal(t, tt.email, identity.GetTraitEmail())
			assert.Equal(t, tt.user, identity.GetTraitUsername())
			assert.Equal(t, tt.admin, identity.GetTraitIsAdmin())
		})
	}
}

// TestWhoami_LiftsSessionMetadata verifies JAB-380: the session-envelope's
// authenticated_at + authenticator_assurance_level are parsed and lifted onto
// the returned Identity (they live at the session level, not on the identity).
func TestWhoami_LiftsSessionMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                            "session-abc",
			"active":                        true,
			"authenticated_at":              "2026-08-23T10:00:00.123456Z",
			"authenticator_assurance_level": "aal1",
			"identity": map[string]any{
				"id":     "user-456",
				"traits": map[string]any{"email": "user@example.com"},
			},
		})
	}))
	defer server.Close()

	client := kratosclient.NewClient(server.URL, server.URL)
	identity, err := client.Whoami(context.Background(), "cookie-x")
	require.NoError(t, err)
	assert.Equal(t, "user-456", identity.ID)
	assert.Equal(t, "aal1", identity.AAL)
	require.False(t, identity.AuthenticatedAt.IsZero(), "authenticated_at must be parsed")
	assert.Equal(t, 2026, identity.AuthenticatedAt.Year())
	assert.Equal(t, 10, identity.AuthenticatedAt.UTC().Hour())
}

// TestWhoami_MissingAuthenticatedAtIsZero: a whoami without authenticated_at (or
// an unparseable value) leaves a zero time, which the step-up gate treats as
// "not recently authenticated" (fail closed).
func TestWhoami_MissingAuthenticatedAtIsZero(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"s","active":true,"authenticated_at":"not-a-time","identity":{"id":"u"}}`))
	}))
	defer server.Close()

	client := kratosclient.NewClient(server.URL, server.URL)
	identity, err := client.Whoami(context.Background(), "cookie-y")
	require.NoError(t, err)
	assert.True(t, identity.AuthenticatedAt.IsZero(), "unparseable authenticated_at must be zero (fail closed)")
}

// TestWhoamiUncached_BypassesCache verifies JAB-380's refresh-loop fix: after a
// stale result is cached, WhoamiUncached round-trips again (seeing the bumped
// authenticated_at from a ?refresh=true login) instead of serving the cache.
func TestWhoamiUncached_BypassesCache(t *testing.T) {
	t.Parallel()

	authAt := "2026-08-23T09:00:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"s","active":true,"authenticated_at":"` + authAt +
			`","identity":{"id":"u"}}`))
	}))
	defer server.Close()

	client := kratosclient.NewClient(server.URL, server.URL)
	ctx := context.Background()

	// Prime the cache with the pre-refresh time.
	id1, err := client.Whoami(ctx, "cookie-z")
	require.NoError(t, err)
	assert.Equal(t, 9, id1.AuthenticatedAt.UTC().Hour())

	// Simulate a ?refresh=true login: same cookie, bumped authenticated_at.
	authAt = "2026-08-23T11:30:00Z"

	// Cached Whoami still serves the old time.
	idCached, err := client.Whoami(ctx, "cookie-z")
	require.NoError(t, err)
	assert.Equal(t, 9, idCached.AuthenticatedAt.UTC().Hour(), "cache should still hold pre-refresh time")

	// Uncached read sees the fresh time AND updates the cache.
	idFresh, err := client.WhoamiUncached(ctx, "cookie-z")
	require.NoError(t, err)
	assert.Equal(t, 11, idFresh.AuthenticatedAt.UTC().Hour(), "uncached must see post-refresh time")

	idAfter, err := client.Whoami(ctx, "cookie-z")
	require.NoError(t, err)
	assert.Equal(t, 11, idAfter.AuthenticatedAt.UTC().Hour(), "uncached must refresh the cache")
}
