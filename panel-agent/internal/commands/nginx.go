package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// logNginxTestFailure records the raw `nginx -t` output server-side only
// (JAB-68). The output carries host filesystem paths (/etc/nginx/sites-
// available/…), module info, and config internals that must never reach a
// tenant through the API error message. `op` is a fixed operation label.
func logNginxTestFailure(op, rawOutput string) {
	slog.Error("nginx -t failed", "op", op, "output", strings.TrimSpace(rawOutput))
}

// nginxTestFailure logs the raw output (server-side) and returns a leak-free
// AgentError carrying only the operation label — never the raw nginx output.
func nginxTestFailure(op, rawOutput string) *agentwire.AgentError {
	logNginxTestFailure(op, rawOutput)
	return &agentwire.AgentError{
		Code:    agentwire.CodeInternal,
		Message: "nginx configuration test failed: " + op,
	}
}

// nginxTestResponse is the output shape for nginx.test.
type nginxTestResponse struct {
	Valid bool `json:"valid"`
}

// nginxReloadResponse is the output shape for nginx.reload.
type nginxReloadResponse struct {
	Reloaded bool `json:"reloaded"`
}

func nginxTestHandler(ctx context.Context, params json.RawMessage) (any, error) {
	// Run nginx -t to test configuration
	testCmd := execCommandContext(ctx, "nginx", "-t")
	var combinedOutput bytes.Buffer
	testCmd.Stdout = &combinedOutput
	testCmd.Stderr = &combinedOutput

	if err := testCmd.Run(); err != nil {
		return nil, nginxTestFailure("nginx.test", combinedOutput.String())
	}

	return nginxTestResponse{Valid: true}, nil
}

func nginxReloadHandler(ctx context.Context, params json.RawMessage) (any, error) {
	// First test nginx configuration
	_, err := nginxTestHandler(ctx, params)
	if err != nil {
		return nil, err
	}

	// Run systemctl reload nginx
	reloadCmd := execCommandContext(ctx, "systemctl", "reload", "nginx")
	var combinedOutput bytes.Buffer
	reloadCmd.Stdout = &combinedOutput
	reloadCmd.Stderr = &combinedOutput

	if err := reloadCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl reload nginx failed: %s", combinedOutput.String()),
		}
	}

	return nginxReloadResponse{Reloaded: true}, nil
}

func init() {
	Default.Register("nginx.test", nginxTestHandler)
	Default.Register("nginx.reload", nginxReloadHandler)
}
