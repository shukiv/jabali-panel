package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nginx.webmail_redirect.apply keeps the /webmail redirects in
// /etc/nginx/sites-available/jabali-default.conf on the panel mail hostname
// (JAB-390).
//
// install.sh renders those four redirects (two in the :443 default block, two
// in the GH#135 landing vhost) from `jabali settings mail-hostname`, but only
// when it runs. After a switchover the redirects would keep pointing at the
// old name until the next `jabali update`. The reconciler calls this verb with
// the effective mail hostname, so they follow at once.
//
// Only the target host of a `location = /webmail` or `location = /webmail/`
// redirect changes. A file that is already current is left alone, with no
// reload. A box with no default vhost, or with no /webmail redirects in it,
// has nothing to keep current and gets a no-op, not an error. The whole
// nginx config is tested after the write, and the file is restored if the
// test fails.
type nginxWebmailRedirectParams struct {
	MailHostname string `json:"mail_hostname"`
}

type nginxWebmailRedirectResponse struct {
	Rewritten    bool   `json:"rewritten"`
	Reason       string `json:"reason,omitempty"`
	Replacements int    `json:"replacements,omitempty"`
}

// webmailRedirectVhostPath is the default vhost install.sh writes.
// Overridable in tests.
var webmailRedirectVhostPath = "/etc/nginx/sites-available/jabali-default.conf"

// webmailRedirectRE matches one /webmail redirect, in either the multi-line
// or the one-line form install.sh writes, and captures its target host.
var webmailRedirectRE = regexp.MustCompile(`(location\s*=\s*/webmail/?\s*\{\s*return\s+301\s+https://)([a-zA-Z0-9][a-zA-Z0-9._-]*)(/;)`)

func init() {
	Default.Register("nginx.webmail_redirect.apply", nginxWebmailRedirectApplyHandler)
}

func nginxWebmailRedirectApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p nginxWebmailRedirectParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	target, err := normalizeVhostServerName("mail_hostname", p.MailHostname)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(webmailRedirectVhostPath)
	if errors.Is(err, os.ErrNotExist) {
		return nginxWebmailRedirectResponse{Reason: "no default vhost"}, nil
	}
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read %s: %v", webmailRedirectVhostPath, err)}
	}

	matches := webmailRedirectRE.FindAllSubmatch(data, -1)
	if len(matches) == 0 {
		return nginxWebmailRedirectResponse{Reason: "no webmail redirects"}, nil
	}
	stale := 0
	for _, m := range matches {
		if !strings.EqualFold(string(m[2]), target) {
			stale++
		}
	}
	if stale == 0 {
		return nginxWebmailRedirectResponse{Reason: "already current"}, nil
	}

	rewritten := webmailRedirectRE.ReplaceAll(data, []byte("${1}"+target+"${3}"))
	for _, m := range webmailRedirectRE.FindAllSubmatch(rewritten, -1) {
		if string(m[2]) != target {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "refusing to write: the webmail redirects did not converge"}
		}
	}

	if err := writeFilePreservingMeta(webmailRedirectVhostPath, rewritten); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write %s: %v", webmailRedirectVhostPath, err)}
	}
	// A broken jabali-default.conf would take every :443 vhost down, so the
	// whole config is tested and this file restored on failure.
	if out, terr := execCommandContext(ctx, "nginx", "-t").CombinedOutput(); terr != nil {
		_ = writeFilePreservingMeta(webmailRedirectVhostPath, data)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("nginx -t failed after the webmail redirect rewrite (rolled back): %s", strings.TrimSpace(string(out))),
		}
	}
	if out, rerr := execCommandContext(ctx, "systemctl", "reload", "nginx").CombinedOutput(); rerr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl reload nginx failed: %s", strings.TrimSpace(string(out))),
		}
	}
	return nginxWebmailRedirectResponse{Rewritten: true, Replacements: len(matches)}, nil
}
