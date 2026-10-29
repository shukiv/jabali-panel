package appseccfg

// bot_path_exclusions.go — paths of a site that skip the bot-detection
// challenge (GH #2328).
//
// An admin lists, per domain, path prefixes whose requests the AppSec
// bot-detection challenge leaves alone (an API, a webhook, a download that
// non-browser clients fetch), optionally for some HTTP methods only. The
// panel reconciler writes one entry per host (the domain and www.<domain>)
// to BotExemptPathsPath every pass, and the agent renders each entry into
// jabali-bot-exempt.yaml (RenderBotExempt) as an ExemptFromChallenge. The
// WAF, rate limits and CrowdSec IP decisions still apply to those requests:
// the exemption only skips the challenge.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BotPathExclusion is one path prefix of one host that skips the
// bot-detection challenge. Methods empty means every method.
type BotPathExclusion struct {
	Host       string   `json:"host"`
	PathPrefix string   `json:"path_prefix"`
	Methods    []string `json:"methods,omitempty"`
}

// MaxBotPathPrefixLen is the longest path prefix: the
// bot_challenge_exclusions.path_prefix column (varchar(512)).
const MaxBotPathPrefixLen = 512

// MaxBotPathExclusionsPerDomain bounds the paths one domain may list. Each
// renders to two filters (the domain and www.<domain>) that CrowdSec checks
// on every request.
const MaxBotPathExclusionsPerDomain = 50

// BotChallengeMethods is the closed set of methods an exclusion may name.
var BotChallengeMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// BotExemptPathsPath is the state file of per-path opt-outs: a JSON list of
// BotPathExclusion, one entry per host. Written by the panel reconciler
// (write-on-diff each pass), read by the agent for RenderBotExempt. Missing
// or empty → no per-path opt-outs, the safe default (every path challenged).
const BotExemptPathsPath = "/var/lib/jabali-panel/bot-exempt-paths.json"

// NormalizeBotMethods upper-cases, de-duplicates and sorts methods, and
// rejects any outside BotChallengeMethods. nil or empty means every method.
func NormalizeBotMethods(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, m := range in {
		m = strings.ToUpper(strings.TrimSpace(m))
		if !isBotChallengeMethod(m) {
			return nil, fmt.Errorf("method %q is not one of %s", m, strings.Join(BotChallengeMethods, ", "))
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	sort.Strings(out)
	return out, nil
}

func isBotChallengeMethod(m string) bool {
	for _, ok := range BotChallengeMethods {
		if m == ok {
			return true
		}
	}
	return false
}

// ValidateBotPathPrefix checks a path prefix before it is stored or
// rendered. The prefix is matched against the request's decoded path
// without the query string, so it must be written the same way.
//
// The quote, backslash and line-break checks keep the prefix inside its expr
// string literal and its YAML scalar. ".." and a leading "//" are refused
// because the rendered filter never matches them (see renderBotPathExclusions).
func ValidateBotPathPrefix(p string) error {
	switch {
	case p == "":
		return errors.New("path is required")
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q must start with /", p)
	case p == "/":
		return errors.New("a path of / covers the whole site: exempt the domain from the bot-detection challenge instead")
	case strings.HasPrefix(p, "//"):
		return fmt.Errorf("path %q must not start with //", p)
	case len(p) > MaxBotPathPrefixLen:
		return fmt.Errorf("path is longer than %d characters", MaxBotPathPrefixLen)
	case strings.ContainsAny(p, "\"\\") || hasLineBreakOrControl(p):
		return fmt.Errorf("path %q contains a quote, backslash, line break or control character", p)
	case strings.ContainsAny(p, "?#"):
		return fmt.Errorf("path %q: leave out the query string and fragment, the path is matched without them", p)
	case strings.Contains(p, ".."):
		return fmt.Errorf("path %q must not contain ..", p)
	case strings.Contains(p, "%"):
		return fmt.Errorf("path %q: write the path decoded (a space as a space, not %%20)", p)
	}
	return nil
}

// ValidateBotPathExclusion checks a state-file entry before it is rendered.
func ValidateBotPathExclusion(e BotPathExclusion) error {
	if clean := sanitizeWebmailHosts([]string{e.Host}); len(clean) != 1 || clean[0] != e.Host {
		return fmt.Errorf("host %q is not a lower-case host name", e.Host)
	}
	if err := ValidateBotPathPrefix(e.PathPrefix); err != nil {
		return err
	}
	_, err := NormalizeBotMethods(e.Methods)
	return err
}

// sortedBotPathExclusions returns a copy of list sorted by (host, path,
// methods), so an unchanged set renders and writes byte-identically.
func sortedBotPathExclusions(list []BotPathExclusion) []BotPathExclusion {
	sorted := make([]BotPathExclusion, len(list))
	copy(sorted, list)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.PathPrefix != b.PathPrefix {
			return a.PathPrefix < b.PathPrefix
		}
		return strings.Join(a.Methods, ",") < strings.Join(b.Methods, ",")
	})
	return sorted
}

