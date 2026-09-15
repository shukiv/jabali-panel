package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// GH #1628: webmail is a package entitlement that defaults ON. The create
// request models webmail_enabled as a *bool so an OMITTED field means "use the
// ON default" (a plain bool could not distinguish omitted from false). These
// pin that contract at the HTTP boundary — the guard against someone reverting
// the DTO to a plain bool, which would silently withhold webmail whenever a
// client omitted the field.
func createPackageForTest(t *testing.T, h *packageHandler, body string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/packages", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.create(c)
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s resp=%s", body, rec.Body.String())
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

func TestPackageCreate_WebmailDefaultsOnWhenOmitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: &mockPackageRepo{}}}

	// Omitted -> ON (the default-ON contract).
	got := createPackageForTest(t, h, `{"name":"omit"}`)
	require.Equal(t, true, got["webmail_enabled"], "omitted webmail_enabled must default ON")

	// Explicit false -> OFF (an admin unchecking webmail on the create form).
	got = createPackageForTest(t, h, `{"name":"off","webmail_enabled":false}`)
	require.Equal(t, false, got["webmail_enabled"], "explicit false must be honored, not flipped ON")

	// Explicit true -> ON.
	got = createPackageForTest(t, h, `{"name":"on","webmail_enabled":true}`)
	require.Equal(t, true, got["webmail_enabled"])
}
