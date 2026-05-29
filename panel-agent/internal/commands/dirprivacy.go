// M50 — per-directory password protection (cPanel "Directory Privacy").
//
// The panel POSTs rules + bcrypt-hashed credentials in the domain.create
// payload. The agent writes one htpasswd file per rule under
// /etc/jabali-panel/dir-privacy/<rule_ulid>.htpasswd (0640 root:www-data,
// outside docroot) and the vhost template emits one
// `location ^~ <path>/ { auth_basic <realm>; auth_basic_user_file <path>; }`
// block per rule. Empty-cred rules get a placeholder htpasswd that can't
// match any password so the location 401s deny-by-default.
package commands

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	dirPrivacyDir       = "/etc/jabali-panel/dir-privacy"
	dirPrivacyFileMode  = 0o640
	dirPrivacyDirMode   = 0o755
	dirPrivacyGroupName = "www-data"
)

// directoryPrivacyRule mirrors the panel-side model. The htpasswd file
// is keyed by RuleID so the agent can deterministically locate and
// prune the per-rule file.
type directoryPrivacyRule struct {
	RuleID      string                       `json:"rule_id"`
	Path        string                       `json:"path"`
	Realm       string                       `json:"realm"`
	Credentials []directoryPrivacyCredential `json:"credentials"`
}

// directoryPrivacyCredential carries a bcrypt hash ($2y/$2a/$2b/$2x).
// Plaintext is never sent — the panel hashes at the API boundary so
// neither the wire nor agent logs ever see it.
type directoryPrivacyCredential struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

var (
	privacyPathAgentRE     = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
	privacyUsernameAgentRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	privacyBcryptRE        = regexp.MustCompile(`^\$2[abxy]\$`)
	privacyULIDRE          = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
)

// syncDirectoryPrivacyFiles writes the htpasswd file for every rule and
// removes any orphaned files (rule deleted on the panel but file still
// present on disk). On entry, rules is the full desired state for the
// caller's domain; the orphan sweep targets only files in dirPrivacyDir
// whose name (sans .htpasswd) appears in domainRuleIDs but not in rules.
//
// Returns the list of rule IDs that produced an htpasswd file so the
// caller can render only those into the vhost.
func syncDirectoryPrivacyFiles(rules []directoryPrivacyRule, domainRuleIDs []string) ([]string, error) {
	if err := ensureDirPrivacyDir(); err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(rules))
	written := make([]string, 0, len(rules))
	for i := range rules {
		r := &rules[i]
		if !privacyULIDRE.MatchString(r.RuleID) {
			return nil, fmt.Errorf("dirprivacy: invalid rule id %q", r.RuleID)
		}
		path, err := normalisePrivacyPath(r.Path)
		if err != nil {
			return nil, fmt.Errorf("dirprivacy: %w", err)
		}
		r.Path = path
		filePath := dirPrivacyFileForRule(r.RuleID)
		if err := writeHtpasswd(filePath, r.Credentials); err != nil {
			return nil, fmt.Errorf("dirprivacy: write %s: %w", filePath, err)
		}
		keep[r.RuleID] = true
		written = append(written, r.RuleID)
	}
	// Prune orphans for *this* domain only. domainRuleIDs is the list of
	// rule IDs panel knows about for this domain; rules is the subset we
	// just wrote. The difference is what to delete.
	for _, id := range domainRuleIDs {
		if !privacyULIDRE.MatchString(id) {
			continue
		}
		if keep[id] {
			continue
		}
		_ = os.Remove(dirPrivacyFileForRule(id))
	}
	return written, nil
}

// writeHtpasswd writes `<user>:<hash>` lines for each credential, atomic
// via tmp+rename, perms 0640, group ownership www-data so nginx can read.
// Empty creds → placeholder line `__deny__:!` (bcrypt rejects `!`, so
// 401 sticks).
func writeHtpasswd(filePath string, creds []directoryPrivacyCredential) error {
	var sb strings.Builder
	if len(creds) == 0 {
		sb.WriteString("__deny__:!\n")
	} else {
		for _, c := range creds {
			if !privacyUsernameAgentRE.MatchString(c.Username) {
				continue
			}
			if !privacyBcryptRE.MatchString(c.PasswordHash) {
				continue
			}
			sb.WriteString(c.Username)
			sb.WriteString(":")
			sb.WriteString(c.PasswordHash)
			sb.WriteString("\n")
		}
		if sb.Len() == 0 {
			// All credentials rejected by sanitiser — preserve deny-by-default.
			sb.WriteString("__deny__:!\n")
		}
	}
	tmp := filePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), dirPrivacyFileMode); err != nil {
		return err
	}
	// Best-effort chown root:www-data. Fails as non-root (tests/dev)
	// without affecting correctness — file perms 0640 still apply.
	_ = chownWWWData(tmp)
	return os.Rename(tmp, filePath)
}

