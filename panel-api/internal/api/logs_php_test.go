package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// phpLogAgent answers php.error_log.read from a script, one reply per call
// (the last one repeats), and records what each call asked for.
type phpLogAgent struct {
	mu      sync.Mutex
	replies []phpErrorLogRead
	err     error
	calls   []map[string]any
}

func (a *phpLogAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if command != "php.error_log.read" {
		return nil, errors.New("unexpected command " + command)
	}
	b, _ := json.Marshal(params)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	a.calls = append(a.calls, m)
	if a.err != nil {
		return nil, a.err
	}
	i := len(a.calls) - 1
	if i >= len(a.replies) {
		i = len(a.replies) - 1
	}
	return json.Marshal(a.replies[i])
}

func (a *phpLogAgent) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

type phpLogUsers struct {
	repository.UserRepository
	username *string
}

func (u phpLogUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	return &models.User{ID: id, Username: u.username}, nil
}

func phpTailRouter(userID string, isAdmin bool, cfg LogHandlerConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
		c.Next()
	})
	RegisterLogRoutes(v1, cfg)
	return r
}

func TestLogTail_PHP(t *testing.T) {
	owner := "01OWNER0000000000000000000"
	dom := &models.Domain{ID: "01DOMAIN000000000000000000", UserID: owner, Name: "example.com"}
	get := func(r *gin.Engine) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/logs/tail?domain_id=x&log_type=php&lines=20", nil))
		return w
	}

	t.Run("owner reads through the agent", func(t *testing.T) {
		ag := &phpLogAgent{replies: []phpErrorLogRead{{Lines: []string{"PHP Warning: a", "PHP Warning: b"}, Offset: 30, Ino: 7}}}
		w := get(phpTailRouter(owner, false, LogHandlerConfig{Domains: fakeDomains{d: dom}, Users: phpLogUsers{username: strp("alice")}, Agent: ag}))
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var body struct{ Lines []string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if strings.Join(body.Lines, "|") != "PHP Warning: a|PHP Warning: b" {
			t.Errorf("lines = %q", body.Lines)
		}
		c := ag.calls[0]
		if c["username"] != "alice" || c["domain"] != "example.com" || c["offset"] != float64(-1) || c["lines"] != float64(20) {
			t.Errorf("agent asked for %v", c)
		}
	})

	t.Run("another tenant's domain is 404 and never reaches the agent", func(t *testing.T) {
		ag := &phpLogAgent{replies: []phpErrorLogRead{{}}}
		w := get(phpTailRouter("01OTHER0000000000000000000", false, LogHandlerConfig{Domains: fakeDomains{d: dom}, Users: phpLogUsers{username: strp("alice")}, Agent: ag}))
		if w.Code != http.StatusNotFound || ag.callCount() != 0 {
			t.Fatalf("status %d, agent calls %d", w.Code, ag.callCount())
		}
	})

	t.Run("a domain without a hosting account is an empty log", func(t *testing.T) {
		ag := &phpLogAgent{replies: []phpErrorLogRead{{}}}
		w := get(phpTailRouter(owner, true, LogHandlerConfig{Domains: fakeDomains{d: dom}, Users: phpLogUsers{}, Agent: ag}))
		if w.Code != http.StatusOK || ag.callCount() != 0 {
			t.Fatalf("status %d, agent calls %d", w.Code, ag.callCount())
		}
	})

	t.Run("no agent is 503", func(t *testing.T) {
		w := get(phpTailRouter(owner, false, LogHandlerConfig{Domains: fakeDomains{d: dom}, Users: phpLogUsers{username: strp("alice")}}))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status %d", w.Code)
		}
	})

	t.Run("an agent error is 502", func(t *testing.T) {
		ag := &phpLogAgent{err: errors.New("the log is not owned by the account")}
		w := get(phpTailRouter(owner, false, LogHandlerConfig{Domains: fakeDomains{d: dom}, Users: phpLogUsers{username: strp("alice")}, Agent: ag}))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("status %d", w.Code)
		}
	})
}

// PHP error logs are per domain: even an admin cannot open an aggregate one.
func TestCreateAccess_PHPNeedsDomain(t *testing.T) {
	r := phpTailRouter("01ADMIN0000000000000000000", true, LogHandlerConfig{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/logs/access", strings.NewReader(`{"log_type":"php"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

// The live view sends the tail, then only what the next read returns, and
// carries the offset and inode of each read into the next.
func TestStreamPHPErrorLog_Follows(t *testing.T) {
	prev := phpErrorLogPollInterval
	phpErrorLogPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { phpErrorLogPollInterval = prev })

	domainID := "01DOMAIN000000000000000000"
	dom := &models.Domain{ID: domainID, UserID: "01OWNER0000000000000000000", Name: "example.com"}
	ag := &phpLogAgent{replies: []phpErrorLogRead{
		{Lines: []string{"old 1", "old 2"}, Offset: 12, Ino: 9},
		{Lines: []string{"new 1"}, Offset: 18, Ino: 9},
		{Lines: []string{}, Offset: 18, Ino: 9},
	}}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterLogStreamRoutes(r, LogStreamHandlerConfig{
		LogAccessStreams: &fakeLogStreamRepo{stream: &models.LogAccessStream{
			LogType: "php", DomainID: &domainID, ExpiresAt: time.Now().Add(time.Minute),
		}},
		Domains: fakeDomains{d: dom},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agent:   ag,
		Users:   phpLogUsers{username: strp("alice")},
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/logs/stream/"+validHexStreamKey, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	var got []string
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for len(got) < 3 {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read after %q: %v", got, err)
		}
		got = append(got, strings.TrimSuffix(string(msg), "\n"))
	}
	if strings.Join(got, "|") != "old 1|old 2|new 1" {
		t.Fatalf("messages = %q", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for ag.callCount() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ag.mu.Lock()
	calls := append([]map[string]any(nil), ag.calls...)
	ag.mu.Unlock()
	if len(calls) < 3 {
		t.Fatalf("only %d agent reads", len(calls))
	}
	if calls[0]["offset"] != float64(-1) {
		t.Errorf("first read must ask for the tail: %v", calls[0])
	}
	if calls[1]["offset"] != float64(12) || calls[1]["ino"] != float64(9) {
		t.Errorf("second read must continue at the first's offset and inode: %v", calls[1])
	}
	if calls[2]["offset"] != float64(18) {
		t.Errorf("third read must continue at the second's offset: %v", calls[2])
	}
}
