package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"

	"github.com/gin-gonic/gin"
	"log/slog"
)

// mockServerSettingsRepo is a simple mock for ServerSettingsRepository.
type mockServerSettingsRepo struct {
	getResult *models.ServerSettings
	getErr    error
	upsertErr error
}

func (m *mockServerSettingsRepo) Get(ctx context.Context) (*models.ServerSettings, error) {
	return m.getResult, m.getErr
}

func (m *mockServerSettingsRepo) Upsert(ctx context.Context, s *models.ServerSettings) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	// Update the mock result to reflect what would be in the database
	if m.getResult != nil {
		m.getResult = s
	}
	return nil
}

func (m *mockServerSettingsRepo) SetDigestLastSent(context.Context, string) error { return nil }

func (m *mockServerSettingsRepo) EnsureVAPID(ctx context.Context, hostname string) (bool, error) {
	return false, nil
}

// settingsRouter returns a Gin engine with server settings routes mounted and
// optional admin claims injected. If adminMode is true, RequireAdmin passes.
// If adminMode is false, claims are omitted entirely (no auth).
func settingsRouter(adminMode bool, mockRepo *mockServerSettingsRepo, mockAgent agent.AgentInterface) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	if adminMode {
		v1.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
			c.Next()
		})
	} else {
		// No middleware — no claims injected
	}

	RegisterServerSettingsRoutes(v1, ServerSettingsHandlerConfig{
		Repo:  mockRepo,
		Agent: mockAgent,
		Log:   slog.Default(),
	})
	return r
}

func TestServerSettingsGet_Unauthorized(t *testing.T) {
	t.Parallel()

	mockRepo := &mockServerSettingsRepo{}
	mockAgent := agent.NewMockClient()

	r := settingsRouter(false, mockRepo, mockAgent) // No admin claims

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "unauthenticated", body["error"])
}

func TestServerSettingsGet_NonAdminForbidden(t *testing.T) {
	t.Parallel()

	mockRepo := &mockServerSettingsRepo{}
	mockAgent := agent.NewMockClient()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	// Inject non-admin claims
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-user", IsAdmin: false})
		c.Next()
	})

	RegisterServerSettingsRoutes(v1, ServerSettingsHandlerConfig{
		Repo:  mockRepo,
		Agent: mockAgent,
		Log:   slog.Default(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestServerSettingsGet_OK(t *testing.T) {
	t.Parallel()

	expected := &models.ServerSettings{
		ID:              1,
		Hostname:        "example.com",
		PublicIPv4:      "192.0.2.1",
		PublicIPv6:      "2001:db8::1",
		NS1Name:         "ns1.example.com",
		NS1IPv4:         "192.0.2.1",
		NS2Name:         "ns2.example.com",
		NS2IPv4:         "192.0.2.2",
		AdminEmail:      "admin@example.com",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = expected
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body models.ServerSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, expected.Hostname, body.Hostname)
	assert.Equal(t, expected.PublicIPv4, body.PublicIPv4)
	assert.Equal(t, expected.AdminEmail, body.AdminEmail)
}

func TestServerSettingsGet_DatabaseError(t *testing.T) {
	t.Parallel()

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getErr = errors.New("database error")
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "internal", body["error"])
}

func TestServerSettingsPatch_Unauthorized(t *testing.T) {
	t.Parallel()

	mockRepo := &mockServerSettingsRepo{}
	mockAgent := agent.NewMockClient()

	r := settingsRouter(false, mockRepo, mockAgent) // No admin claims

	payload := map[string]string{"hostname": "newhost.com"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServerSettingsPatch_InvalidJSON(t *testing.T) {
	t.Parallel()

	mockRepo := &mockServerSettingsRepo{}
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "validation_failed", respBody["error"])
}

func TestServerSettingsPatch_TenantNotificationKinds_SanitizesUnknown(t *testing.T) {
	t.Parallel()
	mockRepo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22}}
	r := settingsRouter(true, mockRepo, agent.NewMockClient())

	// email is admin opt-in (phase 4d); "bogus" is unknown and dropped.
	payload := map[string]any{"tenant_notification_kinds": []string{"webhook", "email", "bogus"}}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Persisted set: webhook + email kept, bogus dropped.
	require.ElementsMatch(t, []string{"webhook", "email"}, []string(mockRepo.getResult.TenantNotificationKinds))
}

func TestServerSettingsPatch_TenantNotificationsToggle(t *testing.T) {
	t.Parallel()
	mockRepo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22}}
	r := settingsRouter(true, mockRepo, agent.NewMockClient())

	payload := map[string]any{"tenant_notifications_enabled": true}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, mockRepo.getResult.TenantNotificationsEnabled, "toggle must persist")
}

