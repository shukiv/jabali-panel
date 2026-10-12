package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// healthAgentTimeout is the ceiling for the agent.version round-trip. Short
// by design: this endpoint must not block a health probe if the agent is
// wedged. A dead agent returns 503 within the deadline so upstream
// monitoring can alert fast.
const healthAgentTimeout = 2 * time.Second

// RegisterAgentHealthRoute mounts GET /health/agent. The route calls the
// agent's agent.version command and surfaces the result — or the typed
// AgentError — to the caller. Used as both an alive-ness probe and a
// version sanity check so operators can confirm the agent they see in
// logs matches what systemd is running.
func RegisterAgentHealthRoute(r *gin.Engine, cli agent.AgentInterface) {
	r.GET("/health/agent", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), healthAgentTimeout)
		defer cancel()

		raw, err := cli.Call(ctx, "agent.version", nil)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}

		// Pass the payload through verbatim so callers see whatever the
		// agent reported (version, go_version, uptime_seconds, started_at).
		c.Data(http.StatusOK, "application/json; charset=utf-8",
			[]byte(`{"status":"ok","agent":`+string(raw)+`}`))
	})
}

// translateAgentError maps the typed *agent.AgentError (or any other error)
// onto an HTTP status + JSON body. Unknown errors become 500 — we never
// leak a raw error string to the caller without a code.
//
// An agent failure without a mapped code answers 500, not 502: its detail
// is the agent's message (why UFW, AppArmor or a package refused), and
// Cloudflare replaces an origin's 502 with its own error page (GH #2077).
// A timeout stays 504: its message is lost behind Cloudflare but the status
// isn't, and the AppSec card reads it from the status.
func translateAgentError(err error) (int, gin.H) {
	var ae *agent.AgentError
	if errors.As(err, &ae) {
		status := http.StatusInternalServerError
		switch ae.Code {
		case agent.CodeUnavailable:
			status = http.StatusServiceUnavailable
		case agent.CodeDeadlineExceeded:
			status = http.StatusGatewayTimeout
		case agent.CodePermissionDenied:
			status = http.StatusForbidden
		case agent.CodeNotFound:
			status = http.StatusNotFound
		case agent.CodeInvalidArgument, agent.CodeMalformedEnvelope:
			status = http.StatusBadRequest
		case agent.CodeFailedPrecondition:
			status = http.StatusConflict
		}
		return status, gin.H{
			"status": "error",
			"error":  ae.Code,
			"detail": ae.Message,
		}
	}
	return http.StatusInternalServerError, gin.H{
		"status": "error",
		"error":  "internal",
		"detail": err.Error(),
	}
}
