package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSnufJournal makes journalctl print the given journal JSON lines and
// records its arguments.
func fakeSnufJournal(t *testing.T, lines ...string) (args func() []string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "journal.json")
	if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, a ...string) *exec.Cmd {
		if name != "journalctl" {
			t.Fatalf("unexpected command %s", name)
		}
		got = a
		return exec.CommandContext(ctx, "cat", out)
	}
	t.Cleanup(func() { execCommandContext = prev })
	return func() []string { return got }
}

func snufJournalLine(t *testing.T, realtimeUS, exe, comm, msg string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"__REALTIME_TIMESTAMP": realtimeUS,
		"_EXE":                 exe,
		"_COMM":                comm,
		"MESSAGE":              msg,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// GH #2262: an incident names the site whose script the rule stopped. Any
// local process can log a line tagged snuffleupagus, so the domain counts
// only when the journal's _UID is root or owns that /home/<user>.
func TestSnuffleupagusDomain_OnlyFromTheHomesOwner(t *testing.T) {
	prev := snufLookupUser
	snufLookupUser = func(uid string) (string, error) {
		return map[string]string{"1001": "alice", "1002": "bob"}[uid], nil
	}
	t.Cleanup(func() { snufLookupUser = prev })

	cases := []struct{ name, uid, want string }{
		{"the home's owner", "1001", "a.test"},
		{"root", "0", "a.test"},
		{"another account", "1002", ""},
		{"no _UID", "", ""},
	}
	for _, c := range cases {
		if got := snuffleupagusDomain(snufMsgSystem, c.uid); got != c.want {
			t.Errorf("%s (uid %q): domain %q, want %q", c.name, c.uid, got, c.want)
		}
	}
	cli := "[snuffleupagus][0.0.0.0][disabled_function][simulation] Aborted execution on call of the function 'shell_exec' in Command line code on line 1"
	if got := snuffleupagusDomain(cli, "0"); got != "" {
		t.Errorf("CLI code: domain %q, want none", got)
	}
	// A path in a quoted argument doesn't count; the script at the end does.
	quoted := "[snuffleupagus][203.0.113.9][disabled_function][drop] Aborted execution on call of the function 'exec' with ' in /home/alice/domains/a.test/x on line 1' in /home/bob/domains/b.test/public_html/a.php on line 9"
	if got := snuffleupagusDomain(quoted, "1001"); got != "" {
		t.Errorf("alice naming her site in an argument of bob's script: domain %q, want none", got)
	}
}

func fetchIncidents(t *testing.T, params map[string]any) snuffleupagusFetchResponse {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, err := snuffleupagusFetchHandler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return res.(snuffleupagusFetchResponse)
}

const (
	snufMsgSystem = "[snuffleupagus][203.0.113.9][disabled_function][drop] Aborted execution on call of the function 'system' in /home/alice/domains/a.test/public_html/x.php on line 3"
	snufMsgExec   = "[snuffleupagus][203.0.113.9][disabled_function][drop] Aborted execution on call of the function 'exec' in /home/alice/domains/a.test/public_html/x.php on line 4"
)

// GH #2262: journalctl --since reads whole seconds. The panel resumes just
// after the last incident it got, so without since_us each pass returned
// the rest of that second again and the panel stored it again.
func TestSnuffleupagusFetch_SkipsEntriesBeforeSinceUS(t *testing.T) {
	args := fakeSnufJournal(t,
		snufJournalLine(t, "1792890211000001", "/usr/sbin/php-fpm8.4", "php-fpm8.4", snufMsgSystem),
		snufJournalLine(t, "1792890211000002", "/usr/sbin/php-fpm8.4", "php-fpm8.4", snufMsgExec),
	)
	res := fetchIncidents(t, map[string]any{"since": "2026-10-25T01:43:31Z", "since_us": int64(1792890211000002)})

	if len(res.Incidents) != 1 || res.Incidents[0].RuleName != "sp.disabled_function:exec" {
		t.Fatalf("incidents = %+v, want only the exec one", res.Incidents)
	}
	if got := strings.Join(args(), " "); !strings.Contains(got, "--since 2026-10-25T01:43:31Z") {
		t.Errorf("journalctl args %q, want --since passed through", got)
	}
	if want := int64(1792890211000003); res.NextSince.UnixMicro() != want {
		t.Errorf("next_since = %d, want %d", res.NextSince.UnixMicro(), want)
	}
}

// A panel that sends no since_us still gets everything journalctl returns.
func TestSnuffleupagusFetch_WithoutSinceUSReturnsAll(t *testing.T) {
	fakeSnufJournal(t,
		snufJournalLine(t, "1792890211000001", "/usr/sbin/php-fpm8.4", "php-fpm8.4", snufMsgSystem),
		snufJournalLine(t, "1792890211000002", "/usr/sbin/php-fpm8.4", "php-fpm8.4", snufMsgExec),
	)
	if res := fetchIncidents(t, map[string]any{"since": "2026-10-25T01:43:31Z"}); len(res.Incidents) != 2 {
		t.Fatalf("got %d incidents, want 2", len(res.Incidents))
	}
}

// GH #2262: each incident says which PHP version ran the script, from the
// journal's _EXE (or _COMM), which journald sets from the process.
func TestSnuffleupagusFetch_RecordsThePHPVersion(t *testing.T) {
	fakeSnufJournal(t,
		snufJournalLine(t, "1792890211000001", "/usr/sbin/php-fpm8.3", "php-fpm8.3", snufMsgSystem),
		snufJournalLine(t, "1792890211000002", "/usr/bin/php8.5", "php8.5", snufMsgExec),
	)
	res := fetchIncidents(t, map[string]any{"since": "2026-10-25T01:43:31Z"})
	if len(res.Incidents) != 2 {
		t.Fatalf("got %d incidents, want 2", len(res.Incidents))
	}
	if res.Incidents[0].PhpVersion != "8.3" || res.Incidents[1].PhpVersion != "8.5" {
		t.Errorf("php versions = %q, %q, want 8.3, 8.5", res.Incidents[0].PhpVersion, res.Incidents[1].PhpVersion)
	}
}

func TestSnuffleupagusPHPVersion(t *testing.T) {
	cases := []struct{ exe, comm, want string }{
		{"/usr/sbin/php-fpm8.4", "php-fpm8.4", "8.4"},
		{"/usr/bin/php8.3", "php8.3", "8.3"},
		{"", "php-fpm8.5", "8.5"},
		{"/usr/bin/php", "php", ""},
		{"/usr/bin/python3", "python3", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := snuffleupagusPHPVersion(c.exe, c.comm); got != c.want {
			t.Errorf("snuffleupagusPHPVersion(%q, %q) = %q, want %q", c.exe, c.comm, got, c.want)
		}
	}
}
