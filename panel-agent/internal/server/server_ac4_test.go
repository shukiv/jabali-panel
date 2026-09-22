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

// JAB-357 AC4: admin_root is a capability bound to the CONNECTING PEER, not a
// request flag. The server resolves the peer's SO_PEERCRED UID and stamps
// whether it is on the -admin-uids allow-list onto the request context; command
// handlers read it via commands.PeerAdminCapable. These tests drive the real
// socket, so the connecting peer UID is os.Getuid(): the admin allow-list either
// contains it (capable) or does not (not capable), with the connect allow-list
// always permitting the connection so we exercise the dispatch path, not the
// connect gate.

func startServerAC4(t *testing.T, allowed, admin []uint32) string {
	t.Helper()
	r := commands.NewRegistry()
	r.Register("probe.admin", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return map[string]bool{"admin": commands.PeerAdminCapable(ctx)}, nil
	})
	sock := filepath.Join(t.TempDir(), "a.sock")
	srv, err := server.New(server.Config{
		SocketPath:        sock,
		SocketMode:        0600,
		SocketOwnerGID:    -1,
		AllowedUIDs:       allowed,
		AdminUIDs:         admin,
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

func probeAdmin(t *testing.T, sock string) bool {
	t.Helper()
	resp := roundTrip(t, sock, agentwire.Request{ID: "01AC4", Command: "probe.admin"})
	require.True(t, resp.Ok, "probe must be served, err=%v", resp.Error)
	var m struct {
		Admin bool `json:"admin"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &m), "decode probe response %s", resp.Data)
	return m.Admin
}

func TestServer_AC4_AdminUID_GrantsCapability(t *testing.T) {
	t.Parallel()
	self := uint32(os.Getuid())
	sock := startServerAC4(t, []uint32{self}, []uint32{self})
	assert.True(t, probeAdmin(t, sock), "a peer on the admin allow-list must be admin-capable")
}

func TestServer_AC4_NonAdminUID_DeniesCapability(t *testing.T) {
	t.Parallel()
	self := uint32(os.Getuid())
	// Connect is allowed (self), but the admin allow-list holds a DIFFERENT UID,
	// so admin_root must not be granted. If the server ever stamped adminCapable
	// unconditionally (or read the wrong UID), this goes RED.
	other := self + 40000
	sock := startServerAC4(t, []uint32{self}, []uint32{other})
	assert.False(t, probeAdmin(t, sock), "a peer NOT on the admin allow-list must not be admin-capable")
}

func TestServer_AC4_EmptyAdminUIDs_DeniesCapability(t *testing.T) {
	t.Parallel()
	self := uint32(os.Getuid())
	// Empty admin allow-list = no peer may assert admin_root (fail closed), even
	// though the connect gate admits this peer.
	sock := startServerAC4(t, []uint32{self}, nil)
	assert.False(t, probeAdmin(t, sock), "an empty admin allow-list must deny admin capability to every peer")
}
