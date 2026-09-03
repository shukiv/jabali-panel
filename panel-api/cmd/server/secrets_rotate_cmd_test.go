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

func saveRotateSeams(t *testing.T) {
	t.Helper()
	sql, restart, probe := rotateRunSQL, rotateRestartService, rotateProbeDBAppUser
	health := rotateProbePanelHealthy
	t.Cleanup(func() {
		rotateRunSQL, rotateRestartService, rotateProbeDBAppUser = sql, restart, probe
		rotateProbePanelHealthy = health
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
