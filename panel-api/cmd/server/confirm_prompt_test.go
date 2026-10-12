package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/spf13/cobra"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A destructive command whose confirmation isn't answered yes changed nothing
// but exited 0, so a script couldn't tell it from success (found deleting a
// drill cron job over ssh, GH #2120 follow-up).

func TestConfirmOrAbort(t *testing.T) {
	for answer, confirmed := range map[string]bool{
		"":      false, // no terminal: nothing to read
		"\n":    false,
		"n\n":   false,
		"no\n":  false,
		"yy\n":  false,
		"y\n":   true,
		"Y\n":   true,
		"yes\n": true,
		"YES":   true,
	} {
		cmd := &cobra.Command{}
		err := confirmOrAbort(cmd, strings.NewReader(answer), "Delete it?", "--force")
		if confirmed {
			if err != nil {
				t.Errorf("%q: %v, want confirmed", answer, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%q: confirmed, want an error", answer)
			continue
		}
		if !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), "--force") {
			t.Errorf("%q: error %q must say nothing changed and name --force", answer, err)
		}
		if !cmd.SilenceUsage {
			t.Errorf("%q: the usage text must not follow a declined confirmation", answer)
		}
	}
}

// Through a real command: `jabali cron delete` with nothing on stdin fails and
// deletes nothing.
func TestCronDelete_UnansweredConfirmationFailsAndDeletesNothing(t *testing.T) {
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: raw}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT \\* FROM `cron_jobs` WHERE id = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "schedule", "created_at"}).
			AddRow("01CRONJOB0000000000000000A", "nightly", "0 3 * * *", time.Now()))

	savedDB, savedStdin := sharedDB, os.Stdin
	defer func() { sharedDB, os.Stdin = savedDB, savedStdin }()
	sharedDB = gdb
	stdin := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(stdin, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	empty, err := os.Open(stdin)
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	os.Stdin = empty

	cmd := newCronDeleteCmd()
	cmd.SetContext(context.Background())
	err = cmd.RunE(cmd, []string{"01CRONJOB0000000000000000A"})
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("RunE = %v, want the not-confirmed error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// No command answers a declined confirmation with "Cancelled."/"Aborted." and
// success again.
func TestNoConfirmationReturnsSuccessWhenDeclined(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, lit := range []string{`fmt.Println("Cancelled.")`, `fmt.Println("Aborted.")`} {
			if strings.Contains(string(b), lit) {
				t.Errorf("%s prints %s: return notConfirmed(cmd, flag) so the command exits 1", f, lit)
			}
		}
	}
}
