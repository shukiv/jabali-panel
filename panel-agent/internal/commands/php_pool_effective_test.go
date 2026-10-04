package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func setupEffectiveFixture(t *testing.T) {
	t.Helper()
	root := usePHPDefenseTempPaths(t)
	oldLib, oldRead := snuffleupagusLibRoot, phpEffectiveIniRead
	snuffleupagusLibRoot = filepath.Join(root, "lib")
	t.Cleanup(func() { snuffleupagusLibRoot, phpEffectiveIniRead = oldLib, oldRead })
	phpEffectiveIniRead = func(context.Context, string, string) (map[string]string, error) {
		return map[string]string{
			"disable_functions": "pcntl_alarm,exec",
			"include_path":      ".:/usr/share/php",
			"session.save_path": "",
		}, nil
	}
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(phpEtcRoot, "8.4", "fpm", "pool.d", "jabali-alice.conf"), `[jabali-alice]
php_admin_value[disable_functions] = exec,passthru,shell_exec
php_value[session.save_path] = /home/alice/tmp
php_admin_value[session.save_path] = /home/alice/.sessions
`)
	write(filepath.Join(snuffleupagusLibRoot, "8.4", "snuffleupagus.so"), "")
	write(filepath.Join(phpEtcRoot, "8.4", "fpm", "conf.d", "30-jabali-snuffleupagus.ini"), "")
}

func TestReadPHPPoolEffective(t *testing.T) {
	setupEffectiveFixture(t)
	got := readPHPPoolEffective(context.Background(), "8.4", "alice")
	if !got.PoolFound {
		t.Fatal("pool conf not found")
	}
	// php.ini's entries are server-wide; a pool entry php.ini already has is
	// reported once, as php.ini.
	want := map[string]string{"exec": "php.ini", "pcntl_alarm": "php.ini", "passthru": "pool", "shell_exec": "pool"}
	if len(got.DisabledFunctions) != len(want) {
		t.Fatalf("disabled = %+v", got.DisabledFunctions)
	}
	for _, f := range got.DisabledFunctions {
		if want[f.Name] != f.Source {
			t.Errorf("%s source = %s, want %s", f.Name, f.Source, want[f.Name])
		}
	}
	if got.IncludePath != (phpIniEffective{Value: ".:/usr/share/php", Source: "php.ini"}) {
		t.Errorf("include_path = %+v", got.IncludePath)
	}
	// php_admin_value beats php_value in the pool conf.
	if got.SessionSavePath != (phpIniEffective{Value: "/home/alice/.sessions", Source: "pool"}) {
		t.Errorf("session.save_path = %+v", got.SessionSavePath)
	}
	pd := got.PHPDefense
	if !pd.Active || pd.Mode != "enforce" || pd.PoolRules {
		t.Fatalf("php defense = %+v", pd)
	}
	states := map[string]string{}
	for _, f := range pd.Functions {
		states[f.Name] = f.State
	}
	// The fixture's shell_exec line is in simulation form; putenv has a
	// filter, so it is not a flat ban.
	if states["exec"] != "blocked" || states["shell_exec"] != "logged" || states["phpinfo"] != "blocked" {
		t.Errorf("states = %v", states)
	}
	if _, ok := states["putenv"]; ok {
		t.Error("a filtered rule is reported as a ban")
	}
}

// A pool with its own copy reports the copy's bans.
func TestReadPHPPoolEffectivePoolCopy(t *testing.T) {
	setupEffectiveFixture(t)
	if _, err := applyPoolPHPDefense("8.4", "alice", []string{"exec", "system"}); err != nil {
		t.Fatal(err)
	}
	pd := readPHPPoolEffective(context.Background(), "8.4", "alice").PHPDefense
	if !pd.PoolRules {
		t.Fatal("the pool's own copy was not used")
	}
	for _, f := range pd.Functions {
		if f.Name == "exec" || f.Name == "system" {
			t.Errorf("%s is lifted for this pool but reported %s", f.Name, f.State)
		}
	}
}

// A pointer outside the agent's pool-copy dir is ignored.
func TestReadPoolPHPDefenseIgnoresForeignPointer(t *testing.T) {
	setupEffectiveFixture(t)
	ini := phpDefensePoolIniPath("8.4", "alice")
	if err := os.MkdirAll(filepath.Dir(ini), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ini, []byte("sp.configuration_file=/etc/shadow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if pd := readPoolPHPDefense("8.4", "alice"); pd.PoolRules {
		t.Fatal("followed a pointer outside the pool-copy dir")
	}
}

func TestReadPHPPoolEffectiveNoModuleNoIni(t *testing.T) {
	setupEffectiveFixture(t)
	if err := os.Remove(filepath.Join(snuffleupagusLibRoot, "8.4", "snuffleupagus.so")); err != nil {
		t.Fatal(err)
	}
	phpEffectiveIniRead = func(context.Context, string, string) (map[string]string, error) {
		return nil, errors.New("php8.4: not found")
	}
	got := readPHPPoolEffective(context.Background(), "8.4", "alice")
	if got.PHPDefense.Active || len(got.PHPDefense.Functions) != 0 {
		t.Errorf("PHP Defense reported without the module: %+v", got.PHPDefense)
	}
	if got.IniReadError == "" {
		t.Error("a failed php.ini read must be reported, not guessed")
	}
	if len(got.DisabledFunctions) != 3 {
		t.Errorf("the pool's own list must still be reported: %+v", got.DisabledFunctions)
	}
}

func TestPHPPoolEffectiveHandlerValidates(t *testing.T) {
	for _, raw := range []string{
		`{"php_version":"8.4;id","slug":"alice"}`,
		`{"php_version":"8.4","slug":"../etc"}`,
		`{"php_version":"8.4","slug":"Alice"}`,
	} {
		_, err := phpPoolEffectiveHandler(context.Background(), json.RawMessage(raw))
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%s: err = %v, want invalid_argument", raw, err)
		}
	}
}
