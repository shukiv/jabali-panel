package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// activeRulesFixture mirrors the shape of a rendered active.rules: the mode
// header, the flat exec bans (one in simulation form, as mode=simulation
// renders them), a filtered putenv rule and an eval blacklist naming the same
// functions.
const activeRulesFixture = `# Jabali Snuffleupagus active rules -- RENDERED, do not edit.
# mode=enforce rendered_at=2026-10-04T00:00:00Z

# --- 00-base.rules ---
sp.disable_function.function("system").drop();
sp.disable_function.function("exec").drop();
sp.disable_function.function("shell_exec").drop().simulation();
sp.disable_function.function("passthru").drop();
sp.disable_function.function("phpinfo").drop();
sp.disable_function.function("putenv").param("assignment").value_r("LD_").drop();
sp.eval_blacklist.list("system,exec,shell_exec,passthru,proc_open,popen,pcntl_exec,assert,eval");
`

func usePHPDefenseTempPaths(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldActive, oldPools, oldEtc := phpDefenseActiveRulesPath, phpDefensePoolRulesDir, phpEtcRoot
	phpDefenseActiveRulesPath = filepath.Join(root, "snuffleupagus", "active.rules")
	phpDefensePoolRulesDir = filepath.Join(root, "snuffleupagus", "pools")
	phpEtcRoot = filepath.Join(root, "php")
	t.Cleanup(func() {
		phpDefenseActiveRulesPath, phpDefensePoolRulesDir, phpEtcRoot = oldActive, oldPools, oldEtc
	})
	if err := os.MkdirAll(filepath.Dir(phpDefenseActiveRulesPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(phpDefenseActiveRulesPath, []byte(activeRulesFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func activeLines(rules string) map[string]bool {
	out := map[string]bool{}
	for _, l := range strings.Split(rules, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			out[l] = true
		}
	}
	return out
}

// Only the flat bans on the allowed functions go, in both the enforce and the
// simulation form. A ban kept on the list, the filtered putenv rule and the
// eval blacklist all stay.
func TestRenderPoolPHPDefenseRules(t *testing.T) {
	got := string(renderPoolPHPDefenseRules([]byte(activeRulesFixture), []string{"shell_exec", "system"}))
	live := activeLines(got)
	for _, gone := range []string{
		`sp.disable_function.function("system").drop();`,
		`sp.disable_function.function("shell_exec").drop().simulation();`,
	} {
		if live[gone] {
			t.Errorf("allowed ban still active: %s", gone)
		}
		if !strings.Contains(got, "# allowed by the hosting package: "+gone) {
			t.Errorf("lifted ban not kept as a comment: %s", gone)
		}
	}
	for _, kept := range []string{
		`sp.disable_function.function("exec").drop();`,
		`sp.disable_function.function("passthru").drop();`,
		`sp.disable_function.function("phpinfo").drop();`,
		`sp.disable_function.function("putenv").param("assignment").value_r("LD_").drop();`,
		`sp.eval_blacklist.list("system,exec,shell_exec,passthru,proc_open,popen,pcntl_exec,assert,eval");`,
	} {
		if !live[kept] {
			t.Errorf("rule lost from the pool copy: %s", kept)
		}
	}
	if !strings.Contains(got, phpDefenseAllowHeader+"shell_exec,system\n") {
		t.Errorf("allow header missing:\n%s", got)
	}
}

// A function outside the liftable set is never removed, even if asked.
func TestRenderPoolPHPDefenseRulesIgnoresNonExecFunctions(t *testing.T) {
	got := string(renderPoolPHPDefenseRules([]byte(activeRulesFixture), []string{"phpinfo"}))
	if !activeLines(got)[`sp.disable_function.function("phpinfo").drop();`] {
		t.Fatal("phpinfo ban was lifted; only the exec family may be")
	}
}

func TestPHPDefenseAllowFromDisabled(t *testing.T) {
	if got := phpDefenseAllowFromDisabled(defaultDisableFunctions); len(got) != 0 {
		t.Errorf("the lockdown lifts nothing, got %v", got)
	}
	if got := strings.Join(phpDefenseAllowFromDisabled(""), ","); got != "exec,passthru,pcntl_exec,popen,proc_open,shell_exec,system" {
		t.Errorf("nothing disabled lifts all seven, got %s", got)
	}
	if got := strings.Join(phpDefenseAllowFromDisabled("exec,passthru,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl,mail"), ","); got != "shell_exec" {
		t.Errorf("only shell_exec allowed, got %s", got)
	}
}

func TestValidateDisableFunctions(t *testing.T) {
	for _, ok := range []string{"", defaultDisableFunctions, "mail", "exec,mail,curl_exec"} {
		if err := validateDisableFunctions(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"exec, mail", "Exec", "exec,,mail", "exec;id", "exec\nuser = root", ",exec", "exec,"} {
		if err := validateDisableFunctions(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestApplyPoolPHPDefense(t *testing.T) {
	usePHPDefenseTempPaths(t)
	ini := phpDefensePoolIniPath("8.4", "alice")
	rules := phpDefensePoolRulesPath("alice")

	changed, err := applyPoolPHPDefense("8.4", "alice", []string{"shell_exec"})
	if err != nil || !changed {
		t.Fatalf("first apply: changed=%v err=%v", changed, err)
	}
	iniBody, err := os.ReadFile(ini)
	if err != nil {
		t.Fatalf("ini not written: %v", err)
	}
	if !strings.Contains(string(iniBody), "\nsp.configuration_file="+rules+"\n") {
		t.Errorf("ini does not point at the pool copy:\n%s", iniBody)
	}
	copyBody, _ := os.ReadFile(rules)
	if activeLines(string(copyBody))[`sp.disable_function.function("shell_exec").drop().simulation();`] {
		t.Error("pool copy still bans shell_exec")
	}

	// Same input again: nothing to write.
	if changed, err = applyPoolPHPDefense("8.4", "alice", []string{"shell_exec"}); err != nil || changed {
		t.Fatalf("idempotent apply: changed=%v err=%v", changed, err)
	}

	// The pool moved to 8.3: the 8.4 ini goes, the 8.3 one appears.
	if _, err = applyPoolPHPDefense("8.3", "alice", []string{"shell_exec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ini); !os.IsNotExist(err) {
		t.Error("ini left in the old version's scan dir")
	}
	if _, err := os.Stat(phpDefensePoolIniPath("8.3", "alice")); err != nil {
		t.Errorf("ini missing in the new version's scan dir: %v", err)
	}

	// Back to the lockdown: both files go, so the master loads active.rules.
	if changed, err = applyPoolPHPDefense("8.3", "alice", nil); err != nil || !changed {
		t.Fatalf("lockdown apply: changed=%v err=%v", changed, err)
	}
	for _, p := range []string{phpDefensePoolIniPath("8.3", "alice"), rules} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind", p)
		}
	}
}

// PHP Defense not installed: nothing to lift, nothing written.
func TestApplyPoolPHPDefenseWithoutActiveRules(t *testing.T) {
	usePHPDefenseTempPaths(t)
	if err := os.Remove(phpDefenseActiveRulesPath); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPoolPHPDefense("8.4", "alice", []string{"exec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(phpDefensePoolIniPath("8.4", "alice")); !os.IsNotExist(err) {
		t.Error("ini written without PHP Defense rules to copy")
	}
}

func TestRegeneratePoolPHPDefenseRules(t *testing.T) {
	usePHPDefenseTempPaths(t)
	// alice's pool exists; bob's was deleted while his copy stayed.
	conf := filepath.Join(phpEtcRoot, "8.4", "fpm", "pool.d", "jabali-alice.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("[jabali-alice]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPoolPHPDefense("8.4", "alice", []string{"shell_exec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPoolPHPDefense("8.4", "bob", []string{"exec"}); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(phpDefensePoolRulesDir, "carol.rules")
	if err := os.WriteFile(foreign, []byte("# hand-made\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	carolConf := filepath.Join(phpEtcRoot, "8.4", "fpm", "pool.d", "jabali-carol.conf")
	if err := os.WriteFile(carolConf, []byte("[jabali-carol]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The operator flips PHP Defense to simulation: active.rules changes.
	next := strings.ReplaceAll(activeRulesFixture, `.drop();`, `.drop().simulation();`)
	next = strings.Replace(next, "mode=enforce", "mode=simulation", 1)
	if err := os.WriteFile(phpDefenseActiveRulesPath, []byte(next), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := regeneratePoolPHPDefenseRules(); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(phpDefensePoolRulesPath("alice"))
	if !strings.Contains(string(got), "# mode=simulation") {
		t.Error("alice's copy was not rebuilt from the new active.rules")
	}
	if !activeLines(string(got))[`sp.disable_function.function("exec").drop().simulation();`] {
		t.Error("alice's rebuilt copy lost the exec ban it keeps")
	}
	if activeLines(string(got))[`sp.disable_function.function("shell_exec").drop().simulation();`] {
		t.Error("alice's rebuilt copy bans shell_exec again")
	}
	for _, p := range []string{phpDefensePoolRulesPath("bob"), phpDefensePoolIniPath("8.4", "bob")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("orphan %s not removed", p)
		}
	}
	if b, _ := os.ReadFile(foreign); string(b) != "# hand-made\n" {
		t.Error("a copy without the agent's header was rewritten")
	}
}

func TestRemovePoolPHPDefense(t *testing.T) {
	usePHPDefenseTempPaths(t)
	if _, err := applyPoolPHPDefense("8.4", "alice-php8.3", []string{"exec"}); err != nil {
		t.Fatal(err)
	}
	removePoolPHPDefense("alice-php8.3")
	for _, p := range []string{phpDefensePoolRulesPath("alice-php8.3"), phpDefensePoolIniPath("8.4", "alice-php8.3")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind", p)
		}
	}
	removePoolPHPDefense("../etc") // refused, no panic
}

// The wiring is order-sensitive: the name check must run on every value, the
// pool's PHP Defense files must be in place before its reload, and a PHP
// Defense rules apply must rebuild the pool copies before it reloads. Pool
// remove and the reaper must drop the copies.
func TestPHPDefensePoolWiringSourceContract(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	apply := read("php_pool_apply.go")
	iValidate := strings.Index(apply, "validateDisableFunctions(disableFunctions)")
	iPD := strings.Index(apply, "applyPoolPHPDefense(p.PHPVersion, slug, phpDefenseAllowFromDisabled(disableFunctions))")
	iReload := strings.Index(apply, "restartOrReloadUserFPM(ctx, slug, oldVersion, p.PHPVersion)")
	if iValidate < 0 || iPD < 0 || iReload < 0 || iPD > iReload {
		t.Fatalf("pool apply wiring: validate=%d php-defense=%d reload=%d", iValidate, iPD, iReload)
	}

	sp := read("security_snuffleupagus.go")
	iRename := strings.Index(sp, "os.Rename(tmpName, snuffleupagusActiveRulesPath)")
	iRegen := strings.Index(sp, "regeneratePoolPHPDefenseRules()")
	iSPReload := strings.Index(sp, "reload, reloadErr := snuffleupagusReloadHandler(ctx, nil)")
	if iRename < 0 || iRegen < iRename || iSPReload < iRegen {
		t.Fatalf("rules apply wiring: rename=%d regenerate=%d reload=%d", iRename, iRegen, iSPReload)
	}

	for _, f := range []string{"php_pool_remove.go", "php_fpm_reap.go"} {
		if !strings.Contains(read(f), "removePoolPHPDefense(slug)") {
			t.Errorf("%s does not remove the pool's PHP Defense rules", f)
		}
	}
}
