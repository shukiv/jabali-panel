package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// php.composer_default_set — set or clear a user's chosen Composer channel
// (GH #1332 item 13). The host's /usr/local/bin/composer is a dispatcher that
// reads /etc/jabali-panel/user-composer/<user> and execs the matching phar
// (composer-latest / composer-lts). The choice file is root-owned + world-
// readable (the dispatcher runs as the invoking tenant). An empty/"latest"
// channel clears the file → the default latest Composer.

type composerDefaultParams struct {
	Username string `json:"username"`
	Channel  string `json:"channel"` // "" or "latest" = default; "lts"
}

func composerChoiceRoot() string {
	if r := os.Getenv("JABALI_COMPOSER_CHOICE_ROOT"); r != "" {
		return r
	}
	return "/etc/jabali-panel/user-composer"
}

// validComposerChannel bounds the channel to the set the dispatcher understands.
func validComposerChannel(ch string) bool {
	switch ch {
	case "", "latest", "lts":
		return true
	}
	return false
}

func composerDefaultSetHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p composerDefaultParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if !phpPoolUsernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid username %q", p.Username)}
	}
	if !validComposerChannel(p.Channel) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid composer channel %q", p.Channel)}
	}
	choicePath := filepath.Join(composerChoiceRoot(), p.Username)
	if p.Channel == "" || p.Channel == "latest" {
		if err := os.Remove(choicePath); err != nil && !os.IsNotExist(err) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("clear choice: %v", err)}
		}
		return map[string]any{"username": p.Username, "channel": "latest"}, nil
	}
	if err := os.MkdirAll(composerChoiceRoot(), 0o755); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir choice root: %v", err)}
	}
	// 0644: the dispatcher runs as the tenant and only needs to read it.
	if err := os.WriteFile(choicePath, []byte(p.Channel+"\n"), 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write choice: %v", err)}
	}
	return map[string]any{"username": p.Username, "channel": p.Channel}, nil
}

func init() {
	Default.Register("php.composer_default_set", composerDefaultSetHandler)
}
