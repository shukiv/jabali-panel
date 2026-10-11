package commands

import (
	"context"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
)

// prodCheckHostReserve is the seam's value before TestMain stubs it.
var prodCheckHostReserve = checkHostReserve

// The seam's production value is the real check: a stub left in the
// non-test file would switch the reserve off on every server, and TestMain's
// own stub would hide it from every other test.
func TestReserveSeam_ProductionIsTheRealCheck(t *testing.T) {
	got := reflect.ValueOf(prodCheckHostReserve).Pointer()
	if want := reflect.ValueOf(hostreserve.CheckReserve).Pointer(); got != want {
		t.Error("checkHostReserve is not hostreserve.CheckReserve outside tests")
	}
}

// TestReserveSeam_NoRawCheck: every reserve check in the package goes
// through checkHostReserve, so TestMain's stub covers it and no test reads
// the free space of the machine it runs on.
func TestReserveSeam_NoRawCheck(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\bhostreserve\.CheckReserve\(`)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if loc := re.FindIndex(b); loc != nil {
			line := 1 + strings.Count(string(b[:loc[0]]), "\n")
			t.Errorf("%s:%d: raw hostreserve.CheckReserve — call checkHostReserve instead", name, line)
		}
	}
}

// refuseHostReserve makes every reserve check fail for one test, as on a
// full disk, and returns the paths checked. Not for parallel tests.
func refuseHostReserve(t *testing.T) *[]string {
	t.Helper()
	var paths []string
	prev := checkHostReserve
	checkHostReserve = func(path string, need int64) error {
		paths = append(paths, path)
		return errors.New("host reserve: test disk is full")
	}
	t.Cleanup(func() { checkHostReserve = prev })
	return &paths
}

// A PostgreSQL load does not start when its storage is under the reserve.
func TestPgLoadScoped_RefusesWhenStorageIsUnderTheHostReserve(t *testing.T) {
	paths := refuseHostReserve(t)
	w := newPgWorld(t, func(string) (string, bool) { return "", false })

	err := pgLoadScoped(context.Background(), "alice_pgx", dumpFile(t), "", nil)

	if err == nil || err.Code != agentwire.CodeUnavailable || !strings.HasPrefix(err.Message, "database storage is under the host disk reserve: ") {
		t.Fatalf("got %v, want unavailable naming the host disk reserve", err)
	}
	if len(*paths) != 1 || (*paths)[0] != "/var/lib/postgresql" {
		t.Errorf("checked %q, want only /var/lib/postgresql", *paths)
	}
	if len(w.lines) != 0 {
		t.Errorf("ran %q, want nothing", w.lines)
	}
}

// A MariaDB restore does not start when its storage is under the reserve.
func TestDBRestore_RefusesWhenStorageIsUnderTheHostReserve(t *testing.T) {
	paths := refuseHostReserve(t)

	_, err := dbRestoreHandler(context.Background(), []byte(`{"db_name":"alice_db","path":"/home/alice/x.sql"}`))

	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeUnavailable || !strings.HasPrefix(ae.Message, "database storage is under the host disk reserve: ") {
		t.Fatalf("got %v, want unavailable naming the host disk reserve", err)
	}
	if len(*paths) != 1 || (*paths)[0] != "/var/lib/mysql" {
		t.Errorf("checked %q, want only /var/lib/mysql", *paths)
	}
}
