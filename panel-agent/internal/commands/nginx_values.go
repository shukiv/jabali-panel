package commands

import (
	"fmt"
	"net"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// The vhost renderers (webmail.vhost_apply, docker_app.vhost_apply,
// mail.webadmin.apply) put their params into nginx config with text/template,
// which escapes nothing. These checks keep a value from changing the config it
// lands in. Like validateDocRootForNginx they refuse only the bytes nginx
// treats as syntax, so every well-formed name and path still passes.

// validateNginxName checks a host name rendered into server_name, a comment
// or a quoted string.
func validateNginxName(field, v string) error {
	if r, ok := nginxUnsafeRune(v); ok {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("%s: character %q not allowed", field, r)}
	}
	return nil
}

// validateNginxPath checks an absolute file path rendered into a directive
// (ssl_certificate, root).
func validateNginxPath(field, v string) error {
	if !strings.HasPrefix(v, "/") {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: field + " must be an absolute path"}
	}
	if err := validateDocRootForNginx(v); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("%s: %v", field, err)}
	}
	return nil
}

// validateListenIP checks a listen address: empty, or a plain IPv4 (want4) or
// IPv6 address.
func validateListenIP(field, v string, want4 bool) error {
	if v == "" {
		return nil
	}
	ip := net.ParseIP(v)
	if ip == nil || (ip.To4() != nil) != want4 {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("%s %q is not a valid address", field, v)}
	}
	return nil
}

func nginxUnsafeRune(v string) (rune, bool) {
	for _, r := range v {
		if r < 0x20 || strings.ContainsRune(docRootUnsafeChars, r) {
			return r, true
		}
	}
	return 0, false
}