// buildDirectoryPrivacyDirectives emits one nginx `location ^~ <path>/`
// block per rule. ^~ is the "longest-prefix-with-no-regex-override"
// modifier — once nginx picks this location it stops looking at regex
// locations, which is what we want for a password-gated directory.
//
// Empty rules slice → empty string (zero overhead, no comment line).
func buildDirectoryPrivacyDirectives(rules []directoryPrivacyRule) string {
	if len(rules) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# M50 directory privacy (auth_basic)\n")
	for _, r := range rules {
		if !privacyULIDRE.MatchString(r.RuleID) {
			continue
		}
		path, err := normalisePrivacyPath(r.Path)
		if err != nil {
			continue
		}
		realm := sanitisePrivacyRealm(r.Realm)
		htpasswd := dirPrivacyFileForRule(r.RuleID)
		sb.WriteString("    location ^~ ")
		sb.WriteString(path)
		sb.WriteString("/ {\n")
		sb.WriteString("        auth_basic ")
		sb.WriteString(strconv.Quote(realm))
		sb.WriteString(";\n")
		sb.WriteString("        auth_basic_user_file ")
		sb.WriteString(htpasswd)
		sb.WriteString(";\n")
		sb.WriteString("    }\n")
	}
	return sb.String()
}

// normalisePrivacyPath ensures the path starts with /, has no .., no //,
// no trailing slash (except root), and matches the safe charset.
func normalisePrivacyPath(in string) (string, error) {
	p := strings.TrimSpace(in)
	if p == "" {
		return "", fmt.Errorf("path required")
	}
	if p == "/" {
		return "/", nil
	}
	if !privacyPathAgentRE.MatchString(p) {
		return "", fmt.Errorf("path invalid charset")
	}
	if strings.Contains(p, "//") {
		return "", fmt.Errorf("path contains //")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path contains ..")
		}
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
	}
	return p, nil
}

// sanitisePrivacyRealm strips disallowed chars; falls back to "Restricted"
// on empty/garbage input. Belt-and-braces — panel already validated.
func sanitisePrivacyRealm(in string) string {
	r := strings.TrimSpace(in)
	if r == "" {
		return "Restricted"
	}
	var b strings.Builder
	for _, c := range r {
		if c < 0x20 || c > 0x7e {
			continue
		}
		if c == '"' || c == '\\' {
			continue
		}
		b.WriteRune(c)
	}
	if b.Len() == 0 {
		return "Restricted"
	}
	return b.String()
}

func dirPrivacyFileForRule(ruleID string) string {
	return filepath.Join(dirPrivacyDir, ruleID+".htpasswd")
}

func ensureDirPrivacyDir() error {
	if err := os.MkdirAll(dirPrivacyDir, dirPrivacyDirMode); err != nil {
		return fmt.Errorf("dirprivacy: mkdir %s: %w", dirPrivacyDir, err)
	}
	// Chown directory to www-data group so nginx (which doesn't need it
	// for reads with 0640 + group ownership on files, but does benefit
	// from directory rx for traversal under hardened umasks) can list.
	if err := chownWWWData(dirPrivacyDir); err != nil {
		// Non-fatal: directory may already exist with correct perms.
		// Fail later on the per-file chown if the group is really missing.
		return nil //nolint:nilerr
	}
	return nil
}

// chownWWWData sets owner root:www-data on path. Silently no-ops if the
// www-data group can't be resolved (test environments, exotic distros).
func chownWWWData(path string) error {
	grp, err := user.LookupGroup(dirPrivacyGroupName)
	if err != nil {
		return nil //nolint:nilerr
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return nil //nolint:nilerr
	}
	return os.Chown(path, 0, gid)
}
