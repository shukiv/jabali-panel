package commands

// M45 root web terminal — PTY broker (ADR-0096).
//
// A SEPARATE unix-socket listener (NOT agentwire, which is req/resp
// JSON-RPC). panel-api validates the one-shot token + the off-by-
// default gate, then dials this socket and pumps opaque frames. The
// agent runs as root, so the spawned PTY is a true root shell. Every
// byte both directions is teed to an asciinema v2 .cast for forensic
// replay.
//
// Frame wire (both directions): [1B opcode][4B BE length][payload].
//   0x10 init   api→agent  JSON {session_id,cols,rows}  (first frame)
//   0x00 stdout agent→api  raw PTY output
//   0x01 stdin  api→agent  raw keystrokes
//   0x02 resize api→agent  JSON {cols,rows}
//   0x03 exit   agent→api  4B BE exit code
//
// Security: the listener is root:<jabali-sockets gid> 0660, so only a
// jabali-group process (panel-api) can connect. The gate + token are
// enforced panel-api-side; this broker is unreachable otherwise.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const (
	tpOpInit   byte = 0x10
	tpOpStdout byte = 0x00
	tpOpStdin  byte = 0x01
	tpOpResize byte = 0x02
	tpOpExit   byte = 0x03

	tpIdleTimeout = 15 * time.Minute
	tpMaxDuration = 4 * time.Hour
	tpMaxFrame    = 1 << 20 // 1 MiB per frame guard
)

type tpInit struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

type tpResize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// writeFrame is mutex-guarded: the PTY-reader goroutine and the
// timeout/exit path both write to the same conn.
type tpConn struct {
	c  net.Conn
	mu sync.Mutex
}

func (t *tpConn) write(op byte, payload []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var hdr [5]byte
	hdr[0] = op
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := t.c.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := t.c.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func tpReadFrame(c net.Conn) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > tpMaxFrame {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	buf := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(c, buf); err != nil {
			return 0, nil, err
		}
	}
	return hdr[0], buf, nil
}

// StartTerminalPTYBroker launches the listener goroutine. recordDir is
// /var/log/jabali/terminal. Best-effort: a bind failure is logged and
// the agent keeps serving everything else (the feature is gated off by
// default anyway).
func StartTerminalPTYBroker(ctx context.Context, sockPath string, gid int, recordDir string, log *slog.Logger) {
	go func() {
		_ = os.MkdirAll(filepath.Dir(sockPath), 0o750)
		_ = os.Remove(sockPath) // stale socket from a previous run
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			log.Warn("terminal PTY broker: listen failed", "path", sockPath, "err", err)
			return
		}
		_ = os.Chmod(sockPath, 0o660)
		if gid >= 0 {
			_ = os.Chown(sockPath, 0, gid) // root:<jabali-sockets>
		}
		_ = os.MkdirAll(recordDir, 0o750)
		log.Info("terminal PTY broker listening", "path", sockPath)

		go func() { <-ctx.Done(); _ = ln.Close() }()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					log.Warn("terminal PTY broker: accept", "err", err)
					continue
				}
			}
			go handleTerminalConn(conn, recordDir, gid, log)
		}
	}()
}

