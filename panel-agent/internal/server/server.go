// Package server implements the Unix-socket listener + per-connection
// NDJSON dispatch loop for the jabali-agent binary.
//
// Lifecycle:
//   - New() binds the socket and sets its mode/owner.
//   - Serve() runs the accept loop until ctx is cancelled; each connection
//     is handled in its own goroutine.
//   - Close() stops accepting and waits for in-flight connections to finish.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/commands"
)

// Config is the tunable surface. Zero values are valid — SocketMode defaults
// to 0660 and SocketOwnerGID defaults to -1 (leave as-is, inherit from umask
// + process identity).
type Config struct {
	SocketPath string

	// SocketMode is applied via os.Chmod right after Listen. Tests pass
	// 0600 so the temp socket isn't group-readable.
	SocketMode os.FileMode

	// SocketOwnerGID, if >= 0, chown's the socket to root:<gid>. Production
	// uses the jabali group so the panel-api process can connect. -1 skips.
	SocketOwnerGID int

	// PerRequestTimeout bounds each handler's wall-clock. Zero = no bound
	// (caller-supplied deadline still applies). We default to 120s in main.go.
	PerRequestTimeout time.Duration

	// AllowedUIDs lists the Unix UIDs permitted to connect. If empty, any
	// UID is allowed — this tolerance is for the server package's own tests,
	// which construct a socket without a gate. The PRODUCTION policy lives at
	// the composition root (cmd/jabali-agent decideUIDGate, JAB-357): the
	// binary refuses to start with an empty/mangled allow-list unless
	// explicitly opted out, so an empty list never reaches here in production.
	AllowedUIDs []uint32

	// MaxRequestBytes caps a single NDJSON request line. A misbehaving or
	// malicious client can't drain memory. Default 8 MiB.
	MaxRequestBytes int

	// Registry is the command table. Defaults to commands.Default.
	Registry *commands.Registry

	// Logger, if nil, defaults to the global slog logger.
	Logger *slog.Logger
}

// Server is the running listener + connection tracker.
type Server struct {
	cfg Config
	ln  net.Listener
	log *slog.Logger

	conns    sync.WaitGroup
	stopOnce sync.Once
}

// New binds the socket and applies permissions. It does NOT start accepting
// yet — call Serve(ctx).
func New(cfg Config) (*Server, error) {
	if cfg.SocketPath == "" {
		return nil, errors.New("server: SocketPath is required")
	}
	if cfg.SocketMode == 0 {
		cfg.SocketMode = 0660
	}
	if cfg.MaxRequestBytes == 0 {
		cfg.MaxRequestBytes = 8 << 20
	}
	if cfg.Registry == nil {
		cfg.Registry = commands.Default
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// Remove a stale socket left by a crashed predecessor. Safe because
	// only a prior instance of us could have created it, and if a live
	// instance is listening net.Listen will fail afterward with EADDRINUSE.
	if err := cleanupStaleSocket(cfg.SocketPath); err != nil {
		return nil, fmt.Errorf("server: cleanup stale socket: %w", err)
	}

	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("server: listen %s: %w", cfg.SocketPath, err)
	}
	// Chmod AFTER bind so any client that races us can't connect before
	// we've clamped permissions.
	if err := os.Chmod(cfg.SocketPath, cfg.SocketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("server: chmod socket: %w", err)
	}
	if cfg.SocketOwnerGID >= 0 {
		if err := os.Chown(cfg.SocketPath, 0, cfg.SocketOwnerGID); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("server: chown socket: %w", err)
		}
	}

	return &Server{cfg: cfg, ln: ln, log: cfg.Logger}, nil
}

// SocketPath returns the absolute path of the bound socket. Useful for tests.
func (s *Server) SocketPath() string { return s.cfg.SocketPath }

// Serve runs the accept loop. Returns when ctx is cancelled or the listener
// is closed. Always returns nil — individual connection errors are logged,
// not bubbled.
func (s *Server) Serve(ctx context.Context) error {
	// Cancelling ctx should unblock Accept(). Since net.UnixListener has no
	// context-aware Accept, we close the listener on cancel and treat the
	// resulting error as a clean stop.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.ln.Close()
		case <-done:
		}
	}()
	defer close(done)

	s.log.Info("agent serving", "socket", s.cfg.SocketPath, "commands", s.cfg.Registry.Commands())

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				s.log.Info("agent accept loop stopped")
				s.conns.Wait()
				return nil
			}
			s.log.Warn("agent accept error", "err", err)
			continue
		}
		s.conns.Add(1)
		go s.serveConn(ctx, conn)
	}
}

