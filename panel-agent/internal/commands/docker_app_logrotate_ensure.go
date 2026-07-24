package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// docker_app.logrotate_ensure (JAB-121 phase 2) (re)writes the host logrotate
// snippet for one already-installed docker app from catalog-supplied specs.
// The reconciler dispatches it once per app per panel-api lifetime so apps
// installed BEFORE the retention feature — and any whose snippet drifted — get
// bounded logs without a reinstall. Empty specs are a no-op (never removes; the
// delete handler owns removal).
func dockerAppLogrotateEnsureHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Slug      string               `json:"slug"`
		LogRotate []dockerAppLogRotate `json:"log_rotate"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	if err := writeDockerAppLogrotate(p.Slug, dir, p.LogRotate); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	return map[string]any{"ok": true}, nil
}

func init() {
	Default.Register("docker_app.logrotate_ensure", dockerAppLogrotateEnsureHandler)
}
