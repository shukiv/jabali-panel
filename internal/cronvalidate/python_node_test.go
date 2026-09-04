package cronvalidate

import "testing"

// GH #1435: python and node were advertised in the docs but the validator only
// accepted wp/php. These lock the added interpreters: a bare/versioned/venv
// interpreter running an absolute script file inside the account (home or a
// docroot), with inline-code execution refused.

const (
	pnHome    = "/home/u"
	pnDocroot = "/home/u/domains/x/public_html"
)

func pnValidate(t *testing.T, raw string) (*Command, error) {
	t.Helper()
	return ValidateCommand(raw, []string{pnDocroot}, pnHome)
}

func TestPython_AcceptsScriptUnderHome(t *testing.T) {
	// The reporter's Django case: absolute manage.py + a subcommand, no cd.
	cases := []string{
		"python /home/u/site/manage.py migrate",
		"python3 /home/u/site/manage.py runjobs",
		"python3.11 /home/u/site/manage.py all-import",
		"/home/u/venv/bin/python /home/u/site/manage.py migrate",       // virtualenv interpreter
		"/home/u/.pyenv/versions/3.11.0/bin/python3.11 /home/u/x/m.py", // pyenv build
		"python /home/u/domains/x/public_html/task.py",                 // docroot also allowed
		"python -V",
		"python --version",
	}
	for _, raw := range cases {
		if _, err := pnValidate(t, raw); err != nil {
			t.Errorf("expected accept, got %v for %q", err, raw)
		}
	}
}

func TestPython_Rejects(t *testing.T) {
	cases := map[string]string{
		"inline -c":       "python -c pass",
		"module -m":       "python -m http.server",
		"stdin -":         "python -",
		"relative script": "python manage.py migrate",
		"wrong extension": "python /home/u/site/manage.txt",
		"outside home":    "python /home/other/site/manage.py",
		"traversal":       "python /home/u/../other/x.py",
		"no argument":     "python",
	}
	for name, raw := range cases {
		if _, err := pnValidate(t, raw); err == nil {
			t.Errorf("%s: expected reject, accepted %q", name, raw)
		}
	}
}

func TestNode_AcceptsScriptUnderHome(t *testing.T) {
	cases := []string{
		"node /home/u/app/server.js",
		"nodejs /home/u/app/job.js run",
		"/home/u/.nvm/versions/node/v20/bin/node /home/u/app/task.mjs",
		"node /home/u/domains/x/public_html/build.cjs",
		"node -v",
	}
	for _, raw := range cases {
		if _, err := pnValidate(t, raw); err != nil {
			t.Errorf("expected accept, got %v for %q", err, raw)
		}
	}
}

func TestNode_Rejects(t *testing.T) {
	cases := map[string]string{
		"eval -e":         "node -e code",
		"eval --eval":     "node --eval code",
		"print -p":        "node -p code",
		"relative script": "node app/server.js",
		"wrong extension": "node /home/u/app/server.ts",
		"outside home":    "node /home/other/app/server.js",
	}
	for name, raw := range cases {
		if _, err := pnValidate(t, raw); err == nil {
			t.Errorf("%s: expected reject, accepted %q", name, raw)
		}
	}
}

// Without a home (ownedHome==""), a python/node script must fall under a
// docroot — the cpanel-restore path passes home, but a defensive "" must not
// silently widen containment.
func TestPythonNode_NoHomeRestrictsToDocroot(t *testing.T) {
	if _, err := ValidateCommand("python /home/u/site/manage.py migrate", []string{pnDocroot}, ""); err == nil {
		t.Error("python under home but not docroot must be rejected when ownedHome is empty")
	}
	if _, err := ValidateCommand("python /home/u/domains/x/public_html/task.py", []string{pnDocroot}, ""); err != nil {
		t.Errorf("python under docroot must pass even with empty home: %v", err)
	}
}
