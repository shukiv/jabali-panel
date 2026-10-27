package cronvalidate

import (
	"errors"
	"reflect"
	"testing"
)

// GH #2290: an account whose hosting package includes SSH may schedule its
// own shell scripts. The job runs in the same SSH sandbox as the account's
// shell, so it gives nothing SSH doesn't. Without SSH, the closed list stays.

var withSSH = Policy{ShellScripts: true}

func shValidate(raw string, pol Policy) ([]*Command, error) {
	return ValidateAnyMultiPolicy(raw, []string{pnDocroot}, []string{"x.test"}, pnHome, pol)
}

func TestShell_AcceptsAScriptUnderTheAccountWithSSH(t *testing.T) {
	cases := map[string][]string{
		"bash /home/u/bin/backup.sh":                 {"bash", "/home/u/bin/backup.sh"},
		"sh /home/u/bin/backup.sh nightly":           {"sh", "/home/u/bin/backup.sh", "nightly"},
		"/bin/bash /home/u/bin/backup.sh --full":     {"/bin/bash", "/home/u/bin/backup.sh", "--full"},
		"/usr/bin/sh /home/u/bin/backup.sh":          {"/usr/bin/sh", "/home/u/bin/backup.sh"},
		"bash /home/u/domains/x/public_html/task.sh": {"bash", "/home/u/domains/x/public_html/task.sh"},
		// The script path alone runs it with bash.
		"/home/u/bin/backup.sh":                {"/bin/bash", "/home/u/bin/backup.sh"},
		"/home/u/bin/backup.sh 'two words' -v": {"/bin/bash", "/home/u/bin/backup.sh", "two words", "-v"},
	}
	for raw, want := range cases {
		cmds, err := shValidate(raw, withSSH)
		if err != nil {
			t.Errorf("%q: expected accept, got %v", raw, err)
			continue
		}
		if len(cmds) != 1 || !reflect.DeepEqual(cmds[0].Argv, want) {
			t.Errorf("%q: argv = %v, want %v", raw, cmds, want)
		}
	}
}

func TestShell_NeedsSSH(t *testing.T) {
	for _, raw := range []string{"bash /home/u/bin/backup.sh", "/home/u/bin/backup.sh", "sh /home/u/x.sh"} {
		_, err := shValidate(raw, Policy{})
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != ErrCodeShellNeedsSSH {
			t.Errorf("%q without SSH: got %v, want %s", raw, err, ErrCodeShellNeedsSSH)
		}
	}
}

func TestShell_Rejects(t *testing.T) {
	cases := map[string]string{
		"inline -c":              "bash -c 'rm -rf /home/u'",
		"option before script":   "bash -x /home/u/bin/backup.sh",
		"login shell":            "bash --login /home/u/bin/backup.sh",
		"stdin":                  "bash -s",
		"no script":              "bash",
		"relative script":        "bash bin/backup.sh",
		"wrong extension":        "bash /home/u/bin/backup.py",
		"outside the account":    "bash /home/other/bin/backup.sh",
		"traversal":              "bash /home/u/../other/x.sh",
		"another shell":          "zsh /home/u/bin/backup.sh",
		"a shell of the account": "/home/u/bin/bash /home/u/bin/backup.sh",
		"pipe":                   "bash /home/u/bin/backup.sh | mail me",
		"redirect":               "/home/u/bin/backup.sh > /tmp/out",
		"chained":                "bash /home/u/x.sh; rm -rf /home/u",
		"script path elsewhere":  "/tmp/x.sh",
		"relative bare script":   "./backup.sh",
	}
	for name, raw := range cases {
		if _, err := shValidate(raw, withSSH); err == nil {
			t.Errorf("%s: expected reject, accepted %q", name, raw)
		}
	}
}

// A sequence may mix a shell script with the other commands.
func TestShell_InASequence(t *testing.T) {
	cmds, err := shValidate("php /home/u/domains/x/public_html/cron.php\nbash /home/u/bin/after.sh", withSSH)
	if err != nil || len(cmds) != 2 {
		t.Fatalf("sequence: %v, %v", cmds, err)
	}
	if _, err := shValidate("php /home/u/domains/x/public_html/cron.php\nbash /home/u/bin/after.sh", Policy{}); err == nil {
		t.Error("sequence with a shell line accepted without SSH")
	}
}

// The closed list is unchanged under either policy.
func TestShell_PolicyLeavesTheClosedList(t *testing.T) {
	for _, pol := range []Policy{{}, withSSH} {
		if _, err := shValidate("python /home/u/site/manage.py migrate", pol); err != nil {
			t.Errorf("python refused under %+v: %v", pol, err)
		}
		if _, err := shValidate("perl /home/u/x.pl", pol); err == nil {
			t.Errorf("perl accepted under %+v", pol)
		}
	}
}

func TestUsesShell(t *testing.T) {
	cases := map[string]bool{
		"bash /home/u/bin/backup.sh":                      true,
		"/home/u/bin/backup.sh":                           true,
		"php /home/u/x.php\n/usr/bin/sh /home/u/after.sh": true,
		"php /home/u/x.php":                               false,
		"python /home/u/x.py":                             false,
		"# bash /home/u/x.sh\nphp /home/u/x.php":          false,
	}
	for raw, want := range cases {
		if got := UsesShell(raw); got != want {
			t.Errorf("UsesShell(%q) = %v, want %v", raw, got, want)
		}
	}
}
