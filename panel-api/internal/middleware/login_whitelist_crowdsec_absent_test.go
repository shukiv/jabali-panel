package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

func loginWhitelistRouter(t *testing.T, ag agent.AgentInterface) (*gin.Engine, *miniredis.Miniredis) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", Email: "admin@example.com", IsAdmin: true})
		c.Next()
	})
	r.Use(WhitelistLoginIP(rdb, ag, nil, nil))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return r, mr
}

func adminRequest(r *gin.Engine) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "203.0.113.7:51234"
	req.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "sess-1"})
	r.ServeHTTP(httptest.NewRecorder(), req)
}

func allowlistAdds(m *agent.MockClient) int {
	n := 0
	for _, c := range m.Calls() {
		if c.Command == "security.crowdsec.allowlists.add" {
			n++
		}
	}
	return n
}

func dedupKeyPresent(mr *miniredis.Miniredis) bool {
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, "jabali:login-wl-seen:") {
			return true
		}
	}
	return false
}

func eventually(cond func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// GH #357: on a box installed without the security module there is no cscli,
// and the agent says so. The panel asks once per dedup window, not on every
// admin request.
func TestWhitelistLoginIP_CrowdSecNotInstalledAsksOnce(t *testing.T) {
	ag := agent.NewMockClient().OnError("security.crowdsec.allowlists.add",
		&agent.AgentError{Code: agent.CodeFailedPrecondition, Message: agentwire.MsgCrowdSecNotInstalled})
	r, mr := loginWhitelistRouter(t, ag)

	adminRequest(r)
	if !eventually(func() bool { return allowlistAdds(ag) == 1 }, 2*time.Second) {
		t.Fatalf("allowlist adds after the first request = %d, want 1", allowlistAdds(ag))
	}
	if eventually(func() bool { return !dedupKeyPresent(mr) }, 300*time.Millisecond) {
		t.Fatal("dedup key dropped after the not-installed answer: the next request asks the agent again")
	}
	adminRequest(r)
	adminRequest(r)
	time.Sleep(100 * time.Millisecond)
	if n := allowlistAdds(ag); n != 1 {
		t.Fatalf("allowlist adds after three requests = %d, want 1", n)
	}
}

// Any other failure is retried on the next request, as before.
func TestWhitelistLoginIP_OtherFailuresRetry(t *testing.T) {
	cases := map[string]error{
		"internal":                    &agent.AgentError{Code: agent.CodeInternal, Message: "allowlist add: cscli allowlists add: exit status 1"},
		"another failed_precondition": &agent.AgentError{Code: agent.CodeFailedPrecondition, Message: agentwire.MsgMailServerNotInstalled},
		"internal, same text":         &agent.AgentError{Code: agent.CodeInternal, Message: agentwire.MsgCrowdSecNotInstalled},
		"transport":                   errors.New("dial unix /run/jabali/agent.sock: connect: connection refused"),
	}
	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			ag := agent.NewMockClient().OnError("security.crowdsec.allowlists.add", failure)
			r, mr := loginWhitelistRouter(t, ag)

			adminRequest(r)
			if !eventually(func() bool { return allowlistAdds(ag) == 1 && !dedupKeyPresent(mr) }, 2*time.Second) {
				t.Fatalf("adds %d, dedup key present %v; want 1 add and the key dropped for a retry", allowlistAdds(ag), dedupKeyPresent(mr))
			}
			adminRequest(r)
			if !eventually(func() bool { return allowlistAdds(ag) == 2 }, 2*time.Second) {
				t.Fatalf("allowlist adds after the second request = %d, want 2 (a retry)", allowlistAdds(ag))
			}
		})
	}
}
