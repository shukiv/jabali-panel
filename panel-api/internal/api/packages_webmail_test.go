package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
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

// fakeWebmailPkgReconciler satisfies PackageReconciler and records webmail
// sweep kicks. The kick runs in a detached goroutine, so tests sync on the
// buffered channel before asserting.
type fakeWebmailPkgReconciler struct {
	webmailFired chan struct{}
}

func (f *fakeWebmailPkgReconciler) ReconcileSSHKeysForUser(context.Context, string) error { return nil }
func (f *fakeWebmailPkgReconciler) ReapplyPHPPoolForUser(context.Context, string) error   { return nil }
func (f *fakeWebmailPkgReconciler) ReconcileWebmailVhosts(context.Context) {
	select {
	case f.webmailFired <- struct{}{}:
	default:
	}
}

func updatePackageForTest(t *testing.T, h *packageHandler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(http.MethodPatch, "/packages/"+id, bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.update(c)
	return rec
}

func seededPkgRepo() *mockPackageRepo {
	return &mockPackageRepo{packages: map[string]*models.HostingPackage{
		"p1": {ID: "p1", Name: "p1", WebmailEnabled: true},
	}}
}

// TestPackageUpdate_WebmailFlip_KicksWebmailReconcile: flipping a package's
// webmail_enabled must kick an immediate whole-sweep webmail reconcile (GH
// #1628) so the entitlement change applies without waiting for the periodic
// sweep — mirrors the ssh_enabled / php_exec_enabled fan-outs.
func TestPackageUpdate_WebmailFlip_KicksWebmailReconcile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := &fakeWebmailPkgReconciler{webmailFired: make(chan struct{}, 1)}
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: seededPkgRepo(), Reconciler: rec}}

	resp := updatePackageForTest(t, h, "p1", `{"webmail_enabled":false}`)
	require.Equal(t, http.StatusOK, resp.Code, "resp=%s", resp.Body.String())

	select {
	case <-rec.webmailFired:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a webmail reconcile kick after webmail_enabled flipped")
	}
}

// TestPackageUpdate_WebmailUnchanged_NoKick: a PATCH that leaves webmail_enabled
// at its current value must NOT kick the sweep.
func TestPackageUpdate_WebmailUnchanged_NoKick(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := &fakeWebmailPkgReconciler{webmailFired: make(chan struct{}, 1)}
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: seededPkgRepo(), Reconciler: rec}}

	resp := updatePackageForTest(t, h, "p1", `{"webmail_enabled":true}`)
	require.Equal(t, http.StatusOK, resp.Code, "resp=%s", resp.Body.String())

	select {
	case <-rec.webmailFired:
		t.Fatal("must not kick when webmail_enabled did not change")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestPackageUpdate_WebmailFlip_NilReconciler_NoPanic: the kick is nil-safe —
// an install without a wired reconciler still updates the package.
func TestPackageUpdate_WebmailFlip_NilReconciler_NoPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: seededPkgRepo()}} // nil Reconciler

	resp := updatePackageForTest(t, h, "p1", `{"webmail_enabled":false}`)
	require.Equal(t, http.StatusOK, resp.Code, "resp=%s", resp.Body.String())
}