// renderBotPathExclusions returns the pre_eval entries of RenderBotExempt for
// the per-path opt-outs, "" when there are none.
//
// The filter has the shape of the rule exclusions by name
// (RenderRuleExclusions): host equality (req.Host is a client-controlled
// header, never a prefix), the decoded path without the query string, and
// two guards against a raw URI that CrowdSec parses to a path nginx serves
// elsewhere: a path with ".." and one that parses with an authority
// ("//other/api/" has path "/api/"). Each filter is a single-quoted YAML
// scalar, since a path may hold ": " or " #".
func renderBotPathExclusions(list []BotPathExclusion) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("    # Per-path opt-outs (domain settings): one path prefix of one site,\n")
	b.WriteString("    # optionally for some methods only.\n")
	rendered := 0
	for _, e := range sortedBotPathExclusions(list) {
		if rendered >= maxOperatorExclusions {
			fmt.Fprintf(&b, "    # !! further path opt-outs NOT rendered — limit of %d reached.\n", maxOperatorExclusions)
			break
		}
		if err := ValidateBotPathExclusion(e); err != nil {
			// %q, never %s: an invalid entry is untrusted, and a raw newline
			// would end this comment and load the rest as live YAML.
			fmt.Fprintf(&b, "    # SKIPPED (%q %q %q): %q\n", e.Host, e.PathPrefix, e.Methods, err.Error())
			continue
		}
		filter := `req.Host == "` + e.Host + `" && req.URL.Host == "" && req.URL.Path startsWith "` + e.PathPrefix +
			`" && !(req.URL.Path contains "..")`
		if methods, _ := NormalizeBotMethods(e.Methods); len(methods) > 0 {
			filter += ` && req.Method in ["` + strings.Join(methods, `", "`) + `"]`
		}
		b.WriteString("    - filter: '" + strings.ReplaceAll(filter, "'", "''") + "'\n")
		b.WriteString("      apply:\n")
		b.WriteString(`        - ExemptFromChallenge("jabali-path-optout")` + "\n")
		rendered++
	}
	return b.String()
}

// LoadBotExemptPaths reads the per-path opt-out state file. A missing file is
// not an error (nil, nil). Entries are not validated here: RenderBotExempt
// validates each one as it renders.
func LoadBotExemptPaths(path string) ([]BotPathExclusion, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("appseccfg: read %s: %w", path, err)
	}
	var list []BotPathExclusion
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("appseccfg: parse %s: %w", path, err)
	}
	if len(list) == 0 {
		return nil, nil
	}
	return list, nil
}

// WriteBotExemptPaths writes list, sorted, to the state file atomically
// (tmp+rename in the same directory, 0644 like the host lists). Returns
// (changed, error): a no-op with changed=false when the file already holds
// the same entries.
func WriteBotExemptPaths(path string, list []BotPathExclusion) (changed bool, err error) {
	sorted := sortedBotPathExclusions(list)
	raw, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		return false, fmt.Errorf("appseccfg: encode bot exempt paths: %w", err)
	}
	body := string(raw) + "\n"
	if existing, readErr := os.ReadFile(path); readErr == nil && string(existing) == body {
		return false, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("appseccfg: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "bot-exempt-paths-*.tmp")
	if err != nil {
		return false, fmt.Errorf("appseccfg: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return false, fmt.Errorf("appseccfg: write tmp: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return false, fmt.Errorf("appseccfg: chmod tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("appseccfg: close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, fmt.Errorf("appseccfg: rename: %w", err)
	}
	return true, nil
}
