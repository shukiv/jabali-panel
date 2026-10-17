package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
)

// cliHostnameDB points sharedDB at a mock whose server_settings row has
// hostname dbHost ("" = the read fails), and config.toml's [server] hostname
// at cfgHost.
func cliHostnameDB(t *testing.T, dbHost, cfgHost string) {
	t.Helper()
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: raw}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	q := mock.ExpectQuery("SELECT \\* FROM `server_settings`")
	if dbHost == "" {
		q.WillReturnError(errors.New("db down"))
	} else {
		q.WillReturnRows(sqlmock.NewRows([]string{"id", "hostname"}).AddRow(1, dbHost))
	}

	savedDB, savedCfg := sharedDB, sharedCfg
	t.Cleanup(func() { sharedDB, sharedCfg = savedDB, savedCfg })
	sharedDB = gdb
	cfg := &config.Config{}
	cfg.Server.Hostname = cfgHost
	sharedCfg = cfg
}

// The CLI names the panel by the server_settings row, the hostname the panel
// itself serves on. config.toml's [server] hostname is the install-time seed:
// on a box renamed before the agent kept it in step it still holds the old
// name, so `jabali db sso` printed a login URL on the old host and `jabali
// user create` without --email made old-host placeholder addresses.
func TestCLIPanelHostname(t *testing.T) {
	t.Run("the settings row wins over a stale config.toml", func(t *testing.T) {
		cliHostnameDB(t, "new.example.com", "old.example.com")
		if got := panelHTTPSBaseForCLI(); got != "https://new.example.com" {
			t.Fatalf("panelHTTPSBaseForCLI() = %q, want https://new.example.com", got)
		}
	})

	t.Run("the row unreadable: config.toml", func(t *testing.T) {
		cliHostnameDB(t, "", "old.example.com")
		if got := panelHTTPSBaseForCLI(); got != "https://old.example.com" {
			t.Fatalf("panelHTTPSBaseForCLI() = %q, want https://old.example.com", got)
		}
	})

	t.Run("no DB handle: config.toml", func(t *testing.T) {
		savedDB, savedCfg := sharedDB, sharedCfg
		t.Cleanup(func() { sharedDB, sharedCfg = savedDB, savedCfg })
		sharedDB = nil
		cfg := &config.Config{}
		cfg.Server.Hostname = "old.example.com"
		sharedCfg = cfg
		if got := panelHTTPSBaseForCLI(); got != "https://old.example.com" {
			t.Fatalf("panelHTTPSBaseForCLI() = %q, want https://old.example.com", got)
		}
	})

	t.Run("neither: localhost", func(t *testing.T) {
		cliHostnameDB(t, "", "")
		if got := panelHTTPSBaseForCLI(); got != "https://localhost" {
			t.Fatalf("panelHTTPSBaseForCLI() = %q, want https://localhost", got)
		}
	})

	t.Run("user create without --email uses it too", func(t *testing.T) {
		cliHostnameDB(t, "new.example.com", "old.example.com")
		_, email, err := resolveCreateIdentity("alice", "", cliPanelHostname(context.Background()))
		if err != nil {
			t.Fatal(err)
		}
		if email != "alice@new.example.com" {
			t.Fatalf("placeholder email %q, want alice@new.example.com", email)
		}
	})
}

// recordingConverter records every SQL argument the mock receives.
type recordingConverter struct{ args *[]string }

func (c recordingConverter) ConvertValue(v any) (driver.Value, error) {
	if s, ok := v.(string); ok {
		*c.args = append(*c.args, s)
	}
	return driver.DefaultParameterConverter.ConvertValue(v)
}

// Through the real create path: `jabali user create` without --email stores
// a placeholder on the panel's own hostname, not config.toml's.
func TestCreateUserDirect_PlaceholderEmailUsesTheSettingsRow(t *testing.T) {
	var args []string
	raw, mock, err := sqlmock.New(
		sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp),
		sqlmock.ValueConverterOption(recordingConverter{&args}),
	)
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
	mock.ExpectQuery("SELECT \\* FROM `server_settings`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "hostname"}).AddRow(1, "new.example.com"))
	stop := errors.New("stop after the insert")
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `users`").WillReturnError(stop)
	mock.ExpectRollback()

	savedDB, savedCfg, savedAgent := sharedDB, sharedCfg, sharedAgent
	defer func() { sharedDB, sharedCfg, sharedAgent = savedDB, savedCfg, savedAgent }()
	sharedDB = gdb
	cfg := &config.Config{}
	cfg.Server.Hostname = "old.example.com"
	sharedCfg = cfg
	sharedAgent = &agent.Client{} // set, so initAgent doesn't dial; an admin never reaches it

	_, _, err = createUserDirect(context.Background(), cliUserInput{Username: "alice", Password: "0123456789abc", IsAdmin: true})
	if err == nil || !strings.Contains(err.Error(), stop.Error()) {
		t.Fatalf("createUserDirect err = %v, want the stubbed insert failure", err)
	}
	got := strings.Join(args, " ")
	if !strings.Contains(got, "alice@new.example.com") || strings.Contains(got, "old.example.com") {
		t.Fatalf("users insert args %q, want the placeholder alice@new.example.com", got)
	}
}
