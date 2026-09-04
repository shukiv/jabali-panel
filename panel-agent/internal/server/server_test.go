package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/commands"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/server"
)

// startServer spins up a server on a temp socket with a private registry so
// tests can't collide via the default registry. Returns the bound socket
// path and a teardown hook registered on the test.
func startServer(t *testing.T, registry *commands.Registry) string {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "a.sock")

	srv, err := server.New(server.Config{
		SocketPath:        sock,
		SocketMode:        0600,
		SocketOwnerGID:    -1,
		PerRequestTimeout: 2 * time.Second,
		Registry:          registry,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx)
		close(done)
	}()

	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-done
	})
	return sock
}

// roundTrip writes a single NDJSON request to sock, returns the response.
func roundTrip(t *testing.T, sock string, req agentwire.Request) agentwire.Response {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	b, err := json.Marshal(req)
	require.NoError(t, err)
	// A server that rejects on connect — the SO_PEERCRED UID gate — sends its
	// permission-denied envelope and closes the connection immediately, so this
	// write can lose the race with that close and return EPIPE/ECONNRESET. That
	// is NOT a test failure: the rejection is already buffered and the scan
	// below still returns it. Fail only on an unexpected write error. (Without
	// this, TestServer_UIDGate_RejectsUnauthorizedUID flaked under -race with
	// "write: broken pipe".)
	if _, werr := conn.Write(append(b, '\n')); werr != nil &&
		!errors.Is(werr, syscall.EPIPE) &&
		!errors.Is(werr, syscall.ECONNRESET) &&
		!errors.Is(werr, net.ErrClosed) {
		require.NoError(t, werr)
	}
	// half-close write so server stops reading
	if uc, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = uc.CloseWrite()
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	require.True(t, sc.Scan(), "response expected, err=%v", sc.Err())
	var resp agentwire.Response
	require.NoError(t, json.Unmarshal(sc.Bytes(), &resp))
	return resp
}

// ---------- tests ----------

func TestServer_HappyPath(t *testing.T) {
	t.Parallel()

	r := commands.NewRegistry()
	r.Register("echo", func(_ context.Context, params json.RawMessage) (any, error) {
		return map[string]json.RawMessage{"params": params}, nil
	})
	sock := startServer(t, r)

	resp := roundTrip(t, sock, agentwire.Request{
		ID:      "01ABCD",
		Command: "echo",
		Params:  json.RawMessage(`{"x":1}`),
	})
	assert.Equal(t, "01ABCD", resp.ID)
	assert.True(t, resp.Ok)
	assert.JSONEq(t, `{"params":{"x":1}}`, string(resp.Data))
}

func TestServer_UnknownCommand(t *testing.T) {
	t.Parallel()

	r := commands.NewRegistry()
	sock := startServer(t, r)

	resp := roundTrip(t, sock, agentwire.Request{ID: "1", Command: "nope"})
	assert.False(t, resp.Ok)
	require.NotNil(t, resp.Error)
	assert.Equal(t, agentwire.CodeUnknownCommand, resp.Error.Code)
}

func TestServer_MalformedEnvelope(t *testing.T) {
	t.Parallel()

	r := commands.NewRegistry()
	sock := startServer(t, r)

	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	_, _ = conn.Write([]byte("not json at all\n"))
	if uc, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = uc.CloseWrite()
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	require.True(t, sc.Scan(), "response expected")
	var resp agentwire.Response
	require.NoError(t, json.Unmarshal(sc.Bytes(), &resp))
	assert.False(t, resp.Ok)
	require.NotNil(t, resp.Error)
	assert.Equal(t, agentwire.CodeMalformedEnvelope, resp.Error.Code)
}

func TestServer_EmptyCommand(t *testing.T) {
	t.Parallel()

	r := commands.NewRegistry()
	sock := startServer(t, r)

	resp := roundTrip(t, sock, agentwire.Request{ID: "1"})
	assert.False(t, resp.Ok)
	require.NotNil(t, resp.Error)
	assert.Equal(t, agentwire.CodeInvalidArgument, resp.Error.Code)
}

