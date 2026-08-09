// support-claim is the tiny operator-run service that turns a Jabali diagnostic
// bundle's {enclosed link + password} into a short, public-safe claim code.
//
// Why it exists: enclosed (enclosed.jabali-panel.com) is end-to-end encrypted —
// its server never sees the decryption password, so it can't hand a password
// back on redemption. To let a user share a *short* code publicly (e.g. paste
// "JAB-7QX9K2" straight into a GitHub issue) instead of a long URL + password,
// something operator-side has to hold the {url, password} and release it only to
// the authenticated support team. That is this service, and nothing else.
//
// Security model:
//   - Issue (POST /claims) is open: a caller can only shorten data they already
//     hold (their own bundle url+password), so there's no secret to protect on
//     the way in. Bounded by a body-size cap, a per-IP rate limit, and a short
//     TTL.
//   - Redeem (GET /claims/{code}) requires Authorization: Bearer <SUPPORT_TOKEN>.
//     The code alone is useless — that's what makes it safe to paste in public.
//     Redemption is constant-time-compared on the token and audited to stderr.
//   - Storage is in-memory with a TTL sweeper. A restart drops pending claims;
//     that's fine — they're short-lived and the user can regenerate. No secrets
//     touch disk.
//
// Deploy: build this, run it behind TLS next to your enclosed deployment, set
// SUPPORT_CLAIM_TOKEN, then point the panel at it with JABALI_CLAIM_URL in the
// jabali-agent environment. See README.md.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxClaims bounds the in-memory claim store. Entries live for the full TTL
// and each holds up to ~16 KiB, on an unauthenticated endpoint — without a
// ceiling the heap grows until the process is OOM-killed.
const maxClaims = 10000

// errStoreFull is returned when maxClaims is reached; the handler maps it to
// 503 so the caller can retry later rather than seeing a generic failure.
var errStoreFull = errors.New("claim store is full")

const (
	// codeAlphabet is Crockford base32 minus vowels/ambiguous chars, so a code
	// is easy to read aloud and hard to typo (no O/0, I/1, L, U).
	codeAlphabet = "23456789BCDFGHJKMNPQRSTVWXYZ"
	codePrefix   = "JAB-"
	codeLen      = 8
	maxBodyBytes = 16 * 1024
)

// claim is one stored {url, password, metadata} keyed by a public code.
type claim struct {
	Payload   redeemPayload
	ExpiresAt time.Time
}

// issueRequest is what the panel POSTs after uploading a bundle to enclosed.
type issueRequest struct {
	URL      string `json:"url"`
	Password string `json:"password"`
	Host     string `json:"host,omitempty"`
	PanelSHA string `json:"panel_sha,omitempty"`
	NoteID   string `json:"note_id,omitempty"`
	Bytes    int64  `json:"byte_count,omitempty"`
	Files    int    `json:"file_count,omitempty"`
}

// redeemPayload is what the support team receives on a valid, authenticated
// redemption.
type redeemPayload struct {
	URL      string `json:"url"`
	Password string `json:"password"`
	Host     string `json:"host,omitempty"`
	PanelSHA string `json:"panel_sha,omitempty"`
	NoteID   string `json:"note_id,omitempty"`
	Bytes    int64  `json:"byte_count,omitempty"`
	Files    int    `json:"file_count,omitempty"`
}

type store struct {
	mu     sync.Mutex
	claims map[string]claim
	ttl    time.Duration
}

func newStore(ttl time.Duration) *store {
	return &store{claims: map[string]claim{}, ttl: ttl}
}

// put stores a payload under a fresh unique code and returns the code.
func (s *store) put(p redeemPayload, now time.Time) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var code string
	for i := 0; i < 8; i++ {
		c, err := genCode()
		if err != nil {
			return "", time.Time{}, err
		}
		if _, exists := s.claims[c]; !exists {
			code = c
			break
		}
	}
	if code == "" {
		return "", time.Time{}, fmt.Errorf("could not allocate a unique code")
	}
	// Hard cap. Claims are held in memory for the full TTL (14 days) and each
	// carries up to ~16 KiB of payload, on an unauthenticated endpoint — with
	// no ceiling, a caller simply posting claims grows the heap until the
	// process is OOM-killed, taking down the service support depends on.
	// Refuse new claims instead; existing ones still redeem.
	if len(s.claims) >= maxClaims {
		return "", time.Time{}, errStoreFull
	}
	exp := now.Add(s.ttl)
	s.claims[code] = claim{Payload: p, ExpiresAt: exp}
	return code, exp, nil
}

