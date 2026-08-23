// Auth mode (jabali-webdav --mode=auth) — the privileged nginx auth_request
// authenticator for WebDAV (GH #1146, step 4 of plans/gh1146-webdav.md).
//
// The per-subaccount worker (worker mode) runs as an unprivileged tenant uid and
// does NO auth. nginx terminates TLS on the panel :8443 vhost, sends each /dav/
// request through auth_request to THIS daemon, and only proxies to the worker
// socket the daemon names. This daemon must run as root: it verifies the
// subaccount's system password with unix_chkpwd (setuid helper; refuses a
// non-root caller checking another user), which is the only credential store —
// the panel never sees or stores subaccount passwords.
//
// Threat model: this endpoint is reachable (post-TLS) by anyone who can hit
// :8443/dav/, so it is a brute-force + password-oracle target. Defenses, in
// order: (1) the username is validated as an OWNED, webdav-enabled, non-system,
// unlocked subaccount BEFORE unix_chkpwd ever runs — so it can never verify a
// password for root/jabali/etc; (2) a per-username token bucket throttles
// guessing; (3) a small global semaphore bounds concurrent (deliberately
// expensive, yescrypt) verifications; (4) no success caching — a locked/disabled
// account fails the very next request (JAB-256). nginx limit_req is the outer
// layer.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	webdavAuthGroup = "jabali-webdav"  // membership = webdav enabled (agent-managed)
	authSocketGroup = "jabali-sockets" // nginx (www-data) is a member → can connect
	minSubaccountUID = 1000            // below this = a system account; never auth
	unixChkpwdPath   = "/usr/sbin/unix_chkpwd"
	unixChkpwdTimeout = 5 * time.Second
	authRealm         = "Jabali WebDAV"
)

// subaccountNameRe matches the <tenant>_<label> namespace (lowercase, digits,
// underscore; must contain a '_'). Beyond rejecting non-subaccount names, this
// guarantees the name is safe to use verbatim as a unix_chkpwd argv element and
// as the /run/jabali-webdav/<name>.sock path component nginx proxies to — no
// slash, dot, or shell metacharacter can appear.
var subaccountNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// runAuth binds the auth socket (as root), sets it to root:jabali-sockets 0660
// so nginx can connect, and serves the auth_request handler.
func runAuth(socket string) {
	if socket == "" {
		log.Fatal("jabali-webdav auth: --socket is required")
	}
	_ = os.Remove(socket) // stale socket from a prior run
	ln, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatalf("jabali-webdav auth: listen %s: %v", socket, err)
	}
	// root:jabali-sockets 0660 — only nginx (a jabali-sockets member) and root
	// may connect; no tenant can reach the authenticator directly.
	if g, gerr := user.LookupGroup(authSocketGroup); gerr == nil {
		if gid, perr := strconv.Atoi(g.Gid); perr == nil {
			if cerr := os.Chown(socket, 0, gid); cerr != nil {
				log.Printf("jabali-webdav auth: chown socket group: %v", cerr)
			}
		}
	} else {
		log.Printf("jabali-webdav auth: group %s not found: %v", authSocketGroup, gerr)
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		log.Printf("jabali-webdav auth: chmod socket: %v", err)
	}

	// Defense in depth beyond the 0660 root:jabali-sockets mode: jabali-sockets
	// also contains the panel (jabali) and webmail uids, so socket perms alone
	// would let a compromised panel/webmail process brute-force or oracle every
	// subaccount password here offline (no nginx limit_req, no CrowdSec). Gate on
	// SO_PEERCRED: only root and nginx (www-data) may connect. Mirrors the agent
	// socket's UID allow-list (JAB-366).
	allowed := map[uint32]bool{0: true}
	if wu, werr := user.Lookup("www-data"); werr == nil {
		if wuid, perr := strconv.Atoi(wu.Uid); perr == nil {
			allowed[uint32(wuid)] = true
		}
	} else {
		log.Printf("jabali-webdav auth: www-data not found — only root may connect: %v", werr)
	}
	ln = &peerCredListener{Listener: ln, allowed: allowed}

	a := newAuthenticator()
	srv := &http.Server{
		Handler:           a,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	log.Printf("jabali-webdav auth: authenticator on %s", socket)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("jabali-webdav auth: serve: %v", err)
	}
}

