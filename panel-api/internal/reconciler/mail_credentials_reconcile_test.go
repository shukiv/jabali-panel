package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The mail server's app passwords and API keys follow the panel: removed
// from a mailbox that may not sign in, and when made before its password
// last changed.

var credChanged = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

type credFakeRegistry struct {
	// account id → address, credentials by position as "<type>@<createdAt>"
	address map[string]string
	creds   map[string][]string
	queries int
	updates []string
	err     error
}

func (f *credFakeRegistry) account(id string) json.RawMessage {
	m := map[string]any{}
	for i, c := range f.creds[id] {
		typ, created, _ := strings.Cut(c, "@")
		m[strconv.Itoa(i)] = map[string]any{"@type": typ, "createdAt": created}
	}
	b, _ := json.Marshal(map[string]any{"id": id, "emailAddress": f.address[id], "credentials": m})
	return b
}

func (f *credFakeRegistry) Query(_ context.Context, _ string, _ map[string]any, _ []string) ([]json.RawMessage, error) {
	f.queries++
	if f.err != nil {
		return nil, f.err
	}
	var out []json.RawMessage
	for id := range f.address {
		out = append(out, f.account(id))
	}
	return out, nil
}

func (f *credFakeRegistry) Get(_ context.Context, _ string, id string) (json.RawMessage, error) {
	return f.account(id), nil
}

func (f *credFakeRegistry) Update(_ context.Context, _ string, id string, payload any) error {
	for k := range payload.(map[string]any) {
		n, _ := strconv.Atoi(strings.TrimPrefix(k, "credentials/"))
		f.creds[id] = append(f.creds[id][:n:n], f.creds[id][n+1:]...)
		f.updates = append(f.updates, id+":"+k)
	}
	return nil
}

type credFakeLogins struct {
	logins map[string]time.Time
	err    error
}

func (f *credFakeLogins) ListMailLogins(context.Context) (map[string]time.Time, error) {
	return f.logins, f.err
}

func mailCredFixture(reg *credFakeRegistry, logins *credFakeLogins) (*Reconciler, *fakeAgent) {
	ag := &fakeAgent{}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithMailCredentials(reg, logins)
	return r, ag
}

func flushCalls(ag *fakeAgent) int {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	n := 0
	for _, c := range ag.calls {
		if c.method == "mail.auth_cache.flush" {
			n++
		}
	}
	return n
}

func aliceRegistry() *credFakeRegistry {
	before := credChanged.Add(-time.Hour).Format(time.RFC3339)
	after := credChanged.Add(time.Hour).Format(time.RFC3339)
	return &credFakeRegistry{
		address: map[string]string{"n2": "alice@example.com"},
		creds:   map[string][]string{"n2": {"Password@", "AppPassword@" + before, "AppPassword@" + after}},
	}
}

// An app password made before the password changed comes off, and the mail
// server's login cache is flushed after it.
func TestReconcileMailCredentials_RemovesAndFlushes(t *testing.T) {
	reg := aliceRegistry()
	r, ag := mailCredFixture(reg, &credFakeLogins{logins: map[string]time.Time{"alice@example.com": credChanged}})
	r.reconcileMailCredentials(context.Background())
	if len(reg.creds["n2"]) != 2 || reg.creds["n2"][1] != "AppPassword@"+credChanged.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("left %v", reg.creds["n2"])
	}
	if n := flushCalls(ag); n != 1 {
		t.Errorf("flushes = %d, want 1", n)
	}
}

// Nothing removed: no flush.
func TestReconcileMailCredentials_NothingRemovedNoFlush(t *testing.T) {
	reg := &credFakeRegistry{address: map[string]string{"n2": "alice@example.com"}, creds: map[string][]string{"n2": {"Password@"}}}
	r, ag := mailCredFixture(reg, &credFakeLogins{logins: map[string]time.Time{"alice@example.com": credChanged}})
	r.reconcileMailCredentials(context.Background())
	if n := flushCalls(ag); n != 0 || reg.queries != 1 {
		t.Errorf("flushes = %d, queries = %d", n, reg.queries)
	}
}

// The pass runs on the first tick; a steady tick inside the interval reads
// nothing from the mail server; a changed list of mailboxes (a password
// changed, a mailbox disabled) runs it again.
func TestReconcileMailCredentials_RunsOnceThenOnChange(t *testing.T) {
	reg := aliceRegistry()
	logins := &credFakeLogins{logins: map[string]time.Time{"alice@example.com": credChanged}}
	r, _ := mailCredFixture(reg, logins)
	r.reconcileMailCredentials(context.Background())
	r.reconcileMailCredentials(context.Background())
	if reg.queries != 1 {
		t.Fatalf("queries = %d, want 1", reg.queries)
	}
	logins.logins = map[string]time.Time{"alice@example.com": credChanged.Add(2 * time.Hour)}
	r.reconcileMailCredentials(context.Background())
	if reg.queries != 2 || len(reg.creds["n2"]) != 1 {
		t.Fatalf("queries = %d, left %v", reg.queries, reg.creds["n2"])
	}
}