// get returns the payload for a code, or ok=false when it is unknown or expired.
func (s *store) get(code string, now time.Time) (redeemPayload, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.claims[code]
	if !ok || now.After(c.ExpiresAt) {
		if ok {
			delete(s.claims, code) // lazy eviction
		}
		return redeemPayload{}, false
	}
	return c.Payload, true
}

// sweep evicts expired claims. Called periodically.
func (s *store) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for code, c := range s.claims {
		if now.After(c.ExpiresAt) {
			delete(s.claims, code)
		}
	}
}

// genCode returns a random public code like "JAB-7QX9K2P4".
func genCode() (string, error) {
	buf := make([]byte, codeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(codePrefix)
	for _, x := range buf {
		b.WriteByte(codeAlphabet[int(x)%len(codeAlphabet)])
	}
	return b.String(), nil
}

type server struct {
	store *store
	// token is the bearer secret the support team presents to redeem. Empty
	// disables redemption (issue-only) — refuse to start in that state.
	token string
	limit *rateLimiter
	now   func() time.Time
}

func (srv *server) handleIssue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !srv.limit.allow(clientIP(r)) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	var req issueRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.URL == "" || req.Password == "" {
		http.Error(w, "url and password are required", http.StatusBadRequest)
		return
	}
	code, exp, err := srv.store.put(redeemPayload(req), srv.now())
	if err != nil {
		if errors.Is(err, errStoreFull) {
			// Distinguish "at capacity, retry later" from a genuine internal
			// fault so an operator sees the real cause in the response and
			// the log rather than a generic 500.
			log.Printf("claim store full (%d entries) — refusing new claim", maxClaims)
			http.Error(w, "claim store is full, try again later", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("issued claim %s (host=%q note=%q expires=%s)", code, req.Host, req.NoteID, exp.UTC().Format(time.RFC3339))
	writeJSON(w, http.StatusOK, map[string]any{
		"code":       code,
		"expires_at": exp.UTC().Format(time.RFC3339),
	})
}

func (srv *server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !srv.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	code := strings.TrimPrefix(r.URL.Path, "/claims/")
	if code == "" || strings.Contains(code, "/") {
		http.Error(w, "bad code", http.StatusBadRequest)
		return
	}
	payload, ok := srv.store.get(code, srv.now())
	if !ok {
		http.Error(w, "not found or expired", http.StatusNotFound)
		return
	}
	log.Printf("redeemed claim %s (host=%q)", code, payload.Host)
	writeJSON(w, http.StatusOK, payload)
}

// authorized constant-time compares the request's bearer token to the secret.
func (srv *server) authorized(r *http.Request) bool {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, p) {
		return false
	}
	got := strings.TrimPrefix(h, p)
	return subtle.ConstantTimeCompare([]byte(got), []byte(srv.token)) == 1
}

func (srv *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/claims", srv.handleIssue)     // POST — issue
	mux.HandleFunc("/claims/", srv.handleRedeem)   // GET /claims/{code} — redeem
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// clientIP returns the address the rate limiter keys on.
//
// X-Forwarded-For is honoured ONLY when the immediate peer is loopback, i.e. a
// reverse proxy on this host that sets the header itself. Trusting it from any
// peer made the per-IP limit meaningless — a direct caller could rotate the
// header on every request to bypass the 30/min cap on the open POST /claims,
// and each distinct value also allocated a permanent bucket entry, so the same
// trick grew the process heap without bound. Nothing in code or deployment
// guarantees this service is proxy-fronted, and :8088 is reachable directly.
// Mirrors hostedsvc/realip.go.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if peer := net.ParseIP(host); peer != nil && peer.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				if ip := net.ParseIP(first); ip != nil {
					return ip.String()
				}
			}
		}
	}
	return host
}

func main() {
	token := os.Getenv("SUPPORT_CLAIM_TOKEN")
	if token == "" {
		log.Fatal("SUPPORT_CLAIM_TOKEN is required (the bearer secret the support team redeems with)")
	}
	listen := envOr("SUPPORT_CLAIM_LISTEN", ":8088")
	ttlHours, _ := strconv.Atoi(envOr("SUPPORT_CLAIM_TTL_HOURS", "336")) // 14 days
	if ttlHours <= 0 {
		ttlHours = 336
	}
	rpm, _ := strconv.Atoi(envOr("SUPPORT_CLAIM_RATE_PER_MIN", "30"))

	st := newStore(time.Duration(ttlHours) * time.Hour)
	srv := &server{store: st, token: token, limit: newRateLimiter(rpm), now: time.Now}

	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			st.sweep(time.Now())
		}
	}()

	log.Printf("support-claim listening on %s (ttl=%dh, rate=%d/min)", listen, ttlHours, rpm)
	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(httpSrv.ListenAndServe())
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
