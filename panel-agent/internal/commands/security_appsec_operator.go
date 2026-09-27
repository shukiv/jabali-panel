// security.appsec.operator.apply — GH #1650. Writes the operator-managed CRS
// before-plugin file (appseccfg.CRSPluginOperatorBeforePath) from the full set
// of rows the panel sends, then reloads crowdsec when the file changed.
//
// panel-api runs as `jabali` (JAB-357) and cannot write /var/lib/crowdsec or
// reload crowdsec. Before this verb, a row the panel added did nothing live
// until root next ran `jabali appsec render-config`.
//
// The body is rendered by appseccfg.RenderOperatorBeforeFile, the same function
// `render-config` uses, so the two writers produce byte-identical files for the
// same rows and never undo each other. That is also why an invalid row renders
// as a SKIPPED comment here instead of failing the call: the CLI does the same.
// Only the shape of the request is refused (a missing list, an oversized list).
//
// This verb never touches the built-in file (CRSPluginBeforePath); the agent's
// boot writer owns that one (security_appsec_before.go).
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// Overridable in tests.
var (
	appsecOperatorBeforePath = appseccfg.CRSPluginOperatorBeforePath
	appsecCrowdsecDataDir    = "/var/lib/crowdsec/data"
)

func appsecOperatorApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p appseccfg.OperatorApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := p.Check(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	// Gate on the CRS data tree, like the boot writer: absent on hosts without
	// crowdsec, and creating it here would plant a config for nothing to load.
	if _, err := os.Stat(appsecCrowdsecDataDir); err != nil {
		return appseccfg.OperatorApplyResult{Skipped: "crowdsec is not installed"}, nil
	}

	changed, err := writeOperatorBeforeFile(appsecOperatorBeforePath,
		appseccfg.RenderOperatorBeforeFile(p.Exclusions, p.HostModes))
	if err != nil {
		return nil, csInternal("write operator before-plugin", err)
	}
	res := appseccfg.OperatorApplyResult{Changed: changed}
	if !changed {
		return res, nil
	}

	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if out, err := execCommandContext(rctx, "systemctl", "reload", "crowdsec").CombinedOutput(); err != nil {
		if out2, err2 := execCommandContext(rctx, "systemctl", "restart", "crowdsec").CombinedOutput(); err2 != nil {
			// The file is written, so the next reload loads it. Report the
			// failure so the panel can log it rather than assume it is live.
			return nil, csInternal("crowdsec reload and restart failed",
				fmt.Errorf("reload: %v (%s); restart: %v (%s)",
					err, strings.TrimSpace(string(out)), err2, strings.TrimSpace(string(out2))))
		}
	}
	res.Reloaded = true
	return res, nil
}

// writeOperatorBeforeFile makes path hold body, or removes it when body is ""
// (no exclusions and no host modes, so a since-removed entry cannot linger
// live). Returns whether anything on disk changed.
func writeOperatorBeforeFile(path, body string) (bool, error) {
	if body == "" {
		err := os.Remove(path)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, fs.ErrNotExist):
			return false, nil
		default:
			return false, err
		}
	}
	if existing, err := os.ReadFile(path); err == nil && string(existing) == body {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := writeFileAtomically(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func init() {
	Default.Register(appseccfg.OperatorApplyVerb, appsecOperatorApplyHandler)
}
