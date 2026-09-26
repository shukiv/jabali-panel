// JAB-357 crit-7 — `jabali secrets rotate kratos`: rotate the Kratos secrets
// that the internet-facing webmail could read before the JAB-351/357 fix.
//
// /etc/jabali-panel/kratos.yml is root:jabali 0640 (Kratos runs as jabali and
// must read it). It inlines the Kratos DB password (dsn) and the
// secrets.default / secrets.cookie values, so while jabali-webmail sat in the
// jabali group it could read all three:
//
//   - the DB password reaches the Kratos database: identities, password
//     hashes, TOTP secrets and live session tokens;
//   - the cookie/default secrets sign and encrypt Kratos's cookies (session
//     and CSRF), so they let an attacker mint valid CSRF tokens and open any
//     captured cookie.
//
// The rotation therefore REPLACES all three and keeps none of the old values:
// Kratos's usual rollover (prepend the new secret, keep the old one for
// verification) would keep the exposed secret valid, which defeats the point.
// The cost is a one-time sign-out: existing cookies cannot be decoded, and all
// active sessions are revoked server-side as well, because the old DB
// password could have been used to read session tokens straight from the
// database. No data at rest depends on these secrets (no `ciphers:` block in
// kratos.yml), so nothing needs re-encrypting.
//
// The source of truth for each value is its own root-only file
// (kratos-db-password, kratos-secrets/{default,cookie}); install.sh re-renders
// kratos.yml from them on every `jabali update`. All four are rewritten
// together so the next update renders the NEW values.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const kratosDBUser = "jabali_kratos"

func secretKratosConfigPath() string {
	if v := os.Getenv("JABALI_KRATOS_CONFIG"); v != "" {
		return v
	}
	return "/etc/jabali-panel/kratos.yml"
}

func secretKratosDBPasswordPath() string {
	if v := os.Getenv("JABALI_KRATOS_DB_PASSWORD_FILE"); v != "" {
		return v
	}
	return "/etc/jabali-panel/kratos-db-password"
}

func secretKratosSecretsDir() string {
	if v := os.Getenv("JABALI_KRATOS_SECRETS_DIR"); v != "" {
		return v
	}
	return "/etc/jabali-panel/kratos-secrets"
}

// rotateProbeKratosHealthy verifies jabali-kratos comes back and stays up with
// the new config, and that the jabali_kratos DB user authenticates with the
// new password. Password via MYSQL_PWD, never argv.
var rotateProbeKratosHealthy = func(ctx context.Context, password string) error {
	if err := pollUnitStableActive(ctx, "jabali-kratos"); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "mysql", "-u", kratosDBUser, "--protocol=socket", "jabali_kratos", "-e", "SELECT 1")
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+password)
	return cmd.Run()
}

// rotateRevokeAllKratosSessions revokes every active Kratos session over the
// admin API and returns how many it revoked. The admin list returns at most
// one page, so it lists again until nothing active is left.
var rotateRevokeAllKratosSessions = func(ctx context.Context) (int, error) {
	kc, err := cliKratos()
	if err != nil {
		return 0, err
	}
	revoked := 0
	for round := 0; round < 100; round++ {
		sessions, err := kc.ListActiveSessions(ctx)
		if err != nil {
			return revoked, fmt.Errorf("list sessions: %w", err)
		}
		if len(sessions) == 0 {
			return revoked, nil
		}
		for _, s := range sessions {
			if err := kc.RevokeSession(ctx, s.ID); err != nil {
				return revoked, fmt.Errorf("revoke session: %w", err)
			}
			revoked++
		}
	}
	return revoked, fmt.Errorf("sessions still active after 100 revocation rounds")
}

// newKratosSecret returns 256 bits as 64 lowercase hex characters — the same
// shape install.sh generates (`openssl rand -hex 32`), safe inside the DSN
// userinfo, a YAML double-quoted string and a SQL literal.
func newKratosSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// kratosConfigRotate returns kratos.yml with the DB password in the dsn and the
// default/cookie secrets replaced. Each old value must appear exactly once in
// its expected form; anything else means kratos.yml no longer matches the
// source files and a blind rewrite could leave Kratos on the wrong DSN.
func kratosConfigRotate(yml, oldPw, newPw, oldDefault, newDefault, oldCookie, newCookie string) (string, error) {
	swaps := []struct{ what, old, new string }{
		{"dsn password", kratosDBUser + ":" + oldPw + "@", kratosDBUser + ":" + newPw + "@"},
		{"secrets.default", `"` + oldDefault + `"`, `"` + newDefault + `"`},
		{"secrets.cookie", `"` + oldCookie + `"`, `"` + newCookie + `"`},
	}
	for _, s := range swaps {
		if n := strings.Count(yml, s.old); n != 1 {
			return "", fmt.Errorf("kratos.yml does not match its source file for %s (found %d occurrences, want 1) — run `jabali update` to re-render it, then retry", s.what, n)
		}
	}
	for _, s := range swaps {
		yml = strings.Replace(yml, s.old, s.new, 1)
	}
	return yml, nil
}

func readKratosSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return v, nil
}

func newRotateKratosCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "kratos",
		Short: "Rotate the Kratos DB password and cookie/default secrets (signs every user out)",
		Args:  cobra.NoArgs,
		// Config for the Kratos admin URL (session revocation); the DB only
		// for the audit event, so a DB outage never blocks the rotation.
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := initConfig(); err != nil {
				return err
			}
			_ = initDB()
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()
			return rotateKratos(ctx, cmd.OutOrStdout(), dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and touch nothing")
	return cmd
}

