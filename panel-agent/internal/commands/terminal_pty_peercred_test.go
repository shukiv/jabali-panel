package commands

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestTerminalPeerCred covers the PTY broker's SO_PEERCRED gate (Gitea #469):
// over a real unix socket the peer's uid/gid are read (ok=true) and match the
// running process, and a non-unix conn fails closed (ok=false) so the caller
// refuses the connection.
func TestTerminalPeerCred(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "t.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()

	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	srv := <-accepted
	if srv == nil {
		t.Fatal("accept failed")
	}
	defer srv.Close()

	uid, gid, ok := terminalPeerCred(srv)
	if !ok {
		t.Fatal("expected to read peer credentials over a unix socket")
	}
	if uid != os.Getuid() {
		t.Errorf("peer uid = %d, want %d", uid, os.Getuid())
	}
	if gid != os.Getgid() {
		t.Errorf("peer gid = %d, want %d", gid, os.Getgid())
	}

	// Non-unix conn must fail closed.
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, _, ok := terminalPeerCred(a); ok {
		t.Error("non-unix conn must fail closed (ok=false)")
	}
}

// TestTerminalPeerAllowed pins the SO_PEERCRED gate (GH #515 / JAB-169): the
// broker must accept panel-api's real PRIMARY gid (jabali-sockets, what
// -pty-gid carries), accept root, and refuse anything else — in particular the
// jabali gid used for the main agent socket, which is what the pre-fix single
// -gid wrongly compared against.
func TestTerminalPeerAllowed(t *testing.T) {
	const (
		jabaliGID        = 996 // main agent-socket gid (WRONG for the broker)
		jabaliSocketsGID = 995 // panel-api primary gid == -pty-gid (correct)
	)
	cases := []struct {
		name            string
		uid, pgid, want int
		allowed         bool
	}{
		{"panel-api primary jabali-sockets", 1000, jabaliSocketsGID, jabaliSocketsGID, true},
		{"root always allowed", 0, 0, jabaliSocketsGID, true},
		{"root with any gid", 0, 12345, jabaliSocketsGID, true},
		{"jabali gid refused (the #515 bug)", 1000, jabaliGID, jabaliSocketsGID, false},
		{"unrelated gid refused", 1000, 1000, jabaliSocketsGID, false},
	}
	for _, tc := range cases {
		if got := terminalPeerAllowed(tc.uid, tc.pgid, tc.want); got != tc.allowed {
			t.Errorf("%s: terminalPeerAllowed(%d,%d,%d) = %v, want %v", tc.name, tc.uid, tc.pgid, tc.want, got, tc.allowed)
		}
	}
}
