package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func usePHPEtcTempRoot(t *testing.T) string {
	t.Helper()
	old := phpEtcRoot
	phpEtcRoot = t.TempDir()
	t.Cleanup(func() { phpEtcRoot = old })
	return phpEtcRoot
}

// The OPcache sizing directives leave the pool conf's admin values; everything
// else, OPcache's runtime directives included, stays in the pool.
func TestSplitOpcacheStartupValues(t *testing.T) {
	in := []KV{
		{Name: "memory_limit", Value: "256M"},
		{Name: "opcache.memory_consumption", Value: "384"},
		{Name: "opcache.revalidate_freq", Value: "10"},
		{Name: "opcache.max_accelerated_files", Value: "100000"},
		{Name: "opcache.jit", Value: "tracing"},
		{Name: "opcache.jit_buffer_size", Value: "64M"},
	}
	pool, startup := splitOpcacheStartupValues(in)
	wantPool := []KV{{Name: "memory_limit", Value: "256M"}, {Name: "opcache.revalidate_freq", Value: "10"}}
	wantStartup := []KV{
		{Name: "opcache.memory_consumption", Value: "384"},
		{Name: "opcache.max_accelerated_files", Value: "100000"},
		{Name: "opcache.jit", Value: "tracing"},
		{Name: "opcache.jit_buffer_size", Value: "64M"},
	}
	if !reflect.DeepEqual(pool, wantPool) {
		t.Errorf("pool = %v, want %v", pool, wantPool)
	}
	if !reflect.DeepEqual(startup, wantStartup) {
		t.Errorf("startup = %v, want %v", startup, wantStartup)
	}
}

