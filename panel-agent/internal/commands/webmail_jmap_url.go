package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// webmail_jmap_url.go — webmail.jmap_url.apply (JAB-390): point Bulwark's
// JMAP_SERVER_URL at the panel mail hostname and restart jabali-webmail.
//
// The shared-mail-hostname switchover calls it when the applied mail
// hostname changes. The URL must equal the name every webmail vhost's
// sub_filter rewrites (webmail.vhost_apply panel_mail_hostname), or tenant
// webmail breaks cross-origin. install.sh renders the same value from the
// DB (`jabali settings mail-hostname --applied`), so a later `jabali update`
// keeps it.
//
// Only the JMAP_SERVER_URL line changes, in place; every other line —
// including the SSO secrets and branding keys other writers append — is
// kept byte-for-byte. Idempotent: an unchanged file is not rewritten and
// the service is not restarted.

// webmailJMAPEnvFile is bulwark.env; a var so tests can point it elsewhere.
var webmailJMAPEnvFile = webmailEnvFile

type webmailJMAPURLApplyParams struct {
	// MailHostname is the panel mail hostname (bare FQDN).
	MailHostname string `json:"mail_hostname"`
}

type webmailJMAPURLApplyResponse struct {
	Ok      bool `json:"ok"`
	Changed bool `json:"changed"`
}

const jmapServerURLKey = "JMAP_SERVER_URL="

func webmailJMAPURLApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p webmailJMAPURLApplyParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
		}
	}
	// The value lands in an EnvironmentFile line: only a plain DNS name.
	host, err := normalizeVhostServerName("mail_hostname", p.MailHostname)
	if err != nil {
		return nil, err
	}

	orig, err := os.ReadFile(webmailJMAPEnvFile)
	if os.IsNotExist(err) {
		// Bulwark not installed on this host: nothing to point.
		return webmailJMAPURLApplyResponse{Ok: true}, nil
	}
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read bulwark.env: %v", err)}
	}

	want := jmapServerURLKey + "https://" + host
	lines := strings.Split(string(orig), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, jmapServerURLKey) {
			lines[i] = want
			found = true
		}
	}
	body := strings.Join(lines, "\n")
	if !found {
		body = strings.TrimRight(body, "\n") + "\n" + want + "\n"
	}
	if body == string(orig) {
		return webmailJMAPURLApplyResponse{Ok: true}, nil
	}

	if err := replaceFileKeepingOwner(webmailJMAPEnvFile, []byte(body)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	// Bulwark reads its env only at start.
	if out, err := runSystemctl(ctx, "restart", "jabali-webmail.service"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("restart jabali-webmail: %v (%s)", err, bytesTrim(out))}
	}
	return webmailJMAPURLApplyResponse{Ok: true, Changed: true}, nil
}

// replaceFileKeepingOwner atomically replaces path with body, keeping the
// existing file's mode and owner.
func replaceFileKeepingOwner(path string, body []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, fi.Mode().Perm()); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	// WriteFile's mode is filtered by the umask; set it exactly.
	if err := os.Chmod(tmp, fi.Mode().Perm()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if err := os.Chown(tmp, int(st.Uid), int(st.Gid)); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("chown %s: %w", tmp, err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

func init() {
	Default.Register("webmail.jmap_url.apply", webmailJMAPURLApplyHandler)
}
