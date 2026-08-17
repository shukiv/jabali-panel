package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type systemUnitStopParams struct {
	Unit string `json:"unit"`
}

// allowedStopUnits caps which units can be stopped — same hard-coded list
// as the status side. A compromised panel-api can't stop arbitrary system
// services through this command.
var allowedStopUnits = map[string]bool{
	"jabali-update-oneshot.service": true,
	"jabali-apt-oneshot.service":    true,
}

func systemUnitStopHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p systemUnitStopParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !allowedStopUnits[p.Unit] {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "unit not allowlisted"}
	}
	out, err := execCommandContext(ctx, "systemctl", "stop", p.Unit).CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemctl stop: %v: %s", err, strings.TrimSpace(string(out)))}
	}
	return map[string]bool{"ok": true}, nil
}

func init() {
	Default.Register("system.unit_stop", systemUnitStopHandler)
}
