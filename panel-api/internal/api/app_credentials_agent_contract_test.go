package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/apps"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
)

// app_credentials_agent_contract_test.go — the install form never accepts an
// admin username or password that the app's agent installer then rejects.
//
// The panel creates the install row and the database before the agent sees
// the credentials, so a value the agent refuses fails the install late. This
// test reads each installer's own rule from the agent source (the username
// regexp, the password length or forbidden characters) and checks every
// candidate the panel's schema accepts against it.

const agentCommandsDir = "../../../panel-agent/internal/commands"

func agentSource(t *testing.T, app string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(agentCommandsDir, app+"_install.go"))
	if err != nil {
		t.Fatalf("read agent installer for %s: %v", app, err)
	}
	return string(b)
}

// agentRegexp compiles the regexp literal assigned to name in src.
func agentRegexp(t *testing.T, src, name string) *regexp.Regexp {
	t.Helper()
	m := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + "\\s*=\\s*regexp\\.MustCompile\\(`([^`]+)`\\)").FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("agent regexp %s not found", name)
	}
	return regexp.MustCompile(m[1])
}

// agentStrings returns the string slice literal assigned to name in src.
func agentStrings(t *testing.T, src, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*\[\]string\{([^}]*)\}`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("agent string list %s not found", name)
	}
	var out []string
	for _, q := range regexp.MustCompile(`"[^"]*"`).FindAllString(m[1], -1) {
		v, err := strconv.Unquote(q)
		if err != nil {
			t.Fatalf("unquote %s: %v", q, err)
		}
		out = append(out, v)
	}
	return out
}

// agentPasswordRule returns the agent's admin password check for one app.
func agentPasswordRule(t *testing.T, src string) func(string) bool {
	t.Helper()
	rules := []func(string) bool{}
	if m := regexp.MustCompile(`len\(req\.AdminPass\) < (\d+)`).FindStringSubmatch(src); m != nil {
		n, _ := strconv.Atoi(m[1])
		rules = append(rules, func(p string) bool { return len(p) >= n })
	}
	if m := regexp.MustCompile(`(len|utf8\.RuneCountInString)\(req\.AdminPass\); n < (\d+) \|\| n > (\d+)`).FindStringSubmatch(src); m != nil {
		count := func(p string) int { return len(p) } // bytes
		if m[1] != "len" {
			count = utf8.RuneCountInString
		}
		lo, _ := strconv.Atoi(m[2])
		hi, _ := strconv.Atoi(m[3])
		rules = append(rules, func(p string) bool { n := count(p); return n >= lo && n <= hi })
	}
	if m := regexp.MustCompile(`strings\.ContainsAny\(req\.AdminPass, ("[^"]*")\)`).FindStringSubmatch(src); m != nil {
		chars, err := strconv.Unquote(m[1])
		if err != nil {
			t.Fatalf("unquote %s: %v", m[1], err)
		}
		rules = append(rules, func(p string) bool { return !strings.ContainsAny(p, chars) })
	}
	return func(p string) bool {
		if p == "" {
			return false
		}
		for _, r := range rules {
			if !r(p) {
				return false
			}
		}
		return true
	}
}

var usernameCandidates = []string{
	"a", "ab", "abc", "Ab", "aB", "AB", "1ab", "_ab", "-ab", "ab-c", "ab_c", "ab.c", "a b", "ab@c", "Abc", "üser",
	"admin", "Admin", "ADMINS", "username", "osticket", "OsTicket", "12", "12345", "1e5", "1e-5",
	strings.Repeat("a", 19), strings.Repeat("a", 20), strings.Repeat("a", 21),
	strings.Repeat("a", 29), strings.Repeat("a", 30), strings.Repeat("a", 31),
	strings.Repeat("a", 32), strings.Repeat("a", 33), "x" + strings.Repeat("Y", 31),
}

func passwordCandidates() []string {
	out := []string{"=abcdefghijklmnop", "abcdefgh=ijklmnop", "line\nbreak1234567", "carriage\rreturn12", "nul\x00byte1234567", "ünïcödé!", "üüüüü", strings.Repeat("ü", 20), strings.Repeat("ü", 21)}
	for n := 1; n <= 24; n++ {
		out = append(out, strings.Repeat("p", n))
	}
	for n := 127; n <= 129; n++ {
		out = append(out, strings.Repeat("p", n))
	}
	return out
}

