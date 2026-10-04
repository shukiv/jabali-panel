package models

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

func TestNormalizePHPDisabledFunctions(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		// lowercased, de-duplicated, lockdown first in lockdown order, rest sorted
		{"mail, SHELL_EXEC exec\tmail,curl_exec", "exec,shell_exec,curl_exec,mail"},
		{"dl,proc_nice,pcntl_fork,pcntl_exec,popen,proc_open,system,shell_exec,passthru,exec",
			"exec,passthru,shell_exec,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl"},
	} {
		got, err := NormalizePHPDisabledFunctions(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{
		"exec;id", "shell-exec", "1exec", "exec()", `exec"`, "exec\x00", "ünicode",
		strings.Repeat("a", 65),
	} {
		if _, err := NormalizePHPDisabledFunctions(bad); err == nil {
			t.Errorf("Normalize(%q) accepted an invalid name", bad)
		}
	}
	var many []string
	for i := 0; i <= maxPHPDisabledFunctions; i++ {
		many = append(many, "fn_"+strings.Repeat("x", i/26)+string(rune('a'+i%26)))
	}
	if _, err := NormalizePHPDisabledFunctions(strings.Join(many, ",")); err == nil {
		t.Errorf("Normalize accepted more than %d functions", maxPHPDisabledFunctions)
	}
}

func TestEffectivePHPDisabledFunctions(t *testing.T) {
	lock := strings.Join(PHPLockdownFunctions, ",")
	for _, tc := range []struct {
		name string
		pkg  *HostingPackage
		want string
	}{
		{"no package", nil, lock},
		{"default", &HostingPackage{}, lock},
		// A row written before the list existed (or by an older binary) keeps
		// its php_exec_enabled opt-out: no backfill needed.
		{"legacy opt-out row", &HostingPackage{PHPExecEnabled: true}, ""},
		{"explicit empty", &HostingPackage{PHPDisabledFunctions: strp("")}, ""},
		// An explicit list wins over a stale flag.
		{"list wins over flag", &HostingPackage{PHPExecEnabled: true, PHPDisabledFunctions: strp("exec,mail")}, "exec,mail"},
		{"hand-edited garbage fails closed", &HostingPackage{PHPDisabledFunctions: strp("exec;rm -rf")}, lock},
	} {
		got := strings.Join(EffectivePHPDisabledFunctions(tc.pkg), ",")
		if got != tc.want {
			t.Errorf("%s: effective = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSetPHPExecOnList(t *testing.T) {
	// Allowing exec keeps the admin's extra functions.
	got := SetPHPExecOnList([]string{"exec", "shell_exec", "mail"}, true)
	if got == nil || *got != "mail" {
		t.Fatalf("allow = %v, want mail", got)
	}
	// Forbidding adds the lockdown back, extras stay.
	got = SetPHPExecOnList([]string{"mail"}, false)
	if got == nil || *got != strings.Join(PHPLockdownFunctions, ",")+",mail" {
		t.Fatalf("forbid = %v", got)
	}
	// Forbidding with no extras is the default again (nil).
	if got = SetPHPExecOnList([]string{}, false); got != nil {
		t.Fatalf("forbid without extras = %q, want nil", *got)
	}
	if got = SetPHPExecOnList(PHPLockdownFunctions, true); got == nil || *got != "" {
		t.Fatalf("allow on the lockdown = %v, want empty", got)
	}
}

func TestSetPHPDisabledFunctionsDerivesExec(t *testing.T) {
	p := &HostingPackage{PHPExecEnabled: true}
	p.SetPHPDisabledFunctions(nil)
	if p.PHPExecEnabled || p.PHPDisabledFunctions != nil {
		t.Fatalf("nil list must be the lockdown with php_exec off: %+v", p)
	}
	p.SetPHPDisabledFunctions(strp(""))
	if !p.PHPExecEnabled {
		t.Fatal("an empty list allows exec")
	}
	// One lockdown function still disabled: php_exec reads false, which is
	// also what an older binary needs to keep the lockdown.
	p.SetPHPDisabledFunctions(strp("dl"))
	if p.PHPExecEnabled {
		t.Fatal("a list still holding dl must not report php_exec")
	}
	p.SetPHPDisabledFunctions(strp("mail"))
	if !p.PHPExecEnabled {
		t.Fatal("a list with no lockdown function allows exec")
	}
}

func TestPHPDisabledFunctionsField(t *testing.T) {
	if PHPDisabledFunctionsField(strings.Join(PHPLockdownFunctions, ",")) != nil {
		t.Fatal("exactly the lockdown must be stored as nil (follow the default)")
	}
	if f := PHPDisabledFunctionsField(""); f == nil || *f != "" {
		t.Fatal(`"" (nothing disabled) must be stored`)
	}
	if f := PHPDisabledFunctionsField("exec"); f == nil || *f != "exec" {
		t.Fatal("a custom list must be stored")
	}
}

func TestPoolDisableFunctions(t *testing.T) {
	join := func(s []string) string { return strings.Join(s, ",") }
	for _, tc := range []struct {
		name        string
		pkg         *HostingPackage
		wantDisable *string // nil = key omitted (agent default)
	}{
		{"no package", nil, nil},
		{"default", &HostingPackage{}, nil},
		{"legacy opt-out", &HostingPackage{PHPExecEnabled: true}, strp("")},
		{"only shell_exec allowed",
			&HostingPackage{PHPDisabledFunctions: strp("exec,passthru,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl")},
			strp("exec,passthru,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl")},
		{"lockdown plus extras", &HostingPackage{PHPDisabledFunctions: strp(join(PHPLockdownFunctions) + ",mail")},
			strp(join(PHPLockdownFunctions) + ",mail")},
	} {
		disable := PoolDisableFunctions(tc.pkg)
		if (disable == nil) != (tc.wantDisable == nil) || (disable != nil && *disable != *tc.wantDisable) {
			t.Errorf("%s: disable = %v, want %v", tc.name, deref(disable), deref(tc.wantDisable))
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// The panel's lockdown and the agent's default must be the same list, or a
// package "at the default" would render differently from the agent default.
func TestPHPLockdownMatchesAgentDefault(t *testing.T) {
	data, err := os.ReadFile("../../../panel-agent/internal/commands/php_pool_apply.go")
	if err != nil {
		t.Skipf("agent source not readable (%v) — skipping cross-boundary check", err)
	}
	m := regexp.MustCompile(`const defaultDisableFunctions = "([^"]*)"`).FindSubmatch(data)
	if m == nil {
		t.Fatal("agent defaultDisableFunctions not found")
	}
	if string(m[1]) != strings.Join(PHPLockdownFunctions, ",") {
		t.Fatalf("agent default %q != panel lockdown %q", m[1], strings.Join(PHPLockdownFunctions, ","))
	}
	pd, err := os.ReadFile("../../../panel-agent/internal/commands/php_defense_pool.go")
	if err != nil {
		t.Fatalf("agent php_defense_pool.go not readable: %v", err)
	}
	d := regexp.MustCompile(`var phpDefenseExecFunctions = map\[string\]bool\{([^}]*)\}`).FindSubmatch(pd)
	if d == nil {
		t.Fatal("agent phpDefenseExecFunctions not found")
	}
	for _, f := range PHPDefenseExecFunctions {
		if !strings.Contains(string(d[1]), `"`+f+`"`) {
			t.Errorf("agent phpDefenseExecFunctions lacks %q", f)
		}
	}
}

// panel-ui renders the package editor's lockdown checkboxes from its own copy.
func TestPHPLockdownTSInSync(t *testing.T) {
	data, err := os.ReadFile("../../../panel-ui/src/components/packages/phpDisabledFunctions.ts")
	if err != nil {
		t.Skipf("panel-ui list not readable (%v) — skipping cross-boundary check", err)
	}
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"PHP_LOCKDOWN_FUNCTIONS", PHPLockdownFunctions},
		{"PHP_DEFENSE_EXEC_FUNCTIONS", PHPDefenseExecFunctions},
	} {
		content := string(data)
		start := strings.Index(content, "export const "+tc.name+" =")
		if start < 0 {
			t.Fatalf("phpDisabledFunctions.ts does not define %s", tc.name)
		}
		end := strings.Index(content[start:], "]")
		var got []string
		for _, f := range strings.FieldsFunc(content[start:start+end], func(r rune) bool { return r == ',' || r == '\n' || r == '[' }) {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, `"`) {
				got = append(got, strings.Trim(f, `"`))
			}
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("panel-ui %s = %v, want %v", tc.name, got, tc.want)
		}
	}
}
