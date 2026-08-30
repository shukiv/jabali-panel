package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Embedded-interface mocks: only the method the handler calls is implemented;
// everything else would panic if touched (it isn't).
type mdDomainRepo struct {
	repository.DomainRepository
	ds []models.Domain
}

func (m mdDomainRepo) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	out := []models.Domain{}
	for _, d := range m.ds {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, int64(len(out)), nil
}

type mdMailboxRepo struct {
	repository.MailboxRepository
	mbs []repository.MailboxWithDomain
}

func (m mdMailboxRepo) ListByOwnerWithDomain(_ context.Context, userID string) ([]repository.MailboxWithDomain, error) {
	out := []repository.MailboxWithDomain{}
	for _, r := range m.mbs {
		if r.OwnerUserID == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

type mdStatsRepo struct {
	repository.MailStatsRepository
	byUser map[string][]repository.DomainStatSample
}

func (m mdStatsRepo) DomainSeriesForUser(_ context.Context, _ time.Time, userID string) ([]repository.DomainStatSample, error) {
	return m.byUser[userID], nil
}

func mb(domainID, owner string, system bool, bytes uint64) repository.MailboxWithDomain {
	var r repository.MailboxWithDomain
	r.DomainID = domainID
	r.System = system
	r.LastUsageBytes = bytes
	r.OwnerUserID = owner
	return r
}

func setupMailDomainsRouter(t *testing.T, userID string, cfg MeMailDomainsConfig) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID})
		c.Next()
	})
	RegisterMeMailDomainsRoutes(r.Group("/api/v1"), cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/mail-domains", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMailDomains_AggregatesAndScopes(t *testing.T) {
	// u1 owns a.test (mail on) + b.test (mail OFF). u2 owns c.test (mail on).
	cfg := MeMailDomainsConfig{
		Domains: mdDomainRepo{ds: []models.Domain{
			{ID: "da", UserID: "u1", Name: "a.test", EmailEnabled: true},
			{ID: "db", UserID: "u1", Name: "b.test", EmailEnabled: false},
			{ID: "dc", UserID: "u2", Name: "c.test", EmailEnabled: true},
		}},
		Mailboxes: mdMailboxRepo{mbs: []repository.MailboxWithDomain{
			mb("da", "u1", false, 1000), // real
			mb("da", "u1", false, 500),  // real
			mb("da", "u1", true, 999),   // system relay → excluded
			mb("db", "u1", false, 42),   // b.test mail off → domain filtered out
			mb("dc", "u2", false, 7),    // u2's — must not appear for u1
		}},
		MailStats: mdStatsRepo{byUser: map[string][]repository.DomainStatSample{
			"u1": {
				{Domain: "a.test", Metric: "sent", Value: 5},
				{Domain: "a.test", Metric: "sent", Value: 2},
				{Domain: "a.test", Metric: "received", Value: 3},
				{Domain: "a.test", Metric: "delivered", Value: 99}, // not folded in
			},
		}},
	}

	w := setupMailDomainsRouter(t, "u1", cfg)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data  []mailDomainRow `json:"data"`
		Total int             `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 || resp.Total != 1 {
		t.Fatalf("want exactly a.test (b.test mail-off filtered), got %+v", resp.Data)
	}
	row := resp.Data[0]
	if row.Name != "a.test" {
		t.Fatalf("want a.test, got %q", row.Name)
	}
	if row.MailboxCount != 2 {
		t.Fatalf("mailbox_count = %d, want 2 (system relay excluded)", row.MailboxCount)
	}
	if row.MailBytes != 1500 {
		t.Fatalf("mail_bytes = %d, want 1500 (system relay's 999 excluded)", row.MailBytes)
	}
	if row.Sent30d != 7 {
		t.Fatalf("sent_30d = %d, want 7 (5+2)", row.Sent30d)
	}
	if row.Received30d != 3 {
		t.Fatalf("received_30d = %d, want 3", row.Received30d)
	}
}

func TestMailDomains_QueueCounts(t *testing.T) {
	// msg1: a.test sender. msg2: A.test sender (bracketed, mixed case) → a.test,
	// and b.test recipient. msg3: null-sender bounce to two b.test recipients →
	// b.test once (deduped per domain per message). msg4: unrelated → ignored.
	cfg := MeMailDomainsConfig{
		Domains: mdDomainRepo{ds: []models.Domain{
			{ID: "da", UserID: "u1", Name: "a.test", EmailEnabled: true},
			{ID: "db", UserID: "u1", Name: "b.test", EmailEnabled: true},
		}},
		Mailboxes: mdMailboxRepo{},
		MailStats: mdStatsRepo{},
		Agent: &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
			if cmd == "mail.queue.list" {
				return json.RawMessage(`{"data":[
					{"id":"1","from":"x@a.test","recipients":["y@external.com"]},
					{"id":"2","from":"<z@A.test>","recipients":["w@b.test"]},
					{"id":"3","from":"<>","recipients":["p@b.test","q@b.test"]},
					{"id":"4","from":"n@other.com","recipients":["m@other.com"]}
				]}`), nil
			}
			return json.RawMessage(`{}`), nil
		}},
	}
	w := setupMailDomainsRouter(t, "u1", cfg)
	var resp struct {
		Data []mailDomainRow `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]*int64{}
	for _, r := range resp.Data {
		got[r.Name] = r.Queue
	}
	if got["a.test"] == nil || *got["a.test"] != 2 {
		t.Fatalf("a.test queue = %v, want 2", got["a.test"])
	}
	if got["b.test"] == nil || *got["b.test"] != 2 {
		t.Fatalf("b.test queue = %v, want 2", got["b.test"])
	}
}

func TestMailDomains_QueueUnknownOnAgentError(t *testing.T) {
	// Agent error → queue field OMITTED (unknown), never a misleading 0.
	cfg := MeMailDomainsConfig{
		Domains:   mdDomainRepo{ds: []models.Domain{{ID: "da", UserID: "u1", Name: "a.test", EmailEnabled: true}}},
		Mailboxes: mdMailboxRepo{},
		MailStats: mdStatsRepo{},
		Agent: &mockAgent{callFn: func(context.Context, string, any) (json.RawMessage, error) {
			return nil, context.DeadlineExceeded
		}},
	}
	w := setupMailDomainsRouter(t, "u1", cfg)
	var raw struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Data) != 1 {
		t.Fatalf("want 1 row, got %d", len(raw.Data))
	}
	if _, present := raw.Data[0]["queue"]; present {
		t.Fatalf("queue must be omitted on agent error, got %s", w.Body.String())
	}
}

func TestMailDomains_CrossTenantIsolation(t *testing.T) {
	// The same config viewed as u2 shows only u2's domain, never u1's.
	cfg := MeMailDomainsConfig{
		Domains: mdDomainRepo{ds: []models.Domain{
			{ID: "da", UserID: "u1", Name: "a.test", EmailEnabled: true},
			{ID: "dc", UserID: "u2", Name: "c.test", EmailEnabled: true},
		}},
		Mailboxes: mdMailboxRepo{mbs: []repository.MailboxWithDomain{
			mb("da", "u1", false, 1000),
			mb("dc", "u2", false, 7),
		}},
		MailStats: mdStatsRepo{byUser: map[string][]repository.DomainStatSample{}},
	}
	w := setupMailDomainsRouter(t, "u2", cfg)
	var resp struct {
		Data []mailDomainRow `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Name != "c.test" {
		t.Fatalf("u2 must see only c.test, got %+v", resp.Data)
	}
}
