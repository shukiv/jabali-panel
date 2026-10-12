package main

// GH #2093: a hosting account that ran `jabali cron list` got
// "DATABASE_URL not configured", printed twice. The jabali command is the
// server's administration CLI and reads root-only files; the error now says
// so, once. The root listing named each job's owner by user id only, so an
// admin could not tell whose jobs were whose.

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// resetCLIGlobals gives the test fresh CLI state and restores the package
// globals afterwards: a config another test loaded would short-circuit
// initConfig.
func resetCLIGlobals(t *testing.T) {
	t.Helper()
	savedPath, savedCfg, savedDB, savedEnvFile := cfgPath, sharedCfg, sharedDB, defaultEnvFile
	savedLogger := slog.Default()
	t.Cleanup(func() {
		cfgPath, sharedCfg, sharedDB, defaultEnvFile = savedPath, savedCfg, savedDB, savedEnvFile
		slog.SetDefault(savedLogger)
	})
	cfgPath, sharedCfg, sharedDB = "", nil, nil
	defaultEnvFile = filepath.Join(t.TempDir(), "no-panel.env")
	unsetEnv(t, "DATABASE_URL")
	unsetEnv(t, "PANEL_ENV")
}

// unsetEnv removes key for the test and restores it afterwards.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// unreadableFile writes content to a mode-0000 file, which root can still
// read, so the test is skipped there.
func unreadableFile(t *testing.T, name, content string) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-0000 file")
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o000); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertRunAsRoot(t *testing.T, err error, path string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"jabali is the server's administration command and must run as root",
		"cannot read " + path + " (permission denied)",
		"Hosting accounts manage their own cron jobs, websites and mail in the panel",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q\n  does not say %q", msg, want)
		}
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error %q does not wrap fs.ErrPermission", msg)
	}
}

func TestCLI_UnreadableConfigSaysRunAsRoot(t *testing.T) {
	resetCLIGlobals(t)
	cfgPath = unreadableFile(t, "config.toml", "[server]\nenv = \"production\"\n")

	assertRunAsRoot(t, requireDB(nil, nil), cfgPath)
}

func TestCLI_UnreadableEnvFileSaysRunAsRoot(t *testing.T) {
	cases := map[string]string{
		// No config file: the defaults run as development, and the database
		// URL that only the env file holds is missing (the reporter's case).
		"development": "",
		// A production config fails validation without the env file.
		"production": "[server]\nenv = \"production\"\n",
	}
	for name, toml := range cases {
		t.Run(name, func(t *testing.T) {
			resetCLIGlobals(t)
			defaultEnvFile = unreadableFile(t, "panel.env", "DATABASE_URL=jabali:secret@unix(/run/mysqld/mysqld.sock)/jabali\n")
			cfgPath = filepath.Join(t.TempDir(), "config.toml")
			if toml != "" {
				if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			assertRunAsRoot(t, requireDB(nil, nil), defaultEnvFile)
		})
	}
}

// `jabali migrate` checks the database URL itself.
func TestCLI_MigrateSaysRunAsRoot(t *testing.T) {
	for _, args := range [][]string{{"migrate", "up"}, {"migrate", "status"}, {"migrate", "force", "1"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			resetCLIGlobals(t)
			defaultEnvFile = unreadableFile(t, "panel.env", "DATABASE_URL=jabali:secret@unix(/run/mysqld/mysqld.sock)/jabali\n")

			root := newRootCmd()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(append(args, "--config", filepath.Join(t.TempDir(), "config.toml")))
			assertRunAsRoot(t, root.Execute(), defaultEnvFile)
		})
	}
}

// A box with no env file at all is not a permissions problem: the old
// message stays.
func TestCLI_MissingEnvFileKeepsTheMessage(t *testing.T) {
	resetCLIGlobals(t)
	cfgPath = filepath.Join(t.TempDir(), "config.toml")

	err := requireDB(nil, nil)
	if err == nil || err.Error() != "DATABASE_URL not configured" {
		t.Fatalf("err = %v, want DATABASE_URL not configured", err)
	}
}

// Cobra printed the error and main printed it again. main is now the only
// one that prints it.
func TestCLI_ErrorIsPrintedOnce(t *testing.T) {
	resetCLIGlobals(t)
	path := unreadableFile(t, "config.toml", "")

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"cron", "list", "--config", path})
	err := root.Execute()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if strings.Contains(out.String(), err.Error()) {
		t.Errorf("cobra printed the error itself, so main prints it a second time:\n%s", out.String())
	}
}

func TestRenderCronJobs_NamesEachJobsAccount(t *testing.T) {
	username := "racingtips"
	users := []models.User{
		{ID: "01USERRACINGTIPS0000000000", Username: &username, Email: "owner@example.com"},
		// An admin has no Linux account name.
		{ID: "01USERADMIN000000000000000", Email: "admin@example.com"},
	}
	exit := 0
	ran := time.Date(2026, 10, 11, 23, 0, 43, 0, time.UTC)
	rows := []*models.CronJob{
		{ID: "01JOBSELECTIONS00000000000", UserID: users[0].ID, Name: "Racingtips Selections", Schedule: "0 * * * *", Enabled: true, LastRunAt: &ran, LastExitCode: &exit},
		{ID: "01JOBROOT00000000000000000", UserID: users[1].ID, Name: "Root job", Schedule: "0 3 * * *", Enabled: true},
		// The owner's account no longer exists.
		{ID: "01JOBORPHAN000000000000000", UserID: "01USERGONE0000000000000000", Name: "Orphan job", Schedule: "*/15 * * * *"},
	}

	var out bytes.Buffer
	if err := renderCronJobs(&out, rows, cronOwnerNames(users)); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want a header and 3 rows, got:\n%s", out.String())
	}
	if header := strings.Fields(lines[0]); len(header) < 2 || header[1] != "USER" {
		t.Errorf("second column = %v, want USER:\n%s", header, out.String())
	}
	for i, want := range []string{"racingtips", "admin@example.com", "01USERGONE0000000000000000"} {
		cols := strings.Fields(lines[i+1])
		if len(cols) < 2 || cols[1] != want {
			t.Errorf("row %d user = %v, want %q:\n%s", i+1, cols, want, out.String())
		}
	}
	if strings.Contains(out.String(), users[0].ID) {
		t.Errorf("the table still shows the account's user id:\n%s", out.String())
	}
}