func TestInstallForm_AcceptsOnlyCredentialsTheAgentAccepts(t *testing.T) {
	reg := apps.New()
	if err := apps.RegisterDefaults(reg); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	cases := []struct {
		app         string
		userVar     string // agent username regexp; "" when the agent has none
		numericVar  string // agent regexp for usernames it refuses as numbers
		reservedVar string // agent list of usernames it refuses (any case)
		ucfirst     bool   // the panel capitalizes the first letter before the agent sees it
	}{
		{app: "wordpress"},
		{app: "joomla", userVar: "joomlaAdminUserPattern"},
		{app: "mediawiki", userVar: "mediawikiAdminUserPattern", ucfirst: true},
		{app: "drupal", userVar: "drupalAdminUserPattern"},
		{app: "flarum", userVar: "flarumAdminUserPattern"},
		{app: "phpbb", userVar: "phpbbAdminUserPattern"},
		{app: "opencart", userVar: "opencartAdminUserPattern"},
		{app: "openemr", userVar: "openemrUsernamePattern"},
		{app: "dokuwiki", userVar: "dokuwikiUsernamePattern"},
		{app: "nextcloud", userVar: "nextcloudAdminUserRE"},
		{app: "moodle", userVar: "moodleAdminUserPattern"},
		{app: "osticket", userVar: "osticketAdminUserPattern", numericVar: "osticketNumericUsernamePattern", reservedVar: "osticketReservedUsernames"},
		{app: "prestashop"},
		{app: "itflow"},
		{app: "invoiceshelf"},
	}
	for _, tc := range cases {
		t.Run(tc.app, func(t *testing.T) {
			desc, ok := reg.Get(tc.app)
			if !ok {
				t.Fatalf("%s not registered", tc.app)
			}
			src := agentSource(t, tc.app)
			schema := desc.InstallParamSchema

			if tc.userVar != "" {
				pattern := agentRegexp(t, src, tc.userVar)
				var numeric *regexp.Regexp
				if tc.numericVar != "" {
					numeric = agentRegexp(t, src, tc.numericVar)
				}
				var reserved []string
				if tc.reservedVar != "" {
					reserved = agentStrings(t, src, tc.reservedVar)
				}
				agentAccepts := func(u string) bool {
					if tc.ucfirst && u != "" {
						u = strings.ToUpper(u[:1]) + u[1:]
					}
					if !pattern.MatchString(u) || (numeric != nil && numeric.MatchString(u)) {
						return false
					}
					for _, r := range reserved {
						if strings.EqualFold(u, r) {
							return false
						}
					}
					return true
				}
				if gen := generateAdminUsername(6); !agentAccepts(gen) {
					t.Errorf("generated username %q fails the agent rule", gen)
				}
				if spec, has := schema["admin_username"]; has {
					for _, u := range usernameCandidates {
						if validateOne("admin_username", spec, u) != nil {
							continue
						}
						if !agentAccepts(u) {
							t.Errorf("form accepts admin_username %q, the agent installer rejects it", u)
						}
					}
					if validateOne("admin_username", spec, "") != nil {
						t.Error("blank admin_username (random) is refused")
					}
				}
			}

			if spec, has := schema["admin_password"]; has {
				agentPass := agentPasswordRule(t, src)
				for _, p := range passwordCandidates() {
					if validateOne("admin_password", spec, p) != nil {
						continue
					}
					if !agentPass(p) {
						t.Errorf("form accepts a %d-character admin_password %q, the agent installer rejects it", utf8.RuneCountInString(p), p)
					}
				}
				gen := ids.NewSecret()
				if tc.app == "opencart" {
					gen = ids.NewULID()[:20] // applications_service.go: OpenCart's own generator
				}
				if !agentPass(gen) {
					t.Errorf("generated password fails the agent rule")
				}
				if validateOne("admin_password", spec, "") != nil {
					t.Error("blank admin_password (generated) is refused")
				}
			}
		})
	}
}