// peerCredListener rejects any connection whose peer uid is not in allowed,
// checked via SO_PEERCRED at accept time. Only root and nginx (www-data) may
// reach the authenticator; every other local uid is closed immediately.
type peerCredListener struct {
	net.Listener
	allowed map[uint32]bool
}

func (l *peerCredListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, uerr := connPeerUID(c)
		if uerr != nil || !l.allowed[uid] {
			if uerr == nil {
				log.Printf("jabali-webdav auth: rejected connection from uid %d (only root + www-data allowed)", uid)
			}
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

func connPeerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if cerr := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); cerr != nil {
		return 0, cerr
	}
	if credErr != nil {
		return 0, credErr
	}
	return cred.Uid, nil
}

// authenticator holds the rate-limiting state + concurrency cap. The four
// system lookups are seams so tests can exercise the full allow/deny flow
// without real users, groups, /etc/shadow, or unix_chkpwd.
type authenticator struct {
	limiter        *loginLimiter
	sem            chan struct{}
	lookupUser     func(string) (*user.User, error)
	inGroup        func(u *user.User, group string) bool
	locked         func(username string) bool
	verifyPassword func(username, password string) bool
}

func newAuthenticator() *authenticator {
	return &authenticator{
		limiter:        newLoginLimiter(),
		sem:            make(chan struct{}, 4), // bound concurrent yescrypt verifies
		lookupUser:     user.Lookup,
		inGroup:        userInGroup,
		locked:         accountLocked,
		verifyPassword: verifyViaUnixChkpwd,
	}
}

// ServeHTTP handles one nginx auth_request subrequest. 200 = allow (with the
// resolved worker in X-Webdav-Sub); 401 = deny. The daemon never distinguishes
// "no such user" from "wrong password" from "not enabled" — every deny is a bare
// 401, so the endpoint is not an account-enumeration oracle.
func (a *authenticator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	username, password, ok := parseBasicAuth(r.Header.Get("Authorization"))
	if !ok || password == "" {
		// Empty password is never valid for a subaccount; reject before any
		// lookup so it can't probe account existence either.
		a.deny(w)
		return
	}
	// Validate identity BEFORE touching the password: the name must be a
	// well-formed subaccount that exists, is a non-system uid, is webdav-enabled
	// (jabali-webdav group), and is not locked. Only then is it eligible for a
	// password check — so unix_chkpwd is never invoked for root/jabali/etc.
	if !subaccountNameRe.MatchString(username) || strings.IndexByte(username, '_') < 0 {
		a.deny(w)
		return
	}
	u, err := a.lookupUser(username)
	if err != nil {
		a.deny(w)
		return
	}
	if uid, perr := strconv.Atoi(u.Uid); perr != nil || uid < minSubaccountUID {
		a.deny(w)
		return
	}
	if !a.inGroup(u, webdavAuthGroup) {
		a.deny(w)
		return
	}
	if a.locked(username) {
		a.deny(w)
		return
	}
	// Per-username throttle: reject fast, before the expensive verify, once the
	// bucket is empty — the brute-force cap. A token is consumed here and
	// REFUNDED on success below, so a client sending the correct password (HTTP
	// Basic re-sends creds on every request, and we never cache) is effectively
	// unthrottled, while wrong-password floods drain the bucket and get 401'd.
	if !a.limiter.allow(username) {
		a.deny(w)
		return
	}
	// Bound concurrent verifications (each unix_chkpwd fork + yescrypt is
	// deliberately costly — the DoS surface).
	a.sem <- struct{}{}
	verified := a.verifyPassword(username, password)
	<-a.sem
	if !verified {
		a.deny(w)
		return
	}
	a.limiter.refund(username)
	// Success. X-Webdav-Sub is the username WE validated (exists, in-group,
	// charset-clean) — never a value echoed from an untrusted request header, so
	// nginx can safely build the worker socket path from it.
	w.Header().Set("X-Webdav-Sub", username)
	w.WriteHeader(http.StatusOK)
}

