package domainops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-236 pinned properties:
//  1. Sync delete runs purge → domain.delete → dns.zone.delete, and the
//     tombstone exists only inside the danger window (created before the
//     row delete, cleared after the teardown succeeds).
//  2. A refused row delete (panel-primary) runs NOTHING host-side — the
//     Stalwart purge is destructive and must never precede the refusal —
//     and leaves no tombstone behind.
//  3. A failed teardown keeps the tombstone (with the error recorded);
//     the row is already gone.
//  4. A missing PowerDNS backend (DNS module off) is a permanent
//     condition, not a retryable failure.

// recordedCall is one agent call a test agent saw.
type recordedCall struct {
	method string
	params any
}

// selectiveAgent errors on the configured methods, succeeds otherwise.
type selectiveAgent struct {
	mu          sync.Mutex
	calls       []recordedCall
	errByMethod map[string]error
}

func (a *selectiveAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	a.calls = append(a.calls, recordedCall{method: method, params: params})
	a.mu.Unlock()
	if err, ok := a.errByMethod[method]; ok {
		return nil, err
	}
	return json.RawMessage(`{}`), nil
}

type stubDomainsRepo struct {
	repository.DomainRepository
	deleteErr error
	deleted   []string
}

func (s *stubDomainsRepo) Delete(_ context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = append(s.deleted, id)
	return nil
}

type memTeardownRepo struct {
	mu   sync.Mutex
	rows map[string]*models.DomainTeardown
}

func newMemTeardownRepo() *memTeardownRepo {
	return &memTeardownRepo{rows: map[string]*models.DomainTeardown{}}
}

func (m *memTeardownRepo) Ensure(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[name]; !ok {
		m.rows[name] = &models.DomainTeardown{DomainName: name, CreatedAt: time.Now().UTC()}
	}
	return nil
}

func (m *memTeardownRepo) List(_ context.Context) ([]models.DomainTeardown, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.DomainTeardown
	for _, r := range m.rows {
		out = append(out, *r)
	}
	return out, nil
}

func (m *memTeardownRepo) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, name)
	return nil
}

func (m *memTeardownRepo) MarkAttempt(_ context.Context, name, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[name]; ok {
		r.Attempts++
		r.LastError = lastError
		now := time.Now().UTC()
		r.LastAttemptAt = &now
	}
	return nil
}

func methods(a *selectiveAgent) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.calls))
	for _, c := range a.calls {
		out = append(out, c.method)
	}
	return out
}

func TestDelete_SyncHappyPath(t *testing.T) {
	ag := &selectiveAgent{}
	domains := &stubDomainsRepo{}
	tombs := newMemTeardownRepo()
	d := DeleteDeps{Domains: domains, Teardowns: tombs, Agent: ag}

	pending, err := Delete(context.Background(), d, "dom1", "gone.example", false)
	if err != nil || pending {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	want := []string{"mail.domain.purge_accounts", "domain.delete", "dns.zone.delete"}
	got := methods(ag)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("agent calls %v, want %v (purge must run, and only after the row delete succeeded)", got, want)
	}
	if len(domains.deleted) != 1 || domains.deleted[0] != "dom1" {
		t.Fatalf("row not deleted: %v", domains.deleted)
	}
	if len(tombs.rows) != 0 {
		t.Fatalf("tombstone must be cleared after a successful teardown: %v", tombs.rows)
	}
}

func TestDelete_RefusedRowDeleteRunsNothing(t *testing.T) {
	ag := &selectiveAgent{}
	domains := &stubDomainsRepo{deleteErr: repository.ErrCannotDeletePanelPrimary}
	tombs := newMemTeardownRepo()
	d := DeleteDeps{Domains: domains, Teardowns: tombs, Agent: ag}

	_, err := Delete(context.Background(), d, "dom1", "panel.example", false)
	if !errors.Is(err, repository.ErrCannotDeletePanelPrimary) {
		t.Fatalf("err = %v, want panel-primary refusal passed through", err)
	}
	if len(ag.calls) != 0 {
		t.Fatalf("agent was called %v — a refused delete must run NOTHING host-side (the mail purge is destructive)", methods(ag))
	}
	if len(tombs.rows) != 0 {
		t.Fatalf("tombstone left behind for a live row: %v", tombs.rows)
	}
}