// Close stops accepting and waits for in-flight requests to complete.
func (s *Server) Close() error {
	var err error
	s.stopOnce.Do(func() {
		err = s.ln.Close()
		s.conns.Wait()
		// Best-effort remove so the next start-up cleanupStaleSocket
		// doesn't have extra work. Ignore errors — socket may be gone.
		_ = os.Remove(s.cfg.SocketPath)
	})
	return err
}

// serveConn handles exactly one request per connection (matches the client's
// v1 behaviour). A malformed envelope gets a typed error back, then the
// connection is closed unconditionally.
func (s *Server) serveConn(parent context.Context, conn net.Conn) {
	defer s.conns.Done()
	defer func() { _ = conn.Close() }()

	// Peer credential check: verify the connecting process is an allowed UID.
	if len(s.cfg.AllowedUIDs) > 0 {
		uid, err := peerUID(conn)
		if err != nil {
			s.log.Warn("agent peer check failed", "err", err)
			s.writeError(conn, "", &agentwire.AgentError{
				Code:    agentwire.CodePermissionDenied,
				Message: "peer credential check failed",
			})
			return
		}
		allowed := false
		for _, u := range s.cfg.AllowedUIDs {
			if uid == u {
				allowed = true
				break
			}
		}
		if !allowed {
			s.log.Warn("agent rejected connection from unauthorized UID", "uid", uid)
			s.writeError(conn, "", &agentwire.AgentError{
				Code:    agentwire.CodePermissionDenied,
				Message: fmt.Sprintf("UID %d is not authorized", uid),
			})
			return
		}
	}

	// Outer deadline: if the caller's Request.Deadline is missing or bogus,
	// PerRequestTimeout stops a rogue peer from wedging us.
	if s.cfg.PerRequestTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.cfg.PerRequestTimeout))
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64<<10), s.cfg.MaxRequestBytes)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			s.log.Debug("agent read error", "err", err)
		}
		return
	}
	line := scanner.Bytes()

	var req agentwire.Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeError(conn, "", &agentwire.AgentError{
			Code:    agentwire.CodeMalformedEnvelope,
			Message: fmt.Sprintf("parse request: %v", err),
		})
		return
	}
	if req.Command == "" {
		s.writeError(conn, req.ID, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "command is required",
		})
		return
	}

	// Derive the per-request context. Respect request.Deadline; fall back to
	// server-side per-request timeout if the client didn't specify one.
	ctx := parent
	if req.Deadline != "" {
		if dl, err := time.Parse(time.RFC3339Nano, req.Deadline); err == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, dl)
			defer cancel()
			// Extend the socket deadline too so writeResponse below
			// doesn't hit the 120s socket cap when the operator gave
			// us a longer request deadline (e.g. 4h system restore).
			_ = conn.SetDeadline(dl)
		}
	} else if s.cfg.PerRequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.PerRequestTimeout)
		defer cancel()
	}

	data, agentErr := s.cfg.Registry.Dispatch(ctx, req.Command, req.Params)

	resp := agentwire.Response{ID: req.ID, Ok: agentErr == nil}
	if agentErr != nil {
		resp.Error = agentErr
	} else {
		resp.Data = data
	}
	// Clear the socket deadline before writing the response. The earlier
	// SetDeadline(dl) above is for read-side timeout enforcement; once
	// the handler returns (success OR ctx.Done), the deadline may have
	// already expired and a write would silently fail with i/o timeout
	// — leaving the client hung on EOF. The connection closes right
	// after this write anyway, so a far-future write deadline is safe.
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	s.writeResponse(conn, resp)
	s.log.Debug("agent request handled", "id", req.ID, "command", req.Command, "ok", resp.Ok)
}

func (s *Server) writeResponse(conn net.Conn, resp agentwire.Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		s.log.Error("agent response encode failed", "err", err, "id", resp.ID)
		return
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		s.log.Debug("agent response write failed", "err", err, "id", resp.ID)
	}
}

func (s *Server) writeError(conn net.Conn, id string, ae *agentwire.AgentError) {
	s.writeResponse(conn, agentwire.Response{ID: id, Ok: false, Error: ae})
}

// cleanupStaleSocket removes a socket file if it exists but no one is
// listening. If a process IS listening, we leave it alone so net.Listen
// reports the real EADDRINUSE rather than our accidentally-deleting it.
func cleanupStaleSocket(path string) error {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("path %s exists and is not a socket", path)
	}
	// Probe: try to dial it. If we can, someone's running — bail.
	conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("socket %s is already in use", path)
	}
	return os.Remove(path)
}
