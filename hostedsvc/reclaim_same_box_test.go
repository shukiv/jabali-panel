package hostedsvc

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"testing"

	_ "modernc.org/sqlite"
)

// claimWithSeededCode claims with a code put straight into the store, so a
// test can claim twice for one email without waiting out the per-email
// resend gap that /v1/register enforces (tested separately).
func claimWithSeededCode(t *testing.T, a *API, email, ip string) (int, map[string]any) {
	t.Helper()
	if err := a.Store.PutCode(email, "111111"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.db.Exec(`UPDATE email_codes SET sent_at = 0 WHERE email = ?`, email); err != nil {
		t.Fatal(err)
	}
	w, out := call(t, a.Routes(), "/v1/claim", ip, ClaimRequest{Email: email, Code: "111111"})
	return w.Code, out
}

func labelRows(t *testing.T, a *API) int {
	t.Helper()
	var n int
	if err := a.Store.db.QueryRow(`SELECT COUNT(*) FROM labels`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A claim retried from the same box (same verified email, same address) whose
// first setup never checked in gets the same label back with a new token,
// not a new collision suffix. Field data: one IP held 9 labels for 1 email.
func TestClaimReissuesNeverCheckedInLabel(t *testing.T) {
	a, _, _ := newTestAPI(t)
	h := a.Routes()

	code, first := claimWithSeededCode(t, a, "op@example.com", "45.79.1.9")
	if code != 200 {
		t.Fatalf("first claim: HTTP %d %v", code, first)
	}
	code, second := claimWithSeededCode(t, a, "op@example.com", "45.79.1.9")
	if code != 200 {
		t.Fatalf("second claim: HTTP %d %v", code, second)
	}
	if second["label"] != first["label"] || second["label"] != "45-79-1-9" {
		t.Fatalf("retry got %v, first was %v — want the same label back", second["label"], first["label"])
	}
	if second["token"] == first["token"] {
		t.Fatal("reissue kept the old token")
	}
	if n := labelRows(t, a); n != 1 {
		t.Fatalf("label rows = %d, want 1", n)
	}
	if w, _ := call(t, h, "/v1/heartbeat", "45.79.1.9", TokenRequest{Token: first["token"].(string)}); w.Code != http.StatusUnauthorized {
		t.Errorf("old token after reissue: HTTP %d, want 401", w.Code)
	}
	if w, _ := call(t, h, "/v1/heartbeat", "45.79.1.9", TokenRequest{Token: second["token"].(string)}); w.Code != 200 {
		t.Errorf("new token: HTTP %d", w.Code)
	}
}

// Once the box checked in — by heartbeat or by issuing its cert — the label is
// in use and a claim from the same email and address (a second box behind the
// same NAT) gets its own suffix; the first box keeps its token.
func TestClaimNeverReissuesCheckedInLabel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		checkIn func(t *testing.T, a *API, token string)
	}{
		{"heartbeat", func(t *testing.T, a *API, token string) {
			if w, _ := call(t, a.Routes(), "/v1/heartbeat", "45.79.1.9", TokenRequest{Token: token}); w.Code != 200 {
				t.Fatalf("heartbeat: HTTP %d", w.Code)
			}
		}},
		{"acme present", func(t *testing.T, a *API, token string) {
			if w, _ := call(t, a.Routes(), "/v1/acme/present", "45.79.1.9", AcmePresentRequest{Token: token, TXT: "v"}); w.Code != 200 {
				t.Fatalf("acme present: HTTP %d", w.Code)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newTestAPI(t)
			_, first := claimWithSeededCode(t, a, "op@example.com", "45.79.1.9")
			tc.checkIn(t, a, first["token"].(string))

			code, second := claimWithSeededCode(t, a, "op@example.com", "45.79.1.9")
			if code != 200 {
				t.Fatalf("second claim: HTTP %d %v", code, second)
			}
			if second["label"] != "45-79-1-9-b" {
				t.Fatalf("second box got %v, want 45-79-1-9-b", second["label"])
			}
			if w, _ := call(t, a.Routes(), "/v1/heartbeat", "45.79.1.9", TokenRequest{Token: first["token"].(string)}); w.Code != 200 {
				t.Errorf("first box's token after second claim: HTTP %d, want 200", w.Code)
			}
		})
	}
}

// Another email at the same address never gets someone else's label, used or not.
func TestClaimReissueIsPerEmail(t *testing.T) {
	a, _, _ := newTestAPI(t)
	claimWithSeededCode(t, a, "one@example.com", "45.79.1.9")
	_, out := claimWithSeededCode(t, a, "two@example.com", "45.79.1.9")
	if out["label"] != "45-79-1-9-b" {
		t.Fatalf("other email got %v, want 45-79-1-9-b", out["label"])
	}
}

// An email at its label cap can still finish setting up a box whose claim it
// already holds: the reissue creates no row, so the cap does not block it.
func TestClaimReissueAtEmailCap(t *testing.T) {
	a, _, _ := newTestAPI(t)
	for i := 0; i < emailLabelCap; i++ {
		if code, out := claimWithSeededCode(t, a, "cap@example.com", fmt.Sprintf("45.79.1.%d", i+1)); code != 200 {
			t.Fatalf("claim %d: HTTP %d %v", i, code, out)
		}
	}
	code, out := claimWithSeededCode(t, a, "cap@example.com", "45.79.1.1")
	if code != 200 || out["label"] != "45-79-1-1" {
		t.Fatalf("retry at cap: HTTP %d %v, want 200 45-79-1-1", code, out)
	}
}

type failADNS struct {
	*fakeDNS
	fail bool
}

func (f *failADNS) EnsureA(ctx context.Context, label, ip string) error {
	if f.fail {
		return context.DeadlineExceeded
	}
	return f.fakeDNS.EnsureA(ctx, label, ip)
}

// A DNS failure on a reissue must not delete the row (the new-label rollback
// does): the label predates this claim, and the next retry reissues it again.
func TestClaimReissueDNSFailureKeepsRow(t *testing.T) {
	a, dns, _ := newTestAPI(t)
	fd := &failADNS{fakeDNS: dns}
	a.DNS = fd
	if code, out := claimWithSeededCode(t, a, "op@example.com", "45.79.1.9"); code != 200 {
		t.Fatalf("first claim: HTTP %d %v", code, out)
	}
	fd.fail = true
	if code, _ := claimWithSeededCode(t, a, "op@example.com", "45.79.1.9"); code != http.StatusInternalServerError {
		t.Fatalf("claim with DNS down: HTTP %d, want 500", code)
	}
	if n := labelRows(t, a); n != 1 {
		t.Fatalf("label rows after failed reissue = %d, want 1", n)
	}
	fd.fail = false
	if code, out := claimWithSeededCode(t, a, "op@example.com", "45.79.1.9"); code != 200 || out["label"] != "45-79-1-9" {
		t.Fatalf("retry after DNS recovers: HTTP %d %v", code, out)
	}
}

// A database created before checked_in_at existed gains the column on open,
// and every label whose box already heartbeated is marked checked in, so the
// upgrade never makes a label in use reissuable.
func TestNewStoreAddsCheckedInAt(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE labels (
  label TEXT PRIMARY KEY, ipv4 TEXT NOT NULL, email TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL, moved_at INTEGER, revoked_at INTEGER);
INSERT INTO labels VALUES ('45-79-1-9','45.79.1.9','op@example.com','h1',100,100,NULL,NULL);
INSERT INTO labels VALUES ('45-79-1-9-b','45.79.1.9','op@example.com','h2',100,5000,NULL,NULL);`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // second open must be a no-op
		if _, err := NewStore(db); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	got := map[string]sql.NullInt64{}
	rows, err := db.Query(`SELECT label, checked_in_at FROM labels`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var l string
		var v sql.NullInt64
		if err := rows.Scan(&l, &v); err != nil {
			t.Fatal(err)
		}
		got[l] = v
	}
	rows.Close()
	if got["45-79-1-9"].Valid {
		t.Errorf("never-heartbeated label marked checked in: %v", got["45-79-1-9"])
	}
	if !got["45-79-1-9-b"].Valid || got["45-79-1-9-b"].Int64 != 5000 {
		t.Errorf("heartbeated label checked_in_at = %v, want 5000", got["45-79-1-9-b"])
	}
}
