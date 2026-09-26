// JAB-357 crit-7 — the Kratos rotation flow, proven with no box: seams stub
// the privileged steps (SQL, restart, probe, session revocation) so ordering,
// file effects and rollback are asserted deterministically.
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
	kOldPw      = "0ld0db0pw0000000000000000000000000000000000000000000000000000aa"
	kOldDefault = "0ld0default000000000000000000000000000000000000000000000000000bb"
	kOldCookie  = "0ld0cookie0000000000000000000000000000000000000000000000000000cc"
	kYML        = "dsn: \"mysql://jabali_kratos:" + kOldPw + "@unix(/var/run/mysqld/mysqld.sock)/jabali_kratos?parseTime=true\"\n" +
		"\n" +
		"serve:\n" +
		"  public:\n" +
		"    base_url: \"https://panel.example.com:8443/.ory/\"\n" +
		"\n" +
		"secrets:\n" +
		"  default:\n" +
		"    - \"" + kOldDefault + "\"\n" +
		"  cookie:\n" +
		"    - \"" + kOldCookie + "\"\n"
)

type kratosFixture struct {
	yml, pw, def, cookie string
}

func setupKratosFixture(t *testing.T) kratosFixture {
	t.Helper()
	dir := t.TempDir()
	secrets := filepath.Join(dir, "kratos-secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	f := kratosFixture{
		yml:    filepath.Join(dir, "kratos.yml"),
		pw:     filepath.Join(dir, "kratos-db-password"),
		def:    filepath.Join(secrets, "default"),
		cookie: filepath.Join(secrets, "cookie"),
	}
	for path, body := range map[string]string{
		f.yml: kYML, f.pw: kOldPw + "\n", f.def: kOldDefault + "\n", f.cookie: kOldCookie + "\n",
	} {
		mode := os.FileMode(0o600)
		if path == f.yml {
			mode = 0o640
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(path, mode)
	}
	t.Setenv("JABALI_KRATOS_CONFIG", f.yml)
	t.Setenv("JABALI_KRATOS_DB_PASSWORD_FILE", f.pw)
	t.Setenv("JABALI_KRATOS_SECRETS_DIR", secrets)
	return f
}

func saveKratosSeams(t *testing.T) {
	t.Helper()
	saveRotateSeams(t)
	probe, revoke := rotateProbeKratosHealthy, rotateRevokeAllKratosSessions
	t.Cleanup(func() { rotateProbeKratosHealthy, rotateRevokeAllKratosSessions = probe, revoke })
}

func readTrim(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestRotateKratos_HappyPath(t *testing.T) {
	saveKratosSeams(t)
	f := setupKratosFixture(t)

	var order []string
	rotateRunSQL = func(_ context.Context, sql string) error {
		// The DB credential changes before any file points at it.
		if got := readTrim(t, f.pw); got != kOldPw {
			t.Errorf("kratos-db-password rewritten before ALTER: %q", got)
		}
		if !strings.HasPrefix(sql, "ALTER USER 'jabali_kratos'@'localhost' IDENTIFIED BY '") {
			t.Errorf("unexpected SQL: %s", sql)
		}
		order = append(order, "sql")
		return nil
	}
	rotateRestartService = func(_ context.Context, unit string) error { order = append(order, "restart:"+unit); return nil }
	var probedPw string
	rotateProbeKratosHealthy = func(_ context.Context, pw string) error { probedPw = pw; order = append(order, "probe"); return nil }
	rotateRevokeAllKratosSessions = func(context.Context) (int, error) { order = append(order, "revoke"); return 3, nil }

	var out bytes.Buffer
	if err := rotateKratos(context.Background(), &out, false); err != nil {
		t.Fatal(err)
	}

	newPw, newDef, newCookie := readTrim(t, f.pw), readTrim(t, f.def), readTrim(t, f.cookie)
	for name, v := range map[string]string{"db password": newPw, "default": newDef, "cookie": newCookie} {
		if len(v) != 64 {
			t.Errorf("new %s is %d chars, want 64 hex (256 bits)", name, len(v))
		}
	}
	if newPw == kOldPw || newDef == kOldDefault || newCookie == kOldCookie {
		t.Error("a secret file still holds its old value")
	}
	if newDef == newCookie || newPw == newDef {
		t.Error("new secrets must be independent values")
	}
	yml := readTrim(t, f.yml)
	for _, old := range []string{kOldPw, kOldDefault, kOldCookie} {
		if strings.Contains(yml, old) {
			t.Errorf("kratos.yml still contains an old value %q — an exposed secret stays valid", old[:8])
		}
	}
	for _, want := range []string{"jabali_kratos:" + newPw + "@unix(", `- "` + newDef + `"`, `- "` + newCookie + `"`, "base_url: \"https://panel.example.com:8443/.ory/\""} {
		if !strings.Contains(yml, want) {
			t.Errorf("kratos.yml missing %q", want)
		}
	}
	if probedPw != newPw {
		t.Error("probe did not check the NEW database password")
	}
	if got := strings.Join(order, ","); got != "sql,restart:jabali-kratos,probe,revoke" {
		t.Errorf("order = %q, want sql,restart:jabali-kratos,probe,revoke", got)
	}
	if st, _ := os.Stat(f.yml); st.Mode().Perm() != 0o640 {
		t.Errorf("kratos.yml mode = %v, want 0640 preserved", st.Mode().Perm())
	}
	if st, _ := os.Stat(f.pw); st.Mode().Perm() != 0o600 {
		t.Errorf("kratos-db-password mode = %v, want 0600 preserved", st.Mode().Perm())
	}
	for _, p := range []string{f.yml, f.pw, f.def, f.cookie} {
		if _, err := os.Stat(p + bakSuffix); !os.IsNotExist(err) {
			t.Errorf("%s left behind — a fresh copy of the old secret", p+bakSuffix)
		}
	}
	if !strings.Contains(out.String(), "3") || !strings.Contains(strings.ToLower(out.String()), "sign in again") {
		t.Errorf("output should report revoked sessions and the forced sign-in:\n%s", out.String())
	}
}

func TestRotateKratos_UnhealthyRollsBack(t *testing.T) {
	saveKratosSeams(t)
	f := setupKratosFixture(t)

	var sqls, restarts []string
	rotateRunSQL = func(_ context.Context, sql string) error { sqls = append(sqls, sql); return nil }
	rotateRestartService = func(_ context.Context, unit string) error { restarts = append(restarts, unit); return nil }
	rotateProbeKratosHealthy = func(context.Context, string) error { return errors.New("kratos crash-looping") }
	revoked := false
	rotateRevokeAllKratosSessions = func(context.Context) (int, error) { revoked = true; return 0, nil }

	var out bytes.Buffer
	if err := rotateKratos(context.Background(), &out, false); err == nil {
		t.Fatal("expected an error when Kratos is unhealthy after rotation")
	}
	if readTrim(t, f.yml) != strings.TrimSpace(kYML) || readTrim(t, f.pw) != kOldPw ||
		readTrim(t, f.def) != kOldDefault || readTrim(t, f.cookie) != kOldCookie {
		t.Error("files not restored to their pre-rotation contents")
	}
	if len(sqls) != 2 || !strings.Contains(sqls[1], "'"+kOldPw+"'") {
		t.Errorf("rollback ALTER did not restore the old password: %d statements", len(sqls))
	}
	if len(restarts) != 2 {
		t.Errorf("Kratos must be restarted again onto the restored config, restarts=%v", restarts)
	}
	if revoked {
		t.Error("sessions revoked although the rotation was rolled back")
	}
	for _, p := range []string{f.yml, f.pw, f.def, f.cookie} {
		if _, err := os.Stat(p + bakSuffix); !os.IsNotExist(err) {
			t.Errorf("%s left behind after rollback", p+bakSuffix)
		}
	}
}

func TestRotateKratos_AlterFailureTouchesNoFile(t *testing.T) {
	saveKratosSeams(t)
	f := setupKratosFixture(t)

	rotateRunSQL = func(context.Context, string) error { return errors.New("access denied") }
	rotateRestartService = func(context.Context, string) error { t.Error("restart after failed ALTER"); return nil }

	var out bytes.Buffer
	if err := rotateKratos(context.Background(), &out, false); err == nil {
		t.Fatal("expected the ALTER failure to surface")
	}
	if readTrim(t, f.yml) != strings.TrimSpace(kYML) || readTrim(t, f.pw) != kOldPw {
		t.Error("files changed although the DB credential was never rotated")
	}
	for _, p := range []string{f.yml, f.pw, f.def, f.cookie} {
		if _, err := os.Stat(p + bakSuffix); !os.IsNotExist(err) {
			t.Errorf("%s left behind", p+bakSuffix)
		}
	}
}

// A kratos.yml that no longer matches the source files (hand edit, failed
// render) is refused before anything changes: a blind rewrite could leave
// Kratos on a DSN that does not match the database.
func TestRotateKratos_ConfigDriftRefused(t *testing.T) {
	saveKratosSeams(t)
	f := setupKratosFixture(t)
	if err := os.WriteFile(f.yml, []byte(strings.Replace(kYML, kOldPw, "somethingelse", 1)), 0o640); err != nil {
		t.Fatal(err)
	}
	rotateRunSQL = func(context.Context, string) error { t.Error("SQL ran on a drifted config"); return nil }

	var out bytes.Buffer
	err := rotateKratos(context.Background(), &out, false)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected a drift refusal, got %v", err)
	}
	if readTrim(t, f.pw) != kOldPw {
		t.Error("password file changed on refusal")
	}
}

// Rotated secrets stay rotated when revocation fails: rolling back would put
// the exposed values back. The command still fails loudly so the operator
// revokes sessions by hand.
func TestRotateKratos_RevokeFailureKeepsRotation(t *testing.T) {
	saveKratosSeams(t)
	f := setupKratosFixture(t)

	var sqls []string
	rotateRunSQL = func(_ context.Context, sql string) error { sqls = append(sqls, sql); return nil }
	rotateRestartService = func(context.Context, string) error { return nil }
	rotateProbeKratosHealthy = func(context.Context, string) error { return nil }
	rotateRevokeAllKratosSessions = func(context.Context) (int, error) { return 1, errors.New("admin socket refused") }

	var out bytes.Buffer
	err := rotateKratos(context.Background(), &out, false)
	if err == nil || !strings.Contains(err.Error(), "jabali session") {
		t.Fatalf("expected a revocation error pointing at `jabali session`, got %v", err)
	}
	if readTrim(t, f.pw) == kOldPw || strings.Contains(readTrim(t, f.yml), kOldCookie) {
		t.Error("rotation was undone after a revocation failure — the exposed secrets are live again")
	}
	// Exactly the one forward ALTER: a second one would put the exposed
	// password back on the DB user while the files hold the new one.
	if len(sqls) != 1 || strings.Contains(sqls[0], kOldPw) {
		t.Errorf("DB password touched again after revocation failed: %d statements", len(sqls))
	}
}

func TestRotateKratos_DryRunTouchesNothing(t *testing.T) {
	saveKratosSeams(t)
	f := setupKratosFixture(t)

	called := false
	rotateRunSQL = func(context.Context, string) error { called = true; return nil }
	rotateRestartService = func(context.Context, string) error { called = true; return nil }
	rotateProbeKratosHealthy = func(context.Context, string) error { called = true; return nil }
	rotateRevokeAllKratosSessions = func(context.Context) (int, error) { called = true; return 0, nil }

	var out bytes.Buffer
	if err := rotateKratos(context.Background(), &out, true); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("dry run executed a privileged step")
	}
	if readTrim(t, f.yml) != strings.TrimSpace(kYML) || readTrim(t, f.pw) != kOldPw ||
		readTrim(t, f.def) != kOldDefault || readTrim(t, f.cookie) != kOldCookie {
		t.Error("dry run changed a file")
	}
	if !strings.Contains(out.String(), "DRY RUN") {
		t.Errorf("dry run output missing its banner:\n%s", out.String())
	}
}

func TestKratosConfigRotate_RequiresExactlyOneOccurrence(t *testing.T) {
	// The default secret appears twice (hand-edited duplicate) — ambiguous.
	yml := kYML + "  cipher:\n    - \"" + kOldDefault + "\"\n"
	if _, err := kratosConfigRotate(yml, kOldPw, "n1", kOldDefault, "n2", kOldCookie, "n3"); err == nil {
		t.Error("expected a refusal when an old secret appears more than once")
	}
	if _, err := kratosConfigRotate(kYML, kOldPw, "n1", kOldDefault, "n2", kOldCookie, "n3"); err != nil {
		t.Errorf("clean config refused: %v", err)
	}
}
