// Command jabali-webdav serves ONE FTP/SFTP subaccount's home directory over
// WebDAV on a unix socket (GH #1146, step 2 of plans/gh1146-webdav.md).
//
// It is started as `jabali-webdav@<subaccount>` by the agent when a
// subaccount's webdav_access is enabled (step 3). systemd runs it as the
// subaccount's OWN uid and, for isolated accounts, inside the #1145 chroot jail
// — so every file it writes is uid-owned and kernel-confined to the home, the
// same boundary sftp gets. This process therefore does NOT authenticate or
// authorise: nginx terminates TLS and reverse-proxies here only AFTER a
// privileged auth_request has verified the subaccount's credentials + access
// (step 4). Keeping auth out of the unprivileged worker is deliberate — it runs
// as a tenant uid that can't read /etc/shadow anyway.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/net/webdav"
)

func main() {
	mode := flag.String("mode", "worker", "worker = serve one subaccount's WebDAV (socket-activated, tenant uid); auth = privileged nginx auth_request authenticator (root)")
	socket := flag.String("socket", "", "unix socket path to listen on (worker: tests only, prod uses socket activation; auth: the listen path)")
	root := flag.String("root", "", "worker only: directory to serve (the subaccount home)")
	prefix := flag.String("prefix", "", "worker only: URL path prefix stripped by the WebDAV handler (e.g. /dav)")
	flag.Parse()

	switch *mode {
	case "auth":
		runAuth(*socket)
	case "worker":
		runWorker(*socket, *root, *prefix)
	default:
		log.Fatalf("jabali-webdav: unknown --mode %q (want worker|auth)", *mode)
	}
}

// runWorker serves ONE subaccount's home over WebDAV. Socket-activated in
// production (runs as the subaccount uid inside the #1145 chroot); binds --socket
// directly in tests.
func runWorker(socket, root, prefix string) {
	if root == "" {
		log.Fatal("jabali-webdav: --root is required in worker mode")
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		log.Fatalf("jabali-webdav: --root %q is not a directory: %v", root, err)
	}

	ln, err := acquireListener(socket)
	if err != nil {
		log.Fatalf("jabali-webdav: %v", err)
	}

	srv := &http.Server{
		Handler:           newWebdavHandler(root, prefix),
		ReadHeaderTimeout: 30 * time.Second,
	}

	// Graceful shutdown so an in-flight PUT isn't truncated on `systemctl stop`.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	log.Printf("jabali-webdav: serving %s on %s (prefix %q)", root, ln.Addr(), prefix)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("jabali-webdav: serve: %v", err)
	}
}

// acquireListener returns the unix listener the worker serves on. In production
// systemd passes the already-created listening socket as fd 3 (socket
// activation: LISTEN_PID + LISTEN_FDS) — systemd, as root, created it at
// /run/jabali-webdav/<sub>.sock with SocketMode=0660 SocketGroup=jabali-sockets,
// so the unprivileged worker (a tenant uid, inside the chroot) never binds a
// path or touches socket ownership, and nginx (a jabali-sockets member) can
// connect. In tests / manual runs, --socket makes the worker bind the path
// itself.
func acquireListener(socketPath string) (net.Listener, error) {
	if pidStr := os.Getenv("LISTEN_PID"); pidStr != "" {
		pid, _ := strconv.Atoi(pidStr)
		if pid == os.Getpid() {
			nfds, _ := strconv.Atoi(os.Getenv("LISTEN_FDS"))
			if nfds < 1 {
				return nil, fmt.Errorf("socket activation: LISTEN_FDS=%d, expected >= 1", nfds)
			}
			// systemd hands the first passed fd at SD_LISTEN_FDS_START (3).
			const listenFdsStart = 3
			ln, err := listenerFromFd(listenFdsStart, "systemd-webdav-socket")
			if err != nil {
				return nil, fmt.Errorf("socket activation: %w", err)
			}
			return ln, nil
		}
	}
	if socketPath == "" {
		return nil, fmt.Errorf("no systemd socket (LISTEN_FDS unset) and no --socket given")
	}
	return bindListener(socketPath)
}

// listenerFromFd wraps an inherited file descriptor (systemd socket activation)
// into a net.Listener. Factored out so the fd-to-listener wrapping is testable
// without simulating the exact fd-3 the SD protocol mandates.
func listenerFromFd(fd uintptr, name string) (net.Listener, error) {
	ln, err := net.FileListener(os.NewFile(fd, name))
	if err != nil {
		return nil, fmt.Errorf("FileListener on fd %d: %w", fd, err)
	}
	return ln, nil
}

// bindListener binds the unix socket ourselves — the test / manual path. In
// production systemd owns the socket (activation), so this is never reached.
func bindListener(socketPath string) (net.Listener, error) {
	// Stale socket from a previous manual run would make Listen fail with
	// EADDRINUSE; the worker owns this path, so removing it is safe. (Never
	// reached on the socket-activation path, where systemd owns the socket.)
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		log.Printf("jabali-webdav: chmod socket %s: %v", socketPath, err)
	}
	return ln, nil
}

// newWebdavHandler builds the WebDAV handler for one home directory. webdav.Dir
// lexically confines resource paths to root; the kernel chroot + subaccount uid
// (systemd, step 3) are the real security boundary, mirroring sftp.
func newWebdavHandler(root, prefix string) *webdav.Handler {
	return &webdav.Handler{
		Prefix:     prefix,
		FileSystem: webdav.Dir(root),
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				log.Printf("jabali-webdav: %s %s: %v", r.Method, r.URL.Path, err)
			}
		},
	}
}
