package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type mockReconciler struct {
	reconcileAllCalls      int
	reconcileAllForceCalls int
	reconcilePHPPoolsCalls int
	returnError            bool
}

func (m *mockReconciler) ReconcileAll(ctx context.Context) error {
	m.reconcileAllCalls++
	if m.returnError {
		return &mockError{}
	}
	return nil
}

func (m *mockReconciler) ReconcileAllForce(ctx context.Context) error {
	m.reconcileAllForceCalls++
	if m.returnError {
		return &mockError{}
	}
	return nil
}

func (m *mockReconciler) ReconcilePHPPools(ctx context.Context) {
	m.reconcilePHPPoolsCalls++
}

type mockError struct{}

func (e *mockError) Error() string {
	return "mock reconcile error"
}

func TestReconcileAPI_NormalReconciliation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mock := &mockReconciler{}

	cfg := &ReconcileHandlerConfig{
		Reconciler: mock,
		Log:        log,
	}
	h := &reconcileHandler{cfg: cfg}

	// Create test request
	reqBody := reconcileRequest{
		Scope: "all",
		Force: false,
	}
	reqBytes, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/reconcile", bytes.NewReader(reqBytes))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", "test-user")

	h.run(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, mock.reconcileAllCalls)
	require.Equal(t, 0, mock.reconcileAllForceCalls)

	var resp reconcileResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	require.Equal(t, "success", resp.Status)
}

func TestReconcileAPI_ForceReconciliation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mock := &mockReconciler{}

	cfg := &ReconcileHandlerConfig{
		Reconciler: mock,
		Log:        log,
	}
	h := &reconcileHandler{cfg: cfg}

	// Create test request with force=true
	reqBody := reconcileRequest{
		Scope: "all",
		Force: true,
	}
	reqBytes, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/reconcile", bytes.NewReader(reqBytes))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", "test-user")

	h.run(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 0, mock.reconcileAllCalls)
	require.Equal(t, 1, mock.reconcileAllForceCalls)

	var resp reconcileResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	require.Equal(t, "success", resp.Status)
}

func TestReconcileAPI_InvalidRequest(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mock := &mockReconciler{}

	cfg := &ReconcileHandlerConfig{
		Reconciler: mock,
		Log:        log,
	}
	h := &reconcileHandler{cfg: cfg}

	// Request with missing scope
	reqBody := map[string]interface{}{
		"force": true,
	}
	reqBytes, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/reconcile", bytes.NewReader(reqBytes))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", "test-user")

	h.run(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, 0, mock.reconcileAllCalls)
	require.Equal(t, 0, mock.reconcileAllForceCalls)

	var resp reconcileResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	require.Equal(t, "error", resp.Status)
}

func TestReconcileAPI_ReconcileError(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mock := &mockReconciler{returnError: true}

	cfg := &ReconcileHandlerConfig{
		Reconciler: mock,
		Log:        log,
	}
	h := &reconcileHandler{cfg: cfg}

	reqBody := reconcileRequest{
		Scope: "all",
		Force: false,
	}
	reqBytes, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/reconcile", bytes.NewReader(reqBytes))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", "test-user")

	h.run(c)

	require.Equal(t, http.StatusInternalServerError, w.Code)

	var resp reconcileResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	require.Equal(t, "error", resp.Status)
}