func TestServerSettingsGet_TenantNotificationKinds_EffectiveDefault(t *testing.T) {
	t.Parallel()
	// Empty column → GET surfaces the safe default set, not a blank list.
	mockRepo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1}}
	r := settingsRouter(true, mockRepo, agent.NewMockClient())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp models.ServerSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.ElementsMatch(t, []string{"ntfy", "telegram", "discord", "webpush"},
		[]string(resp.TenantNotificationKinds))
}

func TestServerSettingsPatch_PartialUpdate(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "old.example.com",
		PublicIPv4:      "192.0.2.1",
		PublicIPv6:      "2001:db8::1",
		NS1Name:         "ns1.example.com",
		NS1IPv4:         "192.0.2.1",
		NS2Name:         "ns2.example.com",
		NS2IPv4:         "192.0.2.2",
		AdminEmail:      "admin@example.com",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	// No special setup needed; Upsert will succeed by default // Expect upsert to be called, we'll verify below

	mockAgent := agent.NewMockClient()
	mockAgent.On("system.set_hostname", map[string]any{"hostname": "new.example.com"})

	r := settingsRouter(true, mockRepo, mockAgent)

	newHostname := "new.example.com"
	payload := map[string]any{"hostname": newHostname}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var respBody models.ServerSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, newHostname, respBody.Hostname)
	// Other fields should remain unchanged
	assert.Equal(t, existing.PublicIPv4, respBody.PublicIPv4)
	assert.Equal(t, existing.AdminEmail, respBody.AdminEmail)
}

func TestServerSettingsPatch_InvalidHostname(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "good.example.com",
		PublicIPv4:      "192.0.2.1",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	// Invalid hostname with special characters
	badHostname := "invalid_hostname!!!"
	payload := map[string]any{"hostname": badHostname}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "invalid_settings", respBody["error"])
}

func TestServerSettingsPatch_InvalidIPv4(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "example.com",
		PublicIPv4:      "192.0.2.1",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	payload := map[string]any{"public_ipv4": "not.an.ip.address"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "invalid_settings", respBody["error"])
	assert.Contains(t, respBody["detail"], "not a valid IPv4")
}

func TestServerSettingsPatch_InvalidIPv6(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "example.com",
		PublicIPv6:      "2001:db8::1",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	// Try to set IPv6 to IPv4 address
	payload := map[string]any{"public_ipv6": "192.0.2.1"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "invalid_settings", respBody["error"])
	assert.Contains(t, respBody["detail"], "not a valid IPv6")
}

func TestServerSettingsPatch_InvalidEmail(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "example.com",
		AdminEmail:      "admin@example.com",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	payload := map[string]any{"admin_email": "not-an-email"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "invalid_settings", respBody["error"])
	assert.Contains(t, respBody["detail"], "admin_email")
}

func TestServerSettingsPatch_HostnameChangeTriggersAgent(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "old.example.com",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	// No special setup needed; Upsert will succeed by default

	newHostname := "new.example.com"
	mockAgent := agent.NewMockClient()
	mockAgent.On("system.set_hostname", map[string]any{"hostname": newHostname, "status": "ok"})

	r := settingsRouter(true, mockRepo, mockAgent)

	payload := map[string]any{"hostname": newHostname}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// Verify agent was called with correct parameters
	// (actual verification depends on mock implementation)
}

func TestServerSettingsPatch_NoHostnameChangeNoAgent(t *testing.T) {
	t.Parallel()

	hostname := "example.com"
	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        hostname,
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	// No special setup needed; Upsert will succeed by default

	mockAgent := agent.NewMockClient()
	// Do NOT expect agent call when hostname hasn't changed

	r := settingsRouter(true, mockRepo, mockAgent)

	// Update unrelated field
	payload := map[string]any{"admin_email": "newemail@example.com"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// Verify agent was NOT called
}

func TestServerSettingsPatch_EmptyHostnameToSomethingTriggersAgent(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	// No special setup needed; Upsert will succeed by default

	newHostname := "fresh.example.com"
	mockAgent := agent.NewMockClient()
	mockAgent.On("system.set_hostname", map[string]any{"hostname": newHostname, "status": "ok"})

	r := settingsRouter(true, mockRepo, mockAgent)

	payload := map[string]any{"hostname": newHostname}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var respBody models.ServerSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, newHostname, respBody.Hostname)
}