// A pool rendered from the repo template with an OPcache sizing override
// carries no php_admin_value for it: that line is what never took effect.
func TestPoolConfHasNoOpcacheSizing(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "install", "php", "jabali-php-pool.conf.tmpl"))
	if err != nil {
		t.Fatalf("read repo pool template: %v", err)
	}
	tmpl := template.Must(template.New("pool").Parse(string(b)))
	poolValues, _ := splitOpcacheStartupValues([]KV{
		{Name: "opcache.memory_consumption", Value: "384"},
		{Name: "opcache.revalidate_freq", Value: "10"},
	})
	var out strings.Builder
	if err := tmpl.Execute(&out, phpPoolSpecTemplate{
		PoolName: "jabali-alice", User: "alice", Group: "alice", SocketPath: "/run/php/jabali-alice/fpm.sock",
		PmMode: "ondemand", PmMaxChildren: 5, ProcessIdleTimeoutSeconds: 10, AdminValues: poolValues,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	s := out.String()
	if strings.Contains(s, "opcache.memory_consumption") || strings.Contains(s, "opcache.max_accelerated_files") {
		t.Errorf("pool conf sets OPcache sizing:\n%s", s)
	}
	if !strings.Contains(s, "php_admin_value[opcache.revalidate_freq] = 10") {
		t.Error("a runtime OPcache directive must stay in the pool conf")
	}
}

func TestApplyPoolOpcacheIni(t *testing.T) {
	root := usePHPEtcTempRoot(t)
	ini84 := filepath.Join(root, "8.4", "jabali-ext", "alice", phpPoolOpcacheIniName)
	ini85 := filepath.Join(root, "8.5", "jabali-ext", "alice", phpPoolOpcacheIniName)

	// Nothing to set and nothing there: no file, no change (no restart).
	changed, err := applyPoolOpcacheIni("8.4", "alice", nil)
	if err != nil || changed {
		t.Fatalf("empty apply: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(ini84); !os.IsNotExist(err) {
		t.Fatalf("empty apply wrote %s", ini84)
	}

	startup := []KV{{Name: "opcache.memory_consumption", Value: "384"}, {Name: "opcache.max_accelerated_files", Value: "100000"}}
	changed, err = applyPoolOpcacheIni("8.4", "alice", startup)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	got, err := os.ReadFile(ini84)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"opcache.memory_consumption=384\n", "opcache.max_accelerated_files=100000\n"} {
		if !strings.Contains(string(got), line) {
			t.Errorf("ini lacks %q:\n%s", line, got)
		}
	}
	if fi, _ := os.Stat(ini84); fi.Mode().Perm() != 0o644 {
		t.Errorf("ini mode = %v, want 0644 (the master runs as the tenant)", fi.Mode().Perm())
	}

	// The same values again: nothing changed, so no restart.
	if changed, err = applyPoolOpcacheIni("8.4", "alice", startup); err != nil || changed {
		t.Fatalf("re-apply: changed=%v err=%v", changed, err)
	}

	// The pool moved to 8.5: the 8.4 copy goes, the 8.5 one is written.
	if changed, err = applyPoolOpcacheIni("8.5", "alice", startup); err != nil || !changed {
		t.Fatalf("version move: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(ini84); !os.IsNotExist(err) {
		t.Error("the old version's ini was left behind")
	}
	if _, err := os.Stat(ini85); err != nil {
		t.Errorf("no ini for the new version: %v", err)
	}

	// Overrides cleared: the ini goes, and that is a change.
	if changed, err = applyPoolOpcacheIni("8.5", "alice", nil); err != nil || !changed {
		t.Fatalf("clear: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(ini85); !os.IsNotExist(err) {
		t.Error("cleared overrides left the ini")
	}
}

func TestRemovePoolOpcacheIni(t *testing.T) {
	root := usePHPEtcTempRoot(t)
	startup := []KV{{Name: "opcache.memory_consumption", Value: "384"}}
	for _, v := range []string{"8.3", "8.4"} {
		if err := os.MkdirAll(filepath.Join(root, v, "jabali-ext", "alice"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, v, "jabali-ext", "alice", phpPoolOpcacheIniName), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := applyPoolOpcacheIni("8.4", "bob", startup); err != nil {
		t.Fatal(err)
	}
	removePoolOpcacheIni("alice")
	if m, _ := filepath.Glob(filepath.Join(root, "*", "jabali-ext", "alice", phpPoolOpcacheIniName)); len(m) != 0 {
		t.Errorf("left behind: %v", m)
	}
	if _, err := os.Stat(filepath.Join(root, "8.4", "jabali-ext", "bob", phpPoolOpcacheIniName)); err != nil {
		t.Errorf("another slug's ini was removed: %v", err)
	}
	removePoolOpcacheIni("../alice") // refused, no panic
}

// OPcache sizing values are written into an ini file, so only plain sizes,
// counts and JIT mode words are accepted.
func TestPoolApplyRejectsBadOpcacheStartupValue(t *testing.T) {
	for _, kv := range []KV{
		{Name: "opcache.memory_consumption", Value: "256 M"},
		{Name: "opcache.memory_consumption", Value: "1 ; x"},
		{Name: "opcache.max_accelerated_files", Value: "-1"},
		{Name: "opcache.jit_buffer_size", Value: "64MB"},
		{Name: "opcache.jit", Value: "tracing\"x"},
		{Name: "opcache.jit", Value: "${HOME}"},
	} {
		err := validatePoolIniOverrides([]KV{kv}, nil)
		if err == nil || err.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%s=%q: err = %v, want invalid_argument", kv.Name, kv.Value, err)
		}
	}
	ok := []KV{
		{Name: "opcache.memory_consumption", Value: "256"},
		{Name: "opcache.memory_consumption", Value: "256M"},
		{Name: "opcache.max_accelerated_files", Value: "100000"},
		{Name: "opcache.jit_buffer_size", Value: "128M"},
		{Name: "opcache.jit", Value: "tracing"},
		{Name: "opcache.jit", Value: "1255"},
		{Name: "opcache.jit", Value: "off"},
		{Name: "opcache.revalidate_freq", Value: "60"},
	}
	if err := validatePoolIniOverrides(ok, nil); err != nil {
		t.Fatalf("valid OPcache values refused: %v", err)
	}
}