func TestServer_HandlerError_PropagatedWithCode(t *testing.T) {
	t.Parallel()

	r := commands.NewRegistry()
	r.Register("explode", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, &agentwire.AgentError{Code: agentwire.CodePermissionDenied, Message: "go away"}
	})
	sock := startServer(t, r)

	resp := roundTrip(t, sock, agentwire.Request{ID: "1", Command: "explode"})
	assert.False(t, resp.Ok)
	require.NotNil(t, resp.Error)
	assert.Equal(t, agentwire.CodePermissionDenied, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "go away")
}

func TestServer_DeadlineHonoured(t *testing.T) {
	t.Parallel()

	r := commands.NewRegistry()
	r.Register("slow", func(ctx context.Context, _ json.RawMessage) (any, error) {
		select {
		case <-ctx.Done():
			return nil, &agentwire.AgentError{Code: agentwire.CodeDeadlineExceeded, Message: "timed out"}
		case <-time.After(2 * time.Second):
			return map[string]bool{"ok": true}, nil
		}
	})
	sock := startServer(t, r)

	// Request a 200ms deadline; handler's select must fire on ctx.Done().
	dl := time.Now().Add(200 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	start := time.Now()
	resp := roundTrip(t, sock, agentwire.Request{ID: "1", Command: "slow", Deadline: dl})
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 1*time.Second)
	assert.False(t, resp.Ok)
	require.NotNil(t, resp.Error)
	assert.Equal(t, agentwire.CodeDeadlineExceeded, resp.Error.Code)
}

func TestServer_StaleSocketReclaimed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sock := filepath.Join(dir, "stale.sock")

	// Simulate an abandoned socket file left by a crashed predecessor.
	// SetUnlinkOnClose(false) leaves the inode on disk after Close() so
	// cleanupStaleSocket has something to reclaim.
	addr, err := net.ResolveUnixAddr("unix", sock)
	require.NoError(t, err)
	l, err := net.ListenUnix("unix", addr)
	require.NoError(t, err)
	l.SetUnlinkOnClose(false)
	require.NoError(t, l.Close())

	// The file must still exist at this point.
	_, statErr := filepath.Abs(sock)
	require.NoError(t, statErr)

	srv, err := server.New(server.Config{
		SocketPath: sock, SocketMode: 0600, SocketOwnerGID: -1,
		Registry: commands.NewRegistry(),
	})
	require.NoError(t, err, "New() must succeed by reclaiming the stale file")
	require.NoError(t, srv.Close())
}

func TestServer_LiveSocketBindFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sock := filepath.Join(dir, "live.sock")

	// Hold a listener explicitly so cleanupStaleSocket sees a live peer.
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer l.Close()

	_, err = server.New(server.Config{
		SocketPath: sock, Registry: commands.NewRegistry(),
	})
	require.Error(t, err, "New() must refuse to clobber a live socket")
}

// ---------- concurrency ----------

func TestServer_ConcurrentRequests(t *testing.T) {
	t.Parallel()

	r := commands.NewRegistry()
	r.Register("id", func(_ context.Context, params json.RawMessage) (any, error) {
		return map[string]json.RawMessage{"echo": params}, nil
	})
	sock := startServer(t, r)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			body, _ := json.Marshal(map[string]int{"n": i})
			resp := roundTrip(t, sock, agentwire.Request{
				ID: "x", Command: "id", Params: body,
			})
			if !assert.True(t, resp.Ok) {
				return
			}
			var out struct {
				Echo map[string]int `json:"echo"`
			}
			require.NoError(t, json.Unmarshal(resp.Data, &out))
			assert.Equal(t, i, out.Echo["n"])
		}()
	}
	wg.Wait()
}

// Unused import guard.
var _ = io.EOF