// A failed read of the mailboxes removes nothing: an empty list would strip
// every mailbox.
func TestReconcileMailCredentials_ListErrorRemovesNothing(t *testing.T) {
	reg := aliceRegistry()
	r, _ := mailCredFixture(reg, &credFakeLogins{logins: map[string]time.Time{}, err: errors.New("db down")})
	r.reconcileMailCredentials(context.Background())
	if reg.queries != 0 || len(reg.updates) != 0 {
		t.Errorf("queries = %d, updates = %v", reg.queries, reg.updates)
	}
}

// A mail server that can't be reached is tried again on the next tick.
func TestReconcileMailCredentials_RegistryErrorIsRetried(t *testing.T) {
	reg := aliceRegistry()
	reg.err = errors.New("mail server down")
	r, _ := mailCredFixture(reg, &credFakeLogins{logins: map[string]time.Time{"alice@example.com": credChanged}})
	r.reconcileMailCredentials(context.Background())
	reg.err = nil
	r.reconcileMailCredentials(context.Background())
	if reg.queries != 2 || len(reg.creds["n2"]) != 2 {
		t.Errorf("queries = %d, left %v", reg.queries, reg.creds["n2"])
	}
}

// Unwired, the pass does nothing.
func TestReconcileMailCredentials_Unwired(t *testing.T) {
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, &fakeAgent{}, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second})
	r.reconcileMailCredentials(context.Background())
}

// ReconcileAll runs the pass.
func TestReconcileAll_RunsTheMailCredentialsPass(t *testing.T) {
	reg := aliceRegistry()
	r, _ := mailCredFixture(reg, &credFakeLogins{logins: map[string]time.Time{"alice@example.com": credChanged}})
	r.ReconcileAll(context.Background())
	if reg.queries != 1 {
		t.Errorf("queries = %d, want 1", reg.queries)
	}
}

// A server without the mail module has no mail server to sweep: the pass
// skips instead of failing every tick. Only a positive "mail off" reading
// skips it; mail on, or settings that can't be read, keep it.
func TestReconcileMailCredentials_SkipsWhenMailIsOff(t *testing.T) {
	reg := aliceRegistry()
	r, _ := mailCredFixture(reg, &credFakeLogins{logins: map[string]time.Time{"alice@example.com": credChanged}})
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{MailEnabled: false}}
	r.reconcileMailCredentials(context.Background())
	if reg.queries != 0 {
		t.Fatalf("queries = %d with the mail module off, want 0", reg.queries)
	}

	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{MailEnabled: true}}
	r.reconcileMailCredentials(context.Background())
	if reg.queries != 1 {
		t.Fatalf("queries = %d with the mail module on, want 1", reg.queries)
	}
}

// An app password made within a minute after a password change may have been
// made with the old password, which the mail server's login cache takes
// until the flush reaches it: the cutoff lies in the future then. The pass
// runs again once it has passed, and removes what was made before it.
func TestReconcileMailCredentials_RunsAgainWhenACutoffPasses(t *testing.T) {
	now := credChanged
	cutoff := credChanged.Add(time.Minute)
	reg := &credFakeRegistry{address: map[string]string{"n2": "alice@example.com"}, creds: map[string][]string{"n2": {"Password@"}}}
	r, _ := mailCredFixture(reg, &credFakeLogins{logins: map[string]time.Time{"alice@example.com": cutoff}})
	r.mailCredNow = func() time.Time { return now }

	r.reconcileMailCredentials(context.Background())
	// Made 30s after the change, with the old password still cached.
	reg.creds["n2"] = append(reg.creds["n2"], "AppPassword@"+credChanged.Add(30*time.Second).Format(time.RFC3339))
	now = credChanged.Add(40 * time.Second)
	r.reconcileMailCredentials(context.Background())
	if reg.queries != 1 {
		t.Fatalf("queries = %d before the cutoff passed, want 1", reg.queries)
	}
	now = cutoff.Add(time.Second)
	r.reconcileMailCredentials(context.Background())
	if reg.queries != 2 || len(reg.creds["n2"]) != 1 {
		t.Fatalf("queries = %d, left %v after the cutoff passed", reg.queries, reg.creds["n2"])
	}
}
