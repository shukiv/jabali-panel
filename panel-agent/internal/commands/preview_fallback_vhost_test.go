package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"text/template"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func renderPreviewFallback(t *testing.T, p previewFallbackApplyParams) string {
	t.Helper()
	tmpl, err := template.New("t").Parse(previewFallbackTemplate)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, p); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return buf.String()
}

func TestPreviewFallbackTemplate_HTTPOnly(t *testing.T) {
	out := renderPreviewFallback(t, previewFallbackApplyParams{Base: "preview.host.tld"})
	if got := strings.Count(out, "server_name *.preview.host.tld;"); got != 1 {
		t.Fatalf("want exactly one catch-all server block without a cert, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "root /var/www/jabali-errors;") {
		t.Error("fallback must serve the branded error root")
	}
	if !strings.Contains(out, "try_files /jabali-err-404.html =404;") {
		t.Error("every path must land on the branded 404")
	}
	if strings.Contains(out, "ssl_certificate") {
		t.Error("no cert -> no ssl directives")
	}
}

func TestPreviewFallbackTemplate_TLS(t *testing.T) {
	out := renderPreviewFallback(t, previewFallbackApplyParams{
		Base:        "preview.host.tld",
		SSLCertPath: "/etc/letsencrypt/live/wildcard.preview.host.tld/fullchain.pem",
		SSLKeyPath:  "/etc/letsencrypt/live/wildcard.preview.host.tld/privkey.pem",
	})
	if got := strings.Count(out, "server_name *.preview.host.tld;"); got != 2 {
		t.Fatalf("with a cert: HTTP-redirect + HTTPS blocks (2 server_name lines), got %d", got)
	}
	if !strings.Contains(out, "return 301 https://$host$request_uri;") {
		t.Error("HTTP block must redirect to https when the cert exists")
	}
}

func TestPreviewFallbackApply_RejectsBadBase(t *testing.T) {
	params, _ := json.Marshal(map[string]any{"base": "not a hostname!"})
	_, err := previewFallbackApplyHandler(context.Background(), json.RawMessage(params))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if ae, ok := err.(*agentwire.AgentError); !ok || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %v", err)
	}
}
