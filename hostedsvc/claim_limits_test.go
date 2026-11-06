package hostedsvc

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
)

// newFileStore opens a store the way the service does: a file database in
// WAL mode with a busy timeout and a pool of connections, so tries really
// run at the same time.
func newFileStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "svc.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// consumeAtOnce runs one ConsumeCode call per code, released together, and
// returns their errors.
func consumeAtOnce(s *Store, email string, codes []string) []error {
	errs := make([]error, len(codes))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, code := range codes {
		wg.Add(1)
		go func(i int, code string) {
			defer wg.Done()
			<-start
			errs[i] = s.ConsumeCode(email, code)
		}(i, code)
	}
	close(start)
	wg.Wait()
	return errs
}

func TestConsumeCode_TriesAtOnceStopAtTheCap(t *testing.T) {
	s := newFileStore(t)
	const email = "race@example.com"
	if err := s.PutCode(email, "123456"); err != nil {
		t.Fatal(err)
	}
	wrong := make([]string, 4*codeMaxAttempts)
	for i := range wrong {
		wrong[i] = fmt.Sprintf("%06d", 900000+i)
	}
	for _, err := range consumeAtOnce(s, email, wrong) {
		if err == nil {
			t.Fatal("a wrong code was accepted")
		}
	}
	var attempts int
	if err := s.db.QueryRow(`SELECT attempts FROM email_codes WHERE email = ?`, email).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	// Every try that got to compare the code counted one attempt, so the
	// count is the number of codes compared.
	if attempts != codeMaxAttempts {
		t.Fatalf("%d codes compared, want at most %d", attempts, codeMaxAttempts)
	}
	if err := s.ConsumeCode(email, "123456"); err == nil {
		t.Fatal("the real code still works after the cap")
	}
}

func TestConsumeCode_RightCodeAtOnceSucceedsOnce(t *testing.T) {
	s := newFileStore(t)
	const email = "once@example.com"
	if err := s.PutCode(email, "123456"); err != nil {
		t.Fatal(err)
	}
	codes := make([]string, 16)
	for i := range codes {
		codes[i] = "123456"
	}
	ok := 0
	for _, err := range consumeAtOnce(s, email, codes) {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrCodeInvalid):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("code accepted %d times, want once", ok)
	}
}

func TestClaim_SourceAddressHourlyCap(t *testing.T) {
	a, _, _ := newTestAPI(t)
	h := a.Routes()
	for i := 0; i < claimIPHourlyCap; i++ {
		w, _ := call(t, h, "/v1/claim", "45.79.1.9", ClaimRequest{Email: fmt.Sprintf("u%d@example.com", i), Code: "000000"})
		if w.Code != http.StatusForbidden {
			t.Fatalf("try %d: HTTP %d %s, want 403 (no such code)", i, w.Code, w.Body.String())
		}
	}
	w, out := call(t, h, "/v1/claim", "45.79.1.9", ClaimRequest{Email: "next@example.com", Code: "000000"})
	if w.Code != http.StatusTooManyRequests || out["error"] != "rate_limited" {
		t.Fatalf("over the cap: HTTP %d %s, want 429 rate_limited", w.Code, w.Body.String())
	}
	// Another source address is not held back.
	if w, _ := call(t, h, "/v1/claim", "45.79.1.10", ClaimRequest{Email: "next@example.com", Code: "000000"}); w.Code != http.StatusForbidden {
		t.Fatalf("other address: HTTP %d %s, want 403", w.Code, w.Body.String())
	}
}

func TestClaim_EmailHourlyCap(t *testing.T) {
	a, _, _ := newTestAPI(t)
	h := a.Routes()
	const email = "target@example.com"
	for i := 0; i < claimEmailHourlyCap; i++ {
		ip := fmt.Sprintf("45.79.2.%d", i+1)
		if w, _ := call(t, h, "/v1/claim", ip, ClaimRequest{Email: email, Code: "000000"}); w.Code != http.StatusForbidden {
			t.Fatalf("try %d: HTTP %d %s, want 403 (no such code)", i, w.Code, w.Body.String())
		}
	}
	w, out := call(t, h, "/v1/claim", "45.79.3.1", ClaimRequest{Email: email, Code: "000000"})
	if w.Code != http.StatusTooManyRequests || out["error"] != "rate_limited" {
		t.Fatalf("over the cap: HTTP %d %s, want 429 rate_limited", w.Code, w.Body.String())
	}
}

func TestRegister_IPv6CapIsPerSlash64(t *testing.T) {
	a, _, _ := newTestAPI(t)
	h := a.Routes()
	for i := 0; i < registerIPHourlyCap; i++ {
		ip := fmt.Sprintf("2a01:4f8:1:2::%x", i+1)
		if w, _ := call(t, h, "/v1/register", ip, RegisterRequest{Email: fmt.Sprintf("v6-%d@example.com", i)}); w.Code != http.StatusOK {
			t.Fatalf("register %d: HTTP %d %s", i, w.Code, w.Body.String())
		}
	}
	// A new address in the same /64 shares the cap.
	w, _ := call(t, h, "/v1/register", "2a01:4f8:1:2:ffff:ffff:ffff:ffff", RegisterRequest{Email: "v6-next@example.com"})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("same /64 over the cap: HTTP %d %s, want 429", w.Code, w.Body.String())
	}
	// The next /64 has its own.
	if w, _ := call(t, h, "/v1/register", "2a01:4f8:1:3::1", RegisterRequest{Email: "v6-other@example.com"}); w.Code != http.StatusOK {
		t.Fatalf("other /64: HTTP %d %s, want 200", w.Code, w.Body.String())
	}
}

func TestRateKey(t *testing.T) {
	for _, tc := range []struct{ ip, want string }{
		{"45.79.1.9", "45.79.1.9"},
		{"::ffff:45.79.1.9", "45.79.1.9"},
		{"2a01:4f8:1:2::1", "2a01:4f8:1:2::/64"},
		{"2a01:4f8:1:2:aaaa:bbbb:cccc:dddd", "2a01:4f8:1:2::/64"},
		{"2a01:4f8:1:3::1", "2a01:4f8:1:3::/64"},
	} {
		if got := rateKey(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("rateKey(%s) = %q, want %q", tc.ip, got, tc.want)
		}
	}
}