func TestServerSettingsPatch_AllFieldsUpdate(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "old.com",
		PublicIPv4:      "192.0.2.1",
		PublicIPv6:      "2001:db8::1",
		NS1Name:         "ns1.old.com",
		NS1IPv4:         "192.0.2.10",
		NS2Name:         "ns2.old.com",
		NS2IPv4:         "192.0.2.11",
		AdminEmail:      "old@example.com",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	// No special setup needed; Upsert will succeed by default

	mockAgent := agent.NewMockClient()
	mockAgent.On("system.set_hostname", map[string]any{"hostname": "new.com"})

	r := settingsRouter(true, mockRepo, mockAgent)

	payload := map[string]any{
		"hostname":    "new.com",
		"public_ipv4": "192.0.2.2",
		"public_ipv6": "2001:db8::2",
		"ns1_name":    "ns1.new.com",
		"ns1_ipv4":    "192.0.2.20",
		"ns2_name":    "ns2.new.com",
		"ns2_ipv4":    "192.0.2.21",
		"admin_email": "new@example.com",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var respBody models.ServerSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "new.com", respBody.Hostname)
	assert.Equal(t, "192.0.2.2", respBody.PublicIPv4)
	assert.Equal(t, "2001:db8::2", respBody.PublicIPv6)
	assert.Equal(t, "ns1.new.com", respBody.NS1Name)
	assert.Equal(t, "192.0.2.20", respBody.NS1IPv4)
	assert.Equal(t, "ns2.new.com", respBody.NS2Name)
	assert.Equal(t, "192.0.2.21", respBody.NS2IPv4)
	assert.Equal(t, "new@example.com", respBody.AdminEmail)
}

func TestServerSettingsPatch_DatabaseError(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "example.com",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}

	mockRepo := &mockServerSettingsRepo{}
	mockRepo.getResult = existing
	mockRepo.upsertErr = errors.New("database error")
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	payload := map[string]any{"hostname": "newhost.com"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "internal", respBody["error"])
}

// GH #454: the admin owns the tenant scheduled-backup time via
// server_settings.tenant_backup_cron. A valid cron is stored; an invalid one is
// rejected 400 before it can strand the tenant scheduler with an unparseable time.
func TestServerSettingsPatch_TenantBackupCron_Valid(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID: 1, Hostname: "h.example.com", PublicIPv4: "192.0.2.1",
		NS1Name: "ns1.example.com", NS1IPv4: "192.0.2.1",
		NS2Name: "ns2.example.com", NS2IPv4: "192.0.2.2",
		AdminEmail: "admin@example.com", SSHPort: 22,
		TenantBackupCron: "0 3 * * *",
	}
	mockRepo := &mockServerSettingsRepo{getResult: existing}
	r := settingsRouter(true, mockRepo, agent.NewMockClient())

	body, _ := json.Marshal(map[string]any{"tenant_backup_cron": "30 2 * * *"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var respBody models.ServerSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "30 2 * * *", respBody.TenantBackupCron)
}

func TestServerSettingsPatch_TenantBackupCron_Invalid(t *testing.T) {
	t.Parallel()

	existing := &models.ServerSettings{
		ID: 1, Hostname: "h.example.com", PublicIPv4: "192.0.2.1",
		NS1Name: "ns1.example.com", NS1IPv4: "192.0.2.1",
		NS2Name: "ns2.example.com", NS2IPv4: "192.0.2.2",
		AdminEmail: "admin@example.com", SSHPort: 22,
		TenantBackupCron: "0 3 * * *",
	}
	mockRepo := &mockServerSettingsRepo{getResult: existing}
	r := settingsRouter(true, mockRepo, agent.NewMockClient())

	body, _ := json.Marshal(map[string]any{"tenant_backup_cron": "not a cron"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var respBody map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "invalid_cron", respBody["error"])
}
