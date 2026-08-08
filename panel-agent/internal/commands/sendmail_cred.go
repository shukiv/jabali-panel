package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// sendmail.cred.ensure / sendmail.cred.remove (JAB-230) — manage the
// per-domain relay credentials the jabali-sendmail shim reads. Layout:
//
//	/etc/jabali-panel/sendmail/<user>/           0750 root:<usergroup>
//	/etc/jabali-panel/sendmail/<user>/<dom>.cred 0640 root:<usergroup>
//	/etc/jabali-panel/sendmail/<user>/default.cred -> <dom>.cred
//
// The group-read bit is the whole security model: PHP running as the tenant
// can read ITS creds and nobody else's, so a hostile From header can only
// ever select among identities the tenant already owns.

const sendmailCredRoot = "/etc/jabali-panel/sendmail"

// sendmailCredRootDir allows tests to redirect the tree.
func sendmailCredRootDir() string {
	if v := os.Getenv("JABALI_SENDMAIL_CRED_ROOT"); v != "" {
		return v
	}
	return sendmailCredRoot
}

var sendmailDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// sendmailChown is os.Chown in production (the agent runs as root). Tests
// run unprivileged and override it — POSIX ownership is asserted on the real
// box by the install validator + E2E, not by unit tests.
var sendmailChown = os.Chown

type sendmailCredEnsureParams struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// Host is the TLS certificate name of the local submission listener
	// (the panel's mail FQDN); the shim verifies against it.
	Host string `json:"host"`
	// MakeDefault forces default.cred to point at this domain. Independent
	// of it, the FIRST cred provisioned for a user always becomes default.
	MakeDefault bool `json:"make_default,omitempty"`
}

type sendmailCredEnsureResponse struct {
	Ok      bool `json:"ok"`
	Changed bool `json:"changed"`
}

func validateSendmailTarget(username, domain string) (*user.User, error) {
	if !phpPoolUsernameRegex.MatchString(username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid username"}
	}
	if !sendmailDomainRe.MatchString(domain) || strings.Contains(domain, "..") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid domain"}
	}
	u, err := user.Lookup(username)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("user %s: %v", username, err)}
	}
	return u, nil
}

func sendmailCredEnsureHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p sendmailCredEnsureParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if _, err := requireEmail(p.Email); err != nil {
		return nil, err
	}
	if p.Password == "" || strings.ContainsAny(p.Password, "\n\r") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "password required and must be single-line"}
	}
	if p.Host == "" || strings.ContainsAny(p.Host, "\n\r ") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "host required"}
	}
	u, err := validateSendmailTarget(p.Username, p.Domain)
	if err != nil {
		return nil, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("gid of %s: %v", p.Username, err)}
	}

	dir := filepath.Join(sendmailCredRootDir(), p.Username)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v", dir, err)}
	}
	if err := sendmailChown(dir, 0, gid); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown %s: %v", dir, err)}
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod %s: %v", dir, err)}
	}

	content := fmt.Sprintf("# Managed by jabali-panel (JAB-230). Read by jabali-sendmail.\nemail=%s\npassword=%s\nhost=%s\n", p.Email, p.Password, p.Host)
	credPath := filepath.Join(dir, p.Domain+".cred")

	changed := true
	if existing, rerr := os.ReadFile(credPath); rerr == nil && string(existing) == content {
		changed = false
	}
	if changed {
		if err := writeAtomic(credPath, []byte(content), 0o640); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write cred: %v", err)}
		}
		if err := sendmailChown(credPath, 0, gid); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown cred: %v", err)}
		}
	}

	if err := ensureDefaultCredSymlink(dir, p.Domain+".cred", p.MakeDefault); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("default symlink: %v", err)}
	}
	return sendmailCredEnsureResponse{Ok: true, Changed: changed}, nil
}

type sendmailCredRemoveParams struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
}

func sendmailCredRemoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p sendmailCredRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	// The user may already be deleted when the domain cascade runs — validate
	// shapes only, don't require the OS user to still exist.
	if !phpPoolUsernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid username"}
	}
	if !sendmailDomainRe.MatchString(p.Domain) || strings.Contains(p.Domain, "..") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid domain"}
	}

	dir := filepath.Join(sendmailCredRootDir(), p.Username)
	credName := p.Domain + ".cred"
	if err := os.Remove(filepath.Join(dir, credName)); err != nil && !os.IsNotExist(err) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove cred: %v", err)}
	}

	// Re-point default.cred if it referenced the removed domain; drop the
	// whole dir when the last cred is gone.
	defPath := filepath.Join(dir, "default.cred")
	if target, err := os.Readlink(defPath); err == nil && target == credName {
		_ = os.Remove(defPath)
		if remaining := listCredFiles(dir); len(remaining) > 0 {
			_ = os.Symlink(remaining[0], defPath)
		}
	}
	if remaining := listCredFiles(dir); len(remaining) == 0 {
		_ = os.Remove(defPath)
		_ = os.Remove(dir) // fails harmlessly if anything unexpected is inside
	}
	return okBody{Ok: true}, nil
}

// ensureDefaultCredSymlink makes default.cred exist. force re-points it.
// Uses symlink-swap (create temp, rename) so readers never see a missing link.
func ensureDefaultCredSymlink(dir, credName string, force bool) error {
	defPath := filepath.Join(dir, "default.cred")
	current, err := os.Readlink(defPath)
	switch {
	case err == nil && !force:
		return nil // exists, not forcing
	case err == nil && force && current == credName:
		return nil // already pointing right
	case err != nil && !os.IsNotExist(err):
		// default.cred exists but is not a symlink — replace it below.
	}
	tmp := defPath + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(credName, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, defPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// removeSendmailCredForDomain reaps <domain>.cred from EVERY user subtree
// (domain names are box-unique, so at most one match) and re-points that
// user's default.cred. Called from domain.delete — the one chokepoint all
// panel delete paths share — where the owning username is no longer known.
// Best-effort by design: a leftover cred file is inert once the mailbox row
// (its authentication) is gone with the domain.
func removeSendmailCredForDomain(domain string) {
	if !sendmailDomainRe.MatchString(domain) || strings.Contains(domain, "..") {
		return
	}
	credName := domain + ".cred"
	matches, err := filepath.Glob(filepath.Join(sendmailCredRootDir(), "*", credName))
	if err != nil {
		return
	}
	for _, m := range matches {
		dir := filepath.Dir(m)
		_ = os.Remove(m)
		defPath := filepath.Join(dir, "default.cred")
		if target, rerr := os.Readlink(defPath); rerr == nil && target == credName {
			_ = os.Remove(defPath)
			if remaining := listCredFiles(dir); len(remaining) > 0 {
				_ = os.Symlink(remaining[0], defPath)
			}
		}
		if remaining := listCredFiles(dir); len(remaining) == 0 {
			_ = os.Remove(defPath)
			_ = os.Remove(dir)
		}
	}
}

func listCredFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if name == "default.cred" || !strings.HasSuffix(name, ".cred") || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func init() {
	Default.Register("sendmail.cred.ensure", sendmailCredEnsureHandler)
	Default.Register("sendmail.cred.remove", sendmailCredRemoveHandler)
}