func handleTerminalConn(raw net.Conn, recordDir string, gid int, log *slog.Logger) {
	defer raw.Close()

	// Agent-side authorization (Gitea #469). The broker spawns a root shell,
	// so connecting to its socket must be gated independently of panel-api's
	// token/admin checks (socket group perms alone make any jabali-sockets
	// member a root-shell endpoint). panel-api runs with systemd
	// Group=jabali-sockets, i.e. its process PRIMARY gid is the jabali-sockets
	// gid; a process merely ADDED to that group carries it as a SUPPLEMENTARY
	// gid, not primary. SO_PEERCRED reports the peer's primary gid, so
	// requiring peer-gid == jabali-sockets gid (or peer uid 0) accepts only
	// panel-api (and root) and fails closed if creds are unreadable.
	if gid >= 0 {
		uid, pgid, ok := terminalPeerCred(raw)
		if !ok {
			log.Warn("terminal: refusing connection: cannot read peer credentials")
			return
		}
		if !terminalPeerAllowed(uid, pgid, gid) {
			log.Warn("terminal: refusing connection: peer is not panel-api", "peer_uid", uid, "peer_gid", pgid, "want_gid", gid)
			return
		}
	}

	conn := &tpConn{c: raw}

	// First frame must be init.
	op, payload, err := tpReadFrame(raw)
	if err != nil || op != tpOpInit {
		log.Warn("terminal: bad init frame", "op", op, "err", err)
		return
	}
	var init tpInit
	if err := json.Unmarshal(payload, &init); err != nil || init.SessionID == "" {
		log.Warn("terminal: bad init json", "err", err)
		return
	}
	if init.Cols == 0 {
		init.Cols = 80
	}
	if init.Rows == 0 {
		init.Rows = 24
	}

	// asciinema v2 recorder. Filename = session id; panel-api persists
	// the same path on the terminal_sessions row.
	castPath := filepath.Join(recordDir, sanitizeSessionID(init.SessionID)+".cast")
	cast, cerr := os.OpenFile(castPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if cerr != nil {
		log.Warn("terminal: cannot open cast file", "path", castPath, "err", cerr)
		return
	}
	defer cast.Close()
	start := time.Now()
	hdr, _ := json.Marshal(map[string]any{
		"version":   2,
		"width":     init.Cols,
		"height":    init.Rows,
		"timestamp": start.Unix(),
		"env":       map[string]string{"SHELL": "/bin/bash", "TERM": "xterm-256color"},
	})
	var castMu sync.Mutex
	writeCast := func(kind, data string) {
		castMu.Lock()
		defer castMu.Unlock()
		ev, _ := json.Marshal([]any{time.Since(start).Seconds(), kind, data})
		_, _ = cast.Write(append(ev, '\n'))
	}
	castMu.Lock()
	_, _ = cast.Write(append(hdr, '\n'))
	castMu.Unlock()

	// Spawn the root shell. Agent is uid 0 so no privilege juggling;
	// new session + controlling TTY via creack/pty.
	cmd := execCommand("/bin/bash", "-l")
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"JABALI_ROOT_TERMINAL=1",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: init.Cols, Rows: init.Rows})
	if err != nil {
		log.Error("terminal: pty start failed", "err", err)
		_ = conn.write(tpOpExit, beU32(1))
		return
	}
	defer func() { _ = ptmx.Close() }()

	// Kill the whole process group on any exit path.
	killGroup := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	defer killGroup()

	idle := time.NewTimer(tpIdleTimeout)
	maxd := time.NewTimer(tpMaxDuration)
	defer idle.Stop()
	defer maxd.Stop()
	done := make(chan struct{})
	var doneOnce sync.Once
	finish := func() { doneOnce.Do(func() { close(done) }) }

	// PTY → conn (+ cast "o").
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				if werr := conn.write(tpOpStdout, chunk); werr != nil {
					finish()
					return
				}
				writeCast("o", string(chunk))
			}
			if rerr != nil {
				finish()
				return
			}
		}
	}()

	// conn → PTY (+ cast "i").
	go func() {
		for {
			op, p, rerr := tpReadFrame(raw)
			if rerr != nil {
				finish()
				return
			}
			switch op {
			case tpOpStdin:
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(tpIdleTimeout)
				if _, werr := ptmx.Write(p); werr != nil {
					finish()
					return
				}
				writeCast("i", string(p))
			case tpOpResize:
				var rs tpResize
				if json.Unmarshal(p, &rs) == nil && rs.Cols > 0 && rs.Rows > 0 {
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: rs.Cols, Rows: rs.Rows})
				}
			case tpOpExit:
				finish()
				return
			}
		}
	}()

	// Reap the shell so a clean `exit` ends the session promptly.
	waitCh := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waitCh) }()

	select {
	case <-done:
	case <-waitCh:
	case <-idle.C:
		log.Info("terminal: idle timeout", "session", init.SessionID)
	case <-maxd.C:
		log.Info("terminal: max-duration timeout", "session", init.SessionID)
	}
	_ = conn.write(tpOpExit, beU32(0))
}

func beU32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// sanitizeSessionID keeps the cast filename to the ULID charset so a
// crafted session id can't traverse out of recordDir.
func sanitizeSessionID(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s) && i < 32; i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "invalid"
	}
	return string(out)
}

// terminalPeerCred reads the connecting peer's uid + primary gid via
// SO_PEERCRED (Gitea #469). ok=false when the conn is not a unix socket or the
// credentials cannot be read, so callers fail closed.
// terminalPeerAllowed decides whether a broker peer may spawn a root shell
// (Gitea #469 / GH #515). root (uid 0) is always allowed. Everyone else must
// present the expected PRIMARY gid: panel-api runs with systemd
// Group=jabali-sockets, so its SO_PEERCRED primary gid is the jabali-sockets
// gid — which is what the broker is handed via -pty-gid. Matching the
// jabali-sockets gid (NOT the jabali gid of the main agent socket) is what
// #515 got wrong: one -gid conflated two groups and refused panel-api on every
// install. Fails closed for any other primary gid.
func terminalPeerAllowed(uid, pgid, wantGID int) bool {
	if uid == 0 {
		return true
	}
	return pgid == wantGID
}

func terminalPeerCred(c net.Conn) (uid, gid int, ok bool) {
	uc, isUnix := c.(*net.UnixConn)
	if !isUnix {
		return 0, 0, false
	}
	sc, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var cred *unix.Ucred
	var cerr error
	ctrlErr := sc.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if ctrlErr != nil || cerr != nil || cred == nil {
		return 0, 0, false
	}
	return int(cred.Uid), int(cred.Gid), true
}
