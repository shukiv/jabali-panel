package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nginx.panel_csp.apply — renders the panel vhost's Content-Security-Policy
// into /etc/nginx/snippets/jabali-panel-csp.conf, which the panel vhost
// includes at server level.
//
// Server Settings → Support chat can load the "Ask Jabali" widget from
// supportChatOrigin into the admin and client panels. The panel's CSP blocks
// any third-party script, so the snippet allows that one origin, and only
// while the chat is on in at least one panel:
//
//   - script-src: the widget's SDK (/packs/js/sdk.js);
//   - frame-src: the SDK opens the chat in an iframe from that origin;
//   - connect-src: the SDK fetches its notification sound from it.
//
// Its styles are inline (style-src already has 'unsafe-inline') and its
// images load inside the iframe, under the iframe's own policy. With the chat
// off the snippet is exactly the CSP the vhost template carried inline before.
//
// install.sh seeds the off snippet when there is none (PANEL_CSP_OFF heredoc;
// TestPanelCSP_InstallSeedMatchesTheAgent keeps the two byte-identical), and
// the panel re-applies the saved state on every start.

const supportChatOrigin = "https://support.jabali-panel.com"

var panelCSPSnippetPath = "/etc/nginx/snippets/jabali-panel-csp.conf"

type panelCSPApplyParams struct {
	SupportChat *bool `json:"support_chat"`
}

type panelCSPApplyResponse struct {
	SnippetPath string `json:"snippet_path"`
	SupportChat bool   `json:"support_chat"`
	NoChange    bool   `json:"no_change,omitempty"`
}

// BuildPanelCSPSnippet renders the snippet for the given support-chat state.
func BuildPanelCSPSnippet(supportChat bool) string {
	scriptSrc := "'self' 'unsafe-inline'"
	connectSrc := "'self' wss:"
	frameSrc := ""
	if supportChat {
		scriptSrc += " " + supportChatOrigin
		connectSrc += " " + supportChatOrigin
		frameSrc = " frame-src 'self' " + supportChatOrigin + ";"
	}
	csp := "default-src 'self'; script-src " + scriptSrc + "; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https://gravatar.com https://secure.gravatar.com https://www.gravatar.com https://seccdn.libravatar.org; " +
		"font-src 'self' data:; connect-src " + connectSrc + ";" + frameSrc +
		" frame-ancestors 'none'; form-action 'self';"
	return "# Managed by jabali-agent (nginx.panel_csp.apply): Server Settings -> Support chat.\n" +
		"# Included by the panel vhost. Edits here are overwritten.\n" +
		"add_header Content-Security-Policy \"" + csp + "\" always;\n"
}

func panelCSPApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p panelCSPApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if p.SupportChat == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "support_chat is required"}
	}
	on := *p.SupportChat
	snippet := []byte(BuildPanelCSPSnippet(on))

	// JAB-71: serialize read→swap→test→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	if live, err := os.ReadFile(panelCSPSnippetPath); err == nil && bytes.Equal(live, snippet) {
		return &panelCSPApplyResponse{SnippetPath: panelCSPSnippetPath, SupportChat: on, NoChange: true}, nil
	}

	var rollbacks []func()
	restore := func() {
		for i := len(rollbacks) - 1; i >= 0; i-- {
			rollbacks[i]()
		}
	}
	if err := swapFile(panelCSPSnippetPath, snippet, &rollbacks); err != nil {
		restore()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	testCmd := execCommandContext(ctx, "nginx", "-t")
	var testOut bytes.Buffer
	testCmd.Stdout = &testOut
	testCmd.Stderr = &testOut
	if err := testCmd.Run(); err != nil {
		restore()
		return nil, nginxTestFailure("nginx.panel_csp (rolled back)", testOut.String())
	}

	reloadCmd := execCommandContext(ctx, "systemctl", "reload", "nginx")
	var reloadOut bytes.Buffer
	reloadCmd.Stdout = &reloadOut
	reloadCmd.Stderr = &reloadOut
	if err := reloadCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("nginx -t passed but reload failed: %s", reloadOut.String()),
		}
	}

	return &panelCSPApplyResponse{SnippetPath: panelCSPSnippetPath, SupportChat: on}, nil
}

func init() {
	Default.Register("nginx.panel_csp.apply", panelCSPApplyHandler)
}