func TestDelete_TeardownFailureKeepsTombstone(t *testing.T) {
	ag := &selectiveAgent{errByMethod: map[string]error{"domain.delete": errors.New("agent down")}}
	domains := &stubDomainsRepo{}
	tombs := newMemTeardownRepo()
	d := DeleteDeps{Domains: domains, Teardowns: tombs, Agent: ag}

	pending, err := Delete(context.Background(), d, "dom1", "gone.example", false)
	if err != nil {
		t.Fatalf("row delete succeeded — err must be nil, got %v", err)
	}
	if !pending {
		t.Fatal("teardown failed — pending must be true")
	}
	row, ok := tombs.rows["gone.example"]
	if !ok {
		t.Fatal("tombstone must survive a failed teardown — it is the only retry handle left")
	}
	if row.Attempts != 1 || !strings.Contains(row.LastError, "agent down") {
		t.Fatalf("attempt not recorded: %+v", row)
	}
}

func TestExecuteTeardown_MissingPDNSIsSuccess(t *testing.T) {
	ag := &selectiveAgent{errByMethod: map[string]error{
		"dns.zone.delete": errors.New("agent error (internal): powerdns backend not available"),
	}}
	if err := ExecuteTeardown(context.Background(), ag, "gone.example"); err != nil {
		t.Fatalf("a box without the DNS module must not fail (and retry forever): %v", err)
	}
}

func TestDelete_AsyncEventuallyClearsTombstone(t *testing.T) {
	ag := &selectiveAgent{}
	domains := &stubDomainsRepo{}
	tombs := newMemTeardownRepo()
	d := DeleteDeps{Domains: domains, Teardowns: tombs, Agent: ag}

	pending, err := Delete(context.Background(), d, "dom1", "gone.example", true)
	if err != nil || !pending {
		t.Fatalf("async returns pending=true immediately; got pending=%v err=%v", pending, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tombs.mu.Lock()
		n := len(tombs.rows)
		tombs.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("async teardown never cleared the tombstone")
}

func TestPurgeDomainMail_CallsAgentWithDomain(t *testing.T) {
	ag := &selectiveAgent{}
	if err := PurgeDomainMail(context.Background(), ag, "example.test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("want 1 agent call, got %d", len(ag.calls))
	}
	if ag.calls[0].method != "mail.domain.purge_accounts" {
		t.Errorf("method = %q, want mail.domain.purge_accounts", ag.calls[0].method)
	}
	p, ok := ag.calls[0].params.(map[string]any)
	if !ok {
		t.Fatalf("params type = %T, want map[string]any", ag.calls[0].params)
	}
	if p["domain"] != "example.test" {
		t.Errorf("domain param = %v, want example.test", p["domain"])
	}
}

func TestPurgeDomainMail_NilAgentNoop(t *testing.T) {
	// A nil agent interface must be a no-op, not a panic.
	if err := PurgeDomainMail(context.Background(), nil, "example.test"); err != nil {
		t.Fatalf("nil agent should be no-op, got %v", err)
	}
}

func TestPurgeDomainMail_EmptyDomainNoop(t *testing.T) {
	ag := &selectiveAgent{}
	if err := PurgeDomainMail(context.Background(), ag, ""); err != nil {
		t.Fatalf("empty domain should be no-op, got %v", err)
	}
	if len(ag.calls) != 0 {
		t.Errorf("empty domain made %d agent calls, want 0", len(ag.calls))
	}
}

func TestPurgeDomainMail_PropagatesAgentError(t *testing.T) {
	want := errors.New("stalwart down")
	ag := &selectiveAgent{errByMethod: map[string]error{"mail.domain.purge_accounts": want}}
	if err := PurgeDomainMail(context.Background(), ag, "example.test"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestDelete_UnwiredDomainStore(t *testing.T) {
	ag := &selectiveAgent{}
	tombs := newMemTeardownRepo()
	_, err := Delete(context.Background(), DeleteDeps{Teardowns: tombs, Agent: ag}, "dom1", "gone.example", false)
	if !errors.Is(err, ErrDeleteDeps) {
		t.Fatalf("err = %v, want ErrDeleteDeps", err)
	}
	if len(ag.calls) != 0 || len(tombs.rows) != 0 {
		t.Fatalf("an unwired delete must touch nothing: calls=%v tombstones=%v", methods(ag), tombs.rows)
	}
}
