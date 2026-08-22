package server_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/commands"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/server"
)

// JAB-366 / JAB-357: the main agent socket must reject a connection whose peer
// UID is not on the allow-list, so a service account accidentally left in the
// jabali group (webmail) cannot drive privileged commands via the socket alone.
// These tests exercise the real SO_PEERCRED path — roundTrip connects from the
// test process, so its peer UID is os.Getuid().

func startServerUIDs(t *testing.T, r *commands.Registry, allowed []uint32) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	srv, err := server.New(server.Config{
		SocketPath:        sock,
		SocketMode:        0600,
		SocketOwnerGID:    -1,
		AllowedUIDs:       allowed,
		PerRequestTimeout: 2 * time.Second,
		Registry:          r,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); _ = srv.Close(); <-done })
	return sock
}

func echoRegistry() *commands.Registry {
	r := commands.NewRegistry()
	r.Register("echo", func(_ context.Context, p json.RawMessage) (any, error) {
		return map[string]json.RawMessage{"params": p}, nil
	})
	return r
}

func TestServer_UIDGate_AllowsSelf(t *testing.T) {
	t.Parallel()
	sock := startServerUIDs(t, echoRegistry(), []uint32{uint32(os.Getuid())})
	resp := roundTrip(t, sock, agentwire.Request{ID: "01OK", Command: "echo", Params: json.RawMessage(`{"x":1}`)})
	assert.True(t, resp.Ok, "an allowed UID must be served")
}

func TestServer_UIDGate_RejectsUnauthorizedUID(t *testing.T) {
	t.Parallel()
	// Allow a UID that is NOT us, so our connection is refused.
	other := uint32(os.Getuid()) + 40000
	sock := startServerUIDs(t, echoRegistry(), []uint32{other})
	resp := roundTrip(t, sock, agentwire.Request{ID: "01NO", Command: "echo", Params: json.RawMessage(`{"x":1}`)})
	assert.False(t, resp.Ok, "a non-allowed UID must be rejected")
	require.NotNil(t, resp.Error)
	assert.Equal(t, agentwire.CodePermissionDenied, resp.Error.Code)
}

func TestServer_UIDGate_EmptyAllowsAny(t *testing.T) {
	t.Parallel()
	// Empty allow-list = gate disabled (out-of-systemd test runs). Backward-compat.
	sock := startServerUIDs(t, echoRegistry(), nil)
	resp := roundTrip(t, sock, agentwire.Request{ID: "01ANY", Command: "echo", Params: json.RawMessage(`{"x":1}`)})
	assert.True(t, resp.Ok, "empty allow-list must not gate (any UID served)")
}
