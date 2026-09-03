// JAB-357 crit-7 — the db-app-user rotation flow, proven with no box: seams
// stub the privileged steps (SQL, restart, probe) so we can assert ordering,
// file effects, and probe-failure rollback deterministically.
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testOldPw      = "OLDPW"
	testEnvContent = "PANEL_ADDR=127.0.0.1:8443\n" +
		"PANEL_ENV=production\n" +
		"JWT_SECRET=jwtval\n" +
		"DATABASE_URL=jabali_panel_app:OLDPW@unix(/var/run/mysqld/mysqld.sock)/jabali_panel?parseTime=true&charset=utf8mb4&loc=UTC\n" +
		"JABALI_REDIS_PANEL_TOKEN=tok\n"
)

func setupRotateFixture(t *testing.T) (envPath, pwPath string) {
	t.Helper()
	dir := t.TempDir()
	envPath = filepath.Join(dir, "panel.env")
	pwPath = filepath.Join(dir, "db-password")
	if err := os.WriteFile(envPath, []byte(testEnvContent), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pwPath, []byte(testOldPw+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(envPath, 0o640)
	_ = os.Chmod(pwPath, 0o640)
	t.Setenv("JABALI_PANEL_ENV_FILE", envPath)
	t.Setenv("JABALI_DB_PASSWORD_FILE", pwPath)
	return
}

const testACLContent = "user default off\n" +
	"user jabali_panel on >tok ~jabali:* ~automation:* resetchannels +@all -@dangerous +acl +@connection\n" +
	"user wp_alice on >alicepw ~wp:alice:* +@read\n"

// setupRedisFixture adds an aclfile fixture alongside the panel.env one and
// points the resolver at it. Call after setupRotateFixture.
func setupRedisFixture(t *testing.T) (aclPath string) {
	t.Helper()
	aclPath = filepath.Join(t.TempDir(), "users.acl")
	if err := os.WriteFile(aclPath, []byte(testACLContent), 0o640); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(aclPath, 0o640)
	t.Setenv("JABALI_REDIS_ACL_FILE", aclPath)
	return
}

func saveRotateSeams(t *testing.T) {
	t.Helper()
	sql, restart, probe := rotateRunSQL, rotateRestartService, rotateProbeDBAppUser
	health, redis, pdns := rotateProbePanelHealthy, rotateRedisSetPassword, rotateProbePdnsHealthy
	t.Cleanup(func() {
		rotateRunSQL, rotateRestartService, rotateProbeDBAppUser = sql, restart, probe
		rotateProbePanelHealthy, rotateRedisSetPassword, rotateProbePdnsHealthy = health, redis, pdns
	})
}

func TestRotateDBAppUser_HappyPath(t *testing.T) {
	saveRotateSeams(t)
	envPath, pwPath := setupRotateFixture(t)

	var events []string
	rotateRunSQL = func(_ context.Context, sql string) error {
		if strings.HasPrefix(sql, "ALTER USER") {
			// Order guard: db-password must still hold the OLD value when
			// ALTER runs — the credential is applied before config points at it.
			b, _ := os.ReadFile(pwPath)
			if strings.TrimSpace(string(b)) != testOldPw {
				t.Errorf("db-password rewritten before ALTER USER: %q", b)
			}
		}
		events = append(events, "sql")
		return nil
	}
	rotateRestartService = func(_ context.Context, unit string) error {
		events = append(events, "restart:"+unit)
		return nil
	}
	rotateProbePanelHealthy = func(_ context.Context) error { return nil }
	rotateProbeDBAppUser = func(_ context.Context, _ string) error {
		events = append(events, "probe")
		return nil
	}

	var out bytes.Buffer
	if err := rotateDBAppUser(context.Background(), &out, false); err != nil {
		t.Fatal(err)
	}

	pw, _ := os.ReadFile(pwPath)
	newPw := strings.TrimSpace(string(pw))
	if newPw == testOldPw || newPw == "" {
		t.Fatalf("db-password not rotated: %q", newPw)
	}
	env, _ := os.ReadFile(envPath)
	if !strings.Contains(string(env), "jabali_panel_app:"+newPw+"@unix(") {
		t.Errorf("DATABASE_URL not updated to new pw:\n%s", env)
	}
	for _, keep := range []string{"JWT_SECRET=jwtval", "JABALI_REDIS_PANEL_TOKEN=tok", "PANEL_ADDR=127.0.0.1:8443"} {
		if !strings.Contains(string(env), keep) {
			t.Errorf("dropped sibling %q from panel.env", keep)
		}
	}
	if fi, _ := os.Stat(envPath); fi.Mode().Perm() != 0o640 {
		t.Errorf("panel.env mode = %o, want 640", fi.Mode().Perm())
	}
	if _, err := os.Stat(pwPath + bakSuffix); !os.IsNotExist(err) {
		t.Error("db-password .bak not purged after success")
	}
	if _, err := os.Stat(envPath + bakSuffix); !os.IsNotExist(err) {
		t.Error("panel.env .bak not purged after success")
	}
	if got := strings.Join(events, ","); got != "sql,restart:jabali-panel,probe" {
		t.Errorf("event order = %q, want sql,restart:jabali-panel,probe", got)
	}
}

func TestRotateDBAppUser_ProbeFailureRollsBack(t *testing.T) {
	saveRotateSeams(t)
	envPath, pwPath := setupRotateFixture(t)

	var sqls []string
	rotateRunSQL = func(_ context.Context, sql string) error { sqls = append(sqls, sql); return nil }
	restarts := 0
	rotateRestartService = func(_ context.Context, _ string) error { restarts++; return nil }
	rotateProbePanelHealthy = func(_ context.Context) error { return nil }
	rotateProbeDBAppUser = func(_ context.Context, _ string) error { return errors.New("cannot authenticate") }

	var out bytes.Buffer
	if err := rotateDBAppUser(context.Background(), &out, false); err == nil {
		t.Fatal("expected error when the health probe fails")
	}

	pw, _ := os.ReadFile(pwPath)
	if strings.TrimSpace(string(pw)) != testOldPw {
		t.Errorf("db-password not restored to old value: %q", pw)
	}
	env, _ := os.ReadFile(envPath)
	if !strings.Contains(string(env), "jabali_panel_app:"+testOldPw+"@unix(") {
		t.Errorf("DATABASE_URL not restored:\n%s", env)
	}
	// Two ALTERs: forward (new pw), then rollback (old pw).
	if len(sqls) != 2 {
		t.Fatalf("expected 2 ALTER statements, got %d: %v", len(sqls), sqls)
	}
	if !strings.Contains(sqls[1], "'"+testOldPw+"'") {
		t.Errorf("rollback ALTER did not restore old password: %q", sqls[1])
	}
	if restarts != 2 { // forward restart + rollback restart
		t.Errorf("restarts = %d, want 2", restarts)
	}
	if _, err := os.Stat(pwPath + bakSuffix); !os.IsNotExist(err) {
		t.Error(".bak lingering after rollback")
	}
}

func TestRotateJWT_HappyPathAndSiblingsPreserved(t *testing.T) {
	saveRotateSeams(t)
	envPath, _ := setupRotateFixture(t)

	restarts := 0
	rotateRestartService = func(_ context.Context, _ string) error { restarts++; return nil }
	rotateProbePanelHealthy = func(_ context.Context) error { return nil }

	var out bytes.Buffer
	if err := rotateSingleEnvKey(context.Background(), &out, false, "JWT_SECRET", "secrets.rotate.jwt"); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(envPath)
	if strings.Contains(string(env), "JWT_SECRET=jwtval") {
		t.Error("JWT_SECRET not rotated")
	}
	for _, keep := range []string{"DATABASE_URL=jabali_panel_app:OLDPW@unix(", "JABALI_REDIS_PANEL_TOKEN=tok", "PANEL_ADDR=127.0.0.1:8443"} {
		if !strings.Contains(string(env), keep) {
			t.Errorf("dropped sibling %q", keep)
		}
	}
	if restarts != 1 {
		t.Errorf("restarts = %d, want 1", restarts)
	}
	if _, err := os.Stat(envPath + bakSuffix); !os.IsNotExist(err) {
		t.Error(".bak not purged")
	}
}

func TestRotateJWT_UnhealthyRollsBack(t *testing.T) {
	saveRotateSeams(t)
	envPath, _ := setupRotateFixture(t)

	rotateRestartService = func(_ context.Context, _ string) error { return nil }
	rotateProbePanelHealthy = func(_ context.Context) error { return errors.New("panel failed to start") }

	var out bytes.Buffer
	if err := rotateSingleEnvKey(context.Background(), &out, false, "JWT_SECRET", "secrets.rotate.jwt"); err == nil {
		t.Fatal("expected error when the panel is unhealthy")
	}
	env, _ := os.ReadFile(envPath)
	if !strings.Contains(string(env), "JWT_SECRET=jwtval") {
		t.Errorf("JWT_SECRET not restored:\n%s", env)
	}
	if _, err := os.Stat(envPath + bakSuffix); !os.IsNotExist(err) {
		t.Error(".bak lingering after rollback")
	}
}

func TestRotateRedisPanelToken_HappyPath(t *testing.T) {
	saveRotateSeams(t)
	envPath, _ := setupRotateFixture(t)
	aclPath := setupRedisFixture(t)

	var events []string
	var redisCalls [][2]string // (authToken, newToken) per call
	rotateRedisSetPassword = func(_ context.Context, auth, newTok string) error {
		redisCalls = append(redisCalls, [2]string{auth, newTok})
		events = append(events, "redis")
		return nil
	}
	rotateRestartService = func(_ context.Context, _ string) error { events = append(events, "restart"); return nil }
	rotateProbePanelHealthy = func(_ context.Context) error { events = append(events, "probe"); return nil }

	var out bytes.Buffer
	if err := rotateRedisPanelToken(context.Background(), &out, false); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(envPath)
	if strings.Contains(string(env), "JABALI_REDIS_PANEL_TOKEN=tok\n") {
		t.Error("token not rotated in panel.env")
	}
	newTok := redisCalls[0][1]
	acl, _ := os.ReadFile(aclPath)
	if !strings.Contains(string(acl), "user jabali_panel on >"+newTok+" ") {
		t.Errorf("aclfile token not updated:\n%s", acl)
	}
	for _, keep := range []string{"user default off", "user wp_alice on >alicepw"} {
		if !strings.Contains(string(acl), keep) {
			t.Errorf("aclfile dropped %q", keep)
		}
	}
	// Live change authed with OLD token, set NEW.
	if redisCalls[0][0] != "tok" {
		t.Errorf("forward redis auth = %q, want old token 'tok'", redisCalls[0][0])
	}
	// order: redis(live) -> restart -> probe
	if got := strings.Join(events, ","); got != "redis,restart,probe" {
		t.Errorf("event order = %q, want redis,restart,probe", got)
	}
	if _, err := os.Stat(aclPath + bakSuffix); !os.IsNotExist(err) {
		t.Error("acl .bak not purged")
	}
}

func TestRotateRedisPanelToken_UnhealthyRollsBackLiveAndFiles(t *testing.T) {
	saveRotateSeams(t)
	envPath, _ := setupRotateFixture(t)
	aclPath := setupRedisFixture(t)

	var redisCalls [][2]string
	rotateRedisSetPassword = func(_ context.Context, auth, newTok string) error {
		redisCalls = append(redisCalls, [2]string{auth, newTok})
		return nil
	}
	rotateRestartService = func(_ context.Context, _ string) error { return nil }
	rotateProbePanelHealthy = func(_ context.Context) error { return errors.New("panel down") }

	var out bytes.Buffer
	if err := rotateRedisPanelToken(context.Background(), &out, false); err == nil {
		t.Fatal("expected error on unhealthy panel")
	}
	// Files restored to the old token.
	env, _ := os.ReadFile(envPath)
	if !strings.Contains(string(env), "JABALI_REDIS_PANEL_TOKEN=tok") {
		t.Errorf("panel.env token not restored:\n%s", env)
	}
	acl, _ := os.ReadFile(aclPath)
	if !strings.Contains(string(acl), "user jabali_panel on >tok ") {
		t.Errorf("aclfile token not restored:\n%s", acl)
	}
	// Two live calls: forward (old->new) then rollback (new->old).
	if len(redisCalls) != 2 {
		t.Fatalf("expected 2 redis calls, got %d: %v", len(redisCalls), redisCalls)
	}
	fwdNew := redisCalls[0][1]
	if redisCalls[1][0] != fwdNew || redisCalls[1][1] != "tok" {
		t.Errorf("rollback redis call = %v, want auth=%q set=tok", redisCalls[1], fwdNew)
	}
}

func TestRotateAll_HappyPathRunsEveryStepInOrder(t *testing.T) {
	saveRotateSeams(t)
	envPath, pwPath := setupRotateFixture(t)
	aclPath := setupRedisFixture(t)

	var order []string
	rotateRunSQL = func(_ context.Context, _ string) error { order = append(order, "sql"); return nil }
	rotateRedisSetPassword = func(_ context.Context, _, _ string) error { order = append(order, "redis"); return nil }
	rotateRestartService = func(_ context.Context, _ string) error { return nil }
	rotateProbeDBAppUser = func(_ context.Context, _ string) error { return nil }
	rotateProbePanelHealthy = func(_ context.Context) error { return nil }

	var out bytes.Buffer
	if err := rotateAll(context.Background(), &out, false); err != nil {
		t.Fatal(err)
	}
	// db-app-user (sql) before redis-panel-token (redis).
	if got := strings.Join(order, ","); got != "sql,redis" {
		t.Errorf("privileged-step order = %q, want sql,redis", got)
	}
	for _, banner := range []string{"== db-app-user ==", "== redis-panel-token ==", "== jwt =="} {
		if !strings.Contains(out.String(), banner) {
			t.Errorf("missing step banner %q", banner)
		}
	}
	// Every secret actually rotated.
	pw, _ := os.ReadFile(pwPath)
	if strings.TrimSpace(string(pw)) == testOldPw {
		t.Error("db-app-user not rotated")
	}
	acl, _ := os.ReadFile(aclPath)
	if strings.Contains(string(acl), ">tok ") {
		t.Error("redis token not rotated")
	}
	env, _ := os.ReadFile(envPath)
	if strings.Contains(string(env), "JWT_SECRET=jwtval") {
		t.Error("jwt not rotated")
	}
}

func TestRotateAll_StopsAtFirstFailure(t *testing.T) {
	saveRotateSeams(t)
	setupRotateFixture(t)
	setupRedisFixture(t)

	rotateRunSQL = func(_ context.Context, _ string) error { return errors.New("db unreachable") }
	redisCalled := false
	rotateRedisSetPassword = func(_ context.Context, _, _ string) error { redisCalled = true; return nil }
	rotateRestartService = func(_ context.Context, _ string) error { return nil }
	rotateProbeDBAppUser = func(_ context.Context, _ string) error { return nil }
	rotateProbePanelHealthy = func(_ context.Context) error { return nil }

	var out bytes.Buffer
	err := rotateAll(context.Background(), &out, false)
	if err == nil || !strings.Contains(err.Error(), "db-app-user") {
		t.Fatalf("expected failure naming db-app-user, got %v", err)
	}
	if redisCalled {
		t.Error("orchestrator continued past a failed step")
	}
}

func TestRotateRedisPanelToken_FailedLiveRevertKeepsFilesAtNewToken(t *testing.T) {
	saveRotateSeams(t)
	envPath, _ := setupRotateFixture(t)
	aclPath := setupRedisFixture(t)

	call := 0
	var newTok string
	rotateRedisSetPassword = func(_ context.Context, _, setTok string) error {
		call++
		if call == 1 { // forward old->new succeeds
			newTok = setTok
			return nil
		}
		return errors.New("redis unreachable during revert") // revert fails
	}
	rotateRestartService = func(_ context.Context, _ string) error { return nil }
	rotateProbePanelHealthy = func(_ context.Context) error { return errors.New("panel down") } // force rollback

	var out bytes.Buffer
	err := rotateRedisPanelToken(context.Background(), &out, false)
	if err == nil || !strings.Contains(err.Error(), "ROLLBACK INCOMPLETE") {
		t.Fatalf("expected a loud ROLLBACK INCOMPLETE error, got %v", err)
	}
	// Files MUST be left at the new token (matching live Redis, which only
	// accepts new), NOT restored to the old value.
	env, _ := os.ReadFile(envPath)
	if !strings.Contains(string(env), "JABALI_REDIS_PANEL_TOKEN="+newTok) {
		t.Errorf("panel.env not left at new token:\n%s", env)
	}
	if strings.Contains(string(env), "JABALI_REDIS_PANEL_TOKEN=tok\n") {
		t.Error("panel.env wrongly restored to old token while Redis accepts only the new one")
	}
	acl, _ := os.ReadFile(aclPath)
	if !strings.Contains(string(acl), ">"+newTok+" ") {
		t.Errorf("aclfile not left at new token:\n%s", acl)
	}
}

const testPdnsEnv = "PDNS_DB_NAME=jabali_pdns\nPDNS_DB_USER=jabali_pdns\nPDNS_DB_PASSWORD=oldpdnspw\n"
const testPdnsConf = "launch=gmysql\ngmysql-host=localhost\ngmysql-user=jabali_pdns\ngmysql-password=oldpdnspw\ngmysql-dbname=jabali_pdns\n"

func setupPdnsFixture(t *testing.T) (envPath, confPath string) {
	t.Helper()
	dir := t.TempDir()
	envPath = filepath.Join(dir, "pdns.env")
	confPath = filepath.Join(dir, "01-jabali-mysql.conf")
	if err := os.WriteFile(envPath, []byte(testPdnsEnv), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(confPath, []byte(testPdnsConf), 0o640); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(envPath, 0o640)
	_ = os.Chmod(confPath, 0o640)
	t.Setenv("JABALI_PDNS_ENV_FILE", envPath)
	t.Setenv("JABALI_PDNS_BACKEND_CONF", confPath)
	return
}

func TestRotatePdns_HappyPath(t *testing.T) {
	saveRotateSeams(t)
	envPath, confPath := setupPdnsFixture(t)

	var order []string
	rotateRunSQL = func(_ context.Context, sql string) error {
		if strings.HasPrefix(sql, "ALTER USER") {
			b, _ := os.ReadFile(envPath) // still old at ALTER time
			if !strings.Contains(string(b), "PDNS_DB_PASSWORD=oldpdnspw") {
				t.Errorf("pdns.env rewritten before ALTER: %q", b)
			}
		}
		order = append(order, "sql")
		return nil
	}
	rotateRestartService = func(_ context.Context, unit string) error { order = append(order, "restart:"+unit); return nil }
	rotateProbePdnsHealthy = func(_ context.Context, _ string) error { order = append(order, "probe"); return nil }

	var out bytes.Buffer
	if err := rotatePdns(context.Background(), &out, false); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(envPath)
	conf, _ := os.ReadFile(confPath)
	if strings.Contains(string(env), "oldpdnspw") || strings.Contains(string(conf), "oldpdnspw") {
		t.Error("old pdns password still present")
	}
	// Both files carry the SAME new password.
	newPwEnv, _ := envGet(string(env), "PDNS_DB_PASSWORD")
	newPwConf, _ := envGet(string(conf), "gmysql-password")
	if newPwEnv == "" || newPwEnv != newPwConf {
		t.Errorf("pdns.env pw %q != conf pw %q", newPwEnv, newPwConf)
	}
	// Sibling conf lines preserved.
	if !strings.Contains(string(conf), "launch=gmysql") || !strings.Contains(string(conf), "gmysql-dbname=jabali_pdns") {
		t.Errorf("conf siblings dropped:\n%s", conf)
	}
	if got := strings.Join(order, ","); got != "sql,restart:pdns,probe" {
		t.Errorf("order = %q, want sql,restart:pdns,probe", got)
	}
	if _, err := os.Stat(envPath + bakSuffix); !os.IsNotExist(err) {
		t.Error("pdns.env .bak not purged")
	}
}

func TestRotatePdns_UnhealthyRollsBack(t *testing.T) {
	saveRotateSeams(t)
	envPath, confPath := setupPdnsFixture(t)

	var sqls []string
	rotateRunSQL = func(_ context.Context, sql string) error { sqls = append(sqls, sql); return nil }
	rotateRestartService = func(_ context.Context, _ string) error { return nil }
	rotateProbePdnsHealthy = func(_ context.Context, _ string) error { return errors.New("pdns down") }

	var out bytes.Buffer
	if err := rotatePdns(context.Background(), &out, false); err == nil {
		t.Fatal("expected error when pdns is unhealthy")
	}
	env, _ := os.ReadFile(envPath)
	conf, _ := os.ReadFile(confPath)
	if !strings.Contains(string(env), "PDNS_DB_PASSWORD=oldpdnspw") || !strings.Contains(string(conf), "gmysql-password=oldpdnspw") {
		t.Errorf("files not restored:\nenv=%s\nconf=%s", env, conf)
	}
	if len(sqls) != 2 || !strings.Contains(sqls[1], "'oldpdnspw'") {
		t.Errorf("rollback ALTER did not restore old pw: %v", sqls)
	}
}

func TestRotatePdns_NotProvisioned(t *testing.T) {
	saveRotateSeams(t)
	t.Setenv("JABALI_PDNS_ENV_FILE", filepath.Join(t.TempDir(), "absent-pdns.env"))
	t.Setenv("JABALI_PDNS_BACKEND_CONF", filepath.Join(t.TempDir(), "absent.conf"))
	var out bytes.Buffer
	err := rotatePdns(context.Background(), &out, false)
	if err == nil || !strings.Contains(err.Error(), "not provisioned") {
		t.Fatalf("expected a clean not-provisioned error, got %v", err)
	}
}

func TestRotateDBAppUser_DryRunTouchesNothing(t *testing.T) {
	saveRotateSeams(t)
	envPath, pwPath := setupRotateFixture(t)

	called := false
	rotateRunSQL = func(_ context.Context, _ string) error { called = true; return nil }
	rotateRestartService = func(_ context.Context, _ string) error { called = true; return nil }
	rotateProbeDBAppUser = func(_ context.Context, _ string) error { called = true; return nil }

	envBefore, _ := os.ReadFile(envPath)
	pwBefore, _ := os.ReadFile(pwPath)

	var out bytes.Buffer
	if err := rotateDBAppUser(context.Background(), &out, true); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("dry-run invoked a privileged seam")
	}
	if !strings.Contains(out.String(), "DRY RUN") {
		t.Error("dry-run banner missing")
	}
	envAfter, _ := os.ReadFile(envPath)
	pwAfter, _ := os.ReadFile(pwPath)
	if !bytes.Equal(envBefore, envAfter) || !bytes.Equal(pwBefore, pwAfter) {
		t.Error("dry-run modified a file")
	}
}