// rotateKratos replaces the Kratos DB password and the default/cookie secrets,
// restarts Kratos, verifies it, and revokes every active session. A failed
// probe rolls everything back. A failed revocation does NOT: the secrets are
// already rotated, and rolling back would put the exposed values back in
// service, so the command reports the error for a manual revoke instead.
func rotateKratos(ctx context.Context, out io.Writer, dryRun bool) error {
	const action = "secrets.rotate.kratos"
	ymlPath := secretKratosConfigPath()
	pwPath := secretKratosDBPasswordPath()
	defPath := filepath.Join(secretKratosSecretsDir(), "default")
	cookiePath := filepath.Join(secretKratosSecretsDir(), "cookie")

	ymlData, err := os.ReadFile(ymlPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", ymlPath, err)
	}
	oldPw, err := readKratosSecretFile(pwPath)
	if err != nil {
		return fmt.Errorf("read Kratos DB password: %w", err)
	}
	oldDef, err := readKratosSecretFile(defPath)
	if err != nil {
		return fmt.Errorf("read Kratos default secret: %w", err)
	}
	oldCookie, err := readKratosSecretFile(cookiePath)
	if err != nil {
		return fmt.Errorf("read Kratos cookie secret: %w", err)
	}

	var newPw, newDef, newCookie string
	for _, dst := range []*string{&newPw, &newDef, &newCookie} {
		if *dst, err = newKratosSecret(); err != nil {
			return fmt.Errorf("generate secret: %w", err)
		}
	}
	newYML, err := kratosConfigRotate(string(ymlData), oldPw, newPw, oldDef, newDef, oldCookie, newCookie)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Fprintf(out, "DRY RUN — Kratos secret rotation would:\n")
		fmt.Fprintf(out, "  1. ALTER USER '%s'@'localhost' IDENTIFIED BY <new>\n", kratosDBUser)
		fmt.Fprintf(out, "  2. rewrite %s, %s, %s and the dsn + secrets in %s (old values NOT kept)\n", pwPath, defPath, cookiePath, ymlPath)
		fmt.Fprintf(out, "  3. systemctl restart jabali-kratos; verify it stays up and the new DB password authenticates; roll back on failure\n")
		fmt.Fprintf(out, "  4. revoke every active Kratos session — every panel user must sign in again\n")
		return nil
	}

	// 1. Snapshot every file first (root-only .rotate.bak).
	files := []struct{ path, content string }{
		{pwPath, newPw + "\n"},
		{defPath, newDef + "\n"},
		{cookiePath, newCookie + "\n"},
		{ymlPath, newYML},
	}
	baks := make([]string, 0, len(files))
	purgeAll := func() {
		for _, b := range baks {
			_ = purgeBak(b)
		}
	}
	for _, f := range files {
		bak, err := backupToBak(f.path)
		if err != nil {
			purgeAll()
			return fmt.Errorf("backup %s: %w", f.path, err)
		}
		baks = append(baks, bak)
	}

	alter := func(pw string) error {
		return rotateRunSQL(ctx, "ALTER USER '"+kratosDBUser+"'@'localhost' IDENTIFIED BY '"+sqlSingleQuote(pw)+"'")
	}

	// 2. Apply the new DB password. Nothing on disk changed yet on failure.
	if err := alter(newPw); err != nil {
		purgeAll()
		cliAuditErr(ctx, action, "secret", "kratos", nil)
		return fmt.Errorf("ALTER USER %s: %w", kratosDBUser, err)
	}

	rollback := func() {
		for i, f := range files {
			_ = restoreFromBak(f.path, baks[i])
		}
		_ = alter(oldPw)
	}

	// 3. Rewrite the source files and kratos.yml.
	for _, f := range files {
		if err := atomicRewritePreserving(f.path, f.content); err != nil {
			rollback()
			cliAuditErr(ctx, action, "secret", "kratos", nil)
			return fmt.Errorf("rewrite %s (rolled back): %w", f.path, err)
		}
	}

	// 4. Restart Kratos onto the new config and verify it.
	if err := rotateRestartService(ctx, "jabali-kratos"); err != nil {
		rollback()
		_ = rotateRestartService(ctx, "jabali-kratos")
		cliAuditErr(ctx, action, "secret", "kratos", nil)
		return fmt.Errorf("restart jabali-kratos (rolled back): %w", err)
	}
	if err := rotateProbeKratosHealthy(ctx, newPw); err != nil {
		rollback()
		_ = rotateRestartService(ctx, "jabali-kratos")
		cliAuditErr(ctx, action, "secret", "kratos", nil)
		return fmt.Errorf("jabali-kratos unhealthy after rotation (rolled back): %w", err)
	}
	purgeAll()
	cliAuditOK(ctx, action, "secret", "kratos", nil)

	// 5. Revoke every session. Tokens read with the old DB password would
	// otherwise stay valid until they expire.
	n, err := rotateRevokeAllKratosSessions(ctx)
	if err != nil {
		cliAuditErr(ctx, "secrets.rotate.kratos.revoke_sessions", "secret", "kratos", nil)
		return fmt.Errorf("Kratos secrets rotated, but revoking sessions failed after %d: %w — revoke the rest with `jabali session list` / `jabali session revoke-user`", n, err)
	}
	cliAuditOK(ctx, "secrets.rotate.kratos.revoke_sessions", "secret", "kratos", nil)
	fmt.Fprintf(out, "Rotated the Kratos DB password and default/cookie secrets; restarted jabali-kratos and verified.\n")
	fmt.Fprintf(out, "Revoked %d active session(s). Every panel user must sign in again.\n", n)
	return nil
}