func (a *authenticator) deny(w http.ResponseWriter) {
	// The realm is also emitted by nginx on its 401 path (auth_request drops
	// upstream headers on deny); set it here too for completeness.
	w.Header().Set("WWW-Authenticate", `Basic realm="`+authRealm+`"`)
	w.WriteHeader(http.StatusUnauthorized)
}

// parseBasicAuth extracts the credentials from an "Authorization: Basic ..."
// header. Uses the stdlib parser via a throwaway request so decoding matches
// net/http exactly.
func parseBasicAuth(header string) (username, password string, ok bool) {
	if header == "" {
		return "", "", false
	}
	r := &http.Request{Header: http.Header{"Authorization": {header}}}
	return r.BasicAuth()
}

// userInGroup reports whether u is a member of groupName (by gid).
func userInGroup(u *user.User, groupName string) bool {
	g, err := user.LookupGroup(groupName)
	if err != nil {
		return false
	}
	gids, err := u.GroupIds()
	if err != nil {
		return false
	}
	for _, gid := range gids {
		if gid == g.Gid {
			return true
		}
	}
	return false
}

// accountLocked reports whether username's shadow password is locked (usermod -L
// prepends '!'; '*'/empty are never-set). A locked account is a disabled or
// suspended subaccount (JAB-254/256) and must fail auth immediately. Root-only
// read; on any error we fail SAFE (treat as locked → deny).
func accountLocked(username string) bool {
	data, err := os.ReadFile("/etc/shadow")
	if err != nil {
		return true
	}
	return shadowLocked(string(data), username)
}

// shadowLocked reports whether username's shadow entry denies password auth. It
// is the pure core of accountLocked (testable without /etc/shadow). Deny when the
// hash is locked (usermod -L prepends '!'), disabled ('*'), unset/empty, or the
// user has no shadow entry at all — every one of these means "no usable password"
// and must fail CLOSED. Only a real crypt hash ($y$/$6$/…) allows the auth to
// proceed to unix_chkpwd.
func shadowLocked(shadow, username string) bool {
	for _, line := range strings.Split(shadow, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 2 || parts[0] != username {
			continue
		}
		h := parts[1]
		return h == "" || h[0] == '!' || h[0] == '*'
	}
	return true // no shadow entry → not a real login account → deny
}

// verifyViaUnixChkpwd checks password against username's /etc/shadow entry using
// the setuid unix_chkpwd helper — the system's own crypt (yescrypt on Debian 13),
// so no crypt library lives in this auth path. Must run as root (the helper
// refuses a non-root caller checking another user). The password is written to
// the helper's stdin (NUL-terminated), never argv.
func verifyViaUnixChkpwd(username, password string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), unixChkpwdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, unixChkpwdPath, username, "nonull")
	cmd.Stdin = strings.NewReader(password + "\x00")
	err := cmd.Run()
	return err == nil // exit 0 = password matches
}

// ---- per-username token-bucket rate limiter ----

type loginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // tokens per second
	burst   float64
	maxKeys int
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    0.5,   // 1 attempt / 2s sustained
		burst:   5,     // small burst for a legit client's first requests
		maxKeys: 50000, // bound memory against username-cycling attackers
	}
}

// allow consumes one token for key, refilling by elapsed time. Returns false
// when the bucket is empty (too many recent attempts).
func (l *loginLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > l.maxKeys {
		// Crude overflow guard: an attacker cycling distinct usernames must not
		// grow the map without bound. Dropping it costs legit clients at most one
		// burst refill.
		l.buckets = make(map[string]*tokenBucket)
	}
	b := l.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// refund returns one token to key's bucket (capped at burst), used after a
// SUCCESSFUL verify so correct-password requests don't deplete the throttle.
func (l *loginLimiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.buckets[key]; b != nil {
		b.tokens++
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
}
