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
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/net/webdav"
)

func main() {
	socket := flag.String("socket", "", "unix socket path to listen on")
	root := flag.String("root", "", "directory to serve (the subaccount home)")
	prefix := flag.String("prefix", "", "URL path prefix to strip (e.g. /dav/<sub>)")
	flag.Parse()

	if *socket == "" || *root == "" {
		log.Fatal("jabali-webdav: --socket and --root are required")
	}
	if fi, err := os.Stat(*root); err != nil || !fi.IsDir() {
		log.Fatalf("jabali-webdav: --root %q is not a directory: %v", *root, err)
	}

	// Stale socket from a previous run (systemd restart) would make Listen fail
	// with EADDRINUSE; the worker owns this path, so removing it is safe.
	_ = os.Remove(*socket)
	ln, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatalf("jabali-webdav: listen %s: %v", *socket, err)
	}
	// 0660: nginx (www-data, added to the socket group in step 3/4) connects;
	// no other tenant can. The worker runs as the subaccount uid, so the socket
	// is owned by that uid:group.
	if err := os.Chmod(*socket, 0o660); err != nil {
		log.Printf("jabali-webdav: chmod socket %s: %v", *socket, err)
	}

	srv := &http.Server{
		Handler:           newWebdavHandler(*root, *prefix),
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

	log.Printf("jabali-webdav: serving %s on %s (prefix %q)", *root, *socket, *prefix)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("jabali-webdav: serve: %v", err)
	}
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
