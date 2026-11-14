package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// webmail_editor.go — webmail's document editor (GH #2501). Bulwark opens
// office files from its Files view in any WOPI editor (EuroOffice,
// ONLYOFFICE, Collabora) when WOPI_CLIENT_URL names one; empty turns editing
// off. panel-api's reconciler sends the one server-wide URL.
//
// It is written to its own environment file, bulwark-editor.env, which
// jabali-webmail.service loads after bulwark.env and bulwark-branding.env.
// install.sh renders bulwark.env from its template on every update and never
// writes this file, so the editor setting outlives that re-render.

// webmailEditorEnvFile is webmail's document-editor environment file.
var webmailEditorEnvFile = "/etc/jabali-panel/bulwark-editor.env"

// webmailEditorApplyParams is what panel-api sends; its rule is agentwire's,
// which panel-api checks before sending.
type webmailEditorApplyParams struct {
	WopiClientURL string `json:"wopi_client_url"`
}

// validate checks the value again: panel-api checked it when it was saved
// and before sending, and it ends up in webmail's environment.
func (p *webmailEditorApplyParams) validate() error {
	if !agentwire.ValidWebmailEditorURL(p.WopiClientURL) {
		return fmt.Errorf("wopi_client_url: not an https address")
	}
	return nil
}

func webmailEditorApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p webmailEditorApplyParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
		}
	}
	if err := p.validate(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	body := renderWebmailEditorEnv(p.WopiClientURL)
	changed, err := writeFileIfChanged(webmailEditorEnvFile, []byte(body), 0o644)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if changed {
		// try-restart: a webmail that isn't running reads the file when it
		// starts, and isn't started by an editor change.
		if out, sErr := runSystemctl(ctx, "try-restart", "jabali-webmail.service"); sErr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("restart jabali-webmail: %v (%s)", sErr, bytesTrim(out))}
		}
	}
	return okBody{Ok: true}, nil
}

// renderWebmailEditorEnv is the whole bulwark-editor.env. An empty URL still
// writes the line with an empty value, which turns editing off.
func renderWebmailEditorEnv(url string) string {
	return "# Webmail document editor, written by the panel (webmail.editor.apply).\n" +
		"WOPI_CLIENT_URL=" + systemdEnvQuote(url) + "\n"
}

func init() {
	Default.Register("webmail.editor.apply", webmailEditorApplyHandler)
}
