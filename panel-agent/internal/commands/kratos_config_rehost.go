package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// kratos.config.rehost rewrites the panel hostname throughout
// /etc/jabali-panel/kratos.yml and restarts jabali-kratos, so a panel
// hostname/FQDN change actually reaches the identity service (JAB-393).
//
// Why it exists: changing the panel FQDN through Settings updates the OS
// hostname and the server_settings / panel_certificate rows, but nothing
// regenerates kratos.yml — it is rendered only by install.sh from
// install/kratos.yml.tmpl (at install / `jabali update`). So after a live
// rename Kratos keeps emitting URLs for, and allowing CORS only from, the OLD
// origin. The SPA now runs on the new origin, its cross-origin call to the
// stale base_url is CORS-blocked, and the login page shows "Network error —
// could not reach identity service": a full login lockout.
//
// Why a targeted rewriter, not a template renderer: kratos.yml.tmpl
// substitutes seven values — three database credentials, two Kratos secrets,
// the panel hostname and the port suffix. Only the hostname changes here.
// Re-rendering would have to recover the DB credentials and secrets, and a
// wrong cookie secret logs every user out. Rewriting only the host token
// preserves the DSN, the secrets and the port suffix byte-for-byte. The panel
// FQDN never appears in the unix-socket DSN, so a host rewrite cannot corrupt
// the DB credentials.
//
// The host appears in two shapes in the rendered file: the https://<host>
// origins/URLs, and the bare webauthn/passkey rp.id ("<host>"). Both are
// rewritten — the WebAuthn origin check requires the origin and the RP id to
// stay consistent. This invalidates passkeys bound to the old registrable
// domain, which is inherent to any genuine domain change, not a regression.
type kratosConfigRehostParams struct {
	Hostname string `json:"hostname"`
}

type kratosConfigRehostResponse struct {
	Rewritten    bool   `json:"rewritten"`
	Reason       string `json:"reason,omitempty"`
	Replacements int    `json:"replacements,omitempty"`
}

var (
	// kratosConfigPath is the rendered Kratos config. Overridable in tests.
	kratosConfigPath = "/etc/jabali-panel/kratos.yml"
	// kratosRehostReloadFn restarts jabali-kratos after a rewrite. Seam so
	// unit tests don't spawn systemctl.
	kratosRehostReloadFn = defaultKratosRehostReload
)

// kratosBaseURLHostRE captures the panel host from the serve.public base_url
// line, e.g. `base_url: "https://mx.example.com:8443/.ory/"` -> mx.example.com.
var kratosBaseURLHostRE = regexp.MustCompile(`(?m)^\s*base_url:\s*"https://([a-zA-Z0-9.-]+)(?::[0-9]+)?/`)

// kratosConfigBaseURLHost returns the host from the first base_url line.
func kratosConfigBaseURLHost(data []byte) (string, bool) {
	m := kratosBaseURLHostRE.FindSubmatch(data)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// defaultKratosRehostReload restarts jabali-kratos on a detached, bounded
// context. Detached from the RPC ctx so a slow restart isn't cancelled
// mid-flight. The panel does NOT depend on jabali-kratos (no Requires/BindsTo),
// so this does not cascade the panel down; Kratos sessions are MariaDB-backed
// and the cookie secret is preserved, so users stay signed in across it.
func defaultKratosRehostReload(_ context.Context) error {
	bg, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return execCommandContext(bg, "systemctl", "restart", "jabali-kratos").Run()
}

func kratosConfigRehostHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p kratosConfigRehostParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	target := strings.ToLower(strings.TrimSpace(p.Hostname))
	if !sslDomainRegex.MatchString(target) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid hostname %q", p.Hostname),
		}
	}

	data, err := os.ReadFile(kratosConfigPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("read %s: %v", kratosConfigPath, err),
		}
	}

	current, ok := kratosConfigBaseURLHost(data)
	if !ok {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "could not find serve.public.base_url host in kratos.yml",
		}
	}
	if strings.EqualFold(current, target) {
		// Idempotent no-churn: config already on the current hostname. No
		// rewrite, no restart.
		return kratosConfigRehostResponse{Rewritten: false, Reason: "already current"}, nil
	}

	rewritten, n := rewriteKratosHost(data, current, target)
	if n == 0 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("no occurrences of host %q to rewrite", current),
		}
	}
	// Validate the result before touching disk. A corrupt or half-rewritten
	// kratos.yml is itself a lockout, so refuse to write anything that failed
	// to converge: no unresolved template placeholder may appear, and the
	// base_url host must now be the target.
	if bytes.Contains(rewritten, []byte("{{.")) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "refusing to write: unresolved template placeholder in kratos.yml",
		}
	}
	if h, ok := kratosConfigBaseURLHost(rewritten); !ok || !strings.EqualFold(h, target) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "refusing to write: base_url host did not converge to target",
		}
	}

	if err := writeFilePreservingMeta(kratosConfigPath, rewritten); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write %s: %v", kratosConfigPath, err),
		}
	}

	if err := kratosRehostReloadFn(ctx); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("restart jabali-kratos: %v", err),
		}
	}

	return kratosConfigRehostResponse{
		Rewritten:    true,
		Reason:       current + " -> " + target,
		Replacements: n,
	}, nil
}

// hostTokenBoundary matches a character that is NOT part of a hostname; a host
// token is a maximal run bounded by one of these (or a string edge) on each
// side. Anchoring on it makes the rewrite superstring-safe: replacing
// "example.com" never touches the "example.com" inside "mx.example.com"
// (the leading '.' is a hostname character), in either the rename or the
// drift-heal-back direction.
const hostTokenBoundary = `[^a-zA-Z0-9.-]`

// rewriteKratosHost replaces every whole-token occurrence of oldHost with
// newHost and returns the new bytes plus the replacement count. It rewrites
// both the https://<host> URLs and the bare webauthn/passkey rp.id "<host>",
// and leaves everything else (the DSN, secrets, port suffix) byte-for-byte.
func rewriteKratosHost(data []byte, oldHost, newHost string) ([]byte, int) {
	re := regexp.MustCompile(`(^|` + hostTokenBoundary + `)` + regexp.QuoteMeta(oldHost) + `(` + hostTokenBoundary + `|$)`)
	count := len(re.FindAll(data, -1))
	if count == 0 {
		return data, 0
	}
	return re.ReplaceAll(data, []byte("${1}"+newHost+"${2}")), count
}

// writeFilePreservingMeta writes data to path atomically (temp file in the same
// directory + rename) while preserving the original file's permission bits and
// owner/group. A truncated kratos.yml is a lockout, so the rename swap is the
// minimum bar, not extra defense.
func writeFilePreservingMeta(path string, data []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".kratos-rehost-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Removed on any early-return path; a no-op once the rename succeeds.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, fi.Mode().Perm()); err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if err := os.Chown(tmpName, int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	return os.Rename(tmpName, path)
}

func init() {
	Default.Register("kratos.config.rehost", kratosConfigRehostHandler)
}
