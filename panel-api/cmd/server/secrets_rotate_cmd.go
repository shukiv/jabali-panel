// JAB-357 crit-7 — `jabali secrets rotate …`: rotate the panel secrets that
// internet-facing webmail could read before the JAB-351/357 isolation fix.
//
// This is an OPERATOR CEREMONY, run as root on one host at a time
// (`sudo jabali secrets rotate …`). It is deliberately NOT wired into
// install.sh's converger or the 04:30 auto-update — auto-rotating fleet
// secrets unprompted is exactly the outage this ticket is trying to avoid.
//
// Scope + procedure + the operator boundary are in
// docs/secret-rotation.md. Each rotation: apply the privileged
// mutation, back up the old on-disk secret, rewrite the file(s) atomically
// (preserving owner+mode), restart the consumer, health-probe, and roll back
// on failure. Privileged steps go through package-level seams so the whole
// flow is unit-tested with no box (secrets_rotate_cmd_test.go).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
)

// ---- path resolvers (env-overridable for tests / non-standard installs) ----

func secretEnvPath() string {
	if v := os.Getenv("JABALI_PANEL_ENV_FILE"); v != "" {
		return v
	}
	return "/etc/jabali/panel.env"
}

func secretDBPasswordPath() string {
	if v := os.Getenv("JABALI_DB_PASSWORD_FILE"); v != "" {
		return v
	}
	return "/etc/jabali/db-password"
}

// ---- privileged seams (overridden in tests) --------------------------------

// rotateRunSQL runs one root MariaDB statement over the unix socket.
var rotateRunSQL = func(ctx context.Context, sql string) error {
	// root authenticates via unix_socket (ADR-0097); no password on the CLI.
	return exec.CommandContext(ctx, "mysql", "--protocol=socket", "-e", sql).Run()
}

// rotateRestartService restarts a systemd unit.
var rotateRestartService = func(ctx context.Context, unit string) error {
	return exec.CommandContext(ctx, "systemctl", "restart", unit).Run()
}

// rotateProbeDBAppUser verifies the panel DB app-user can authenticate with the
// new password over the socket. Password is passed via MYSQL_PWD (not argv) to
// keep it out of the process table.
var rotateProbeDBAppUser = func(ctx context.Context, password string) error {
	cmd := exec.CommandContext(ctx, "mysql", "-u", "jabali_panel_app", "--protocol=socket", "jabali_panel", "-e", "SELECT 1")
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+password)
	return cmd.Run()
}

// rotateProbePanelHealthy verifies jabali-panel is running after a rotation of
// a secret it consumes. `is-active` returns non-zero unless the unit is fully
// active, so a start-up crash (e.g. bad secret) is caught and rolled back.
var rotateProbePanelHealthy = func(ctx context.Context) error {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "jabali-panel").Run()
}

// sqlSingleQuote renders a value as a MariaDB string literal body. The DB
// password is base64url (ids.NewSecret) so it never contains a quote or
// backslash, but double any quote defensively.
func sqlSingleQuote(v string) string { return strings.ReplaceAll(v, "'", "''") }

// ---- command group ---------------------------------------------------------

func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Rotate panel secrets after remediation (JAB-357; operator ceremony, run as root)",
	}
	cmd.AddCommand(newSecretsRotateCmd())
	return cmd
}

func newSecretsRotateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Rotate an exposed secret (see docs/secret-rotation.md)",
	}
	cmd.AddCommand(newRotateDBAppUserCmd(), newRotateJWTCmd())
	return cmd
}

func newRotateJWTCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "jwt",
		Short: "Rotate JWT_SECRET in panel.env (vestigial post-M20; safe near-noop)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()
			return rotateSingleEnvKey(ctx, cmd.OutOrStdout(), dryRun, "JWT_SECRET", "secrets.rotate.jwt")
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and touch nothing")
	return cmd
}

// rotateSingleEnvKey rotates ONE panel.env key to a fresh value, restarts the
// panel, and rolls back on an unhealthy probe. For secrets whose only consumer
// is the panel process reading panel.env (e.g. the vestigial JWT_SECRET).
// Secrets with an additional live side (Redis ACL, a DB user) have their own
// handler — this is the pure panel.env-key path.
func rotateSingleEnvKey(ctx context.Context, out io.Writer, dryRun bool, key, action string) error {
	envPath := secretEnvPath()
	envData, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", envPath, err)
	}
	if _, ok := envGet(string(envData), key); !ok {
		return fmt.Errorf("no %s in %s", key, envPath)
	}
	newEnv, ok := envReplaceKey(string(envData), key, ids.NewSecret())
	if !ok {
		return fmt.Errorf("%s vanished from %s mid-rotation", key, envPath)
	}

	if dryRun {
		fmt.Fprintf(out, "DRY RUN — would rewrite %s in %s (other keys preserved), restart jabali-panel, verify healthy.\n", key, envPath)
		return nil
	}

	bak, err := backupToBak(envPath)
	if err != nil {
		return fmt.Errorf("backup %s: %w", envPath, err)
	}
	if err := atomicRewritePreserving(envPath, newEnv); err != nil {
		_ = restoreFromBak(envPath, bak)
		cliAuditErr(ctx, action, "secret", key, nil)
		return fmt.Errorf("rewrite %s (rolled back): %w", envPath, err)
	}
	if err := rotateRestartService(ctx, "jabali-panel"); err != nil {
		_ = restoreFromBak(envPath, bak)
		_ = rotateRestartService(ctx, "jabali-panel")
		cliAuditErr(ctx, action, "secret", key, nil)
		return fmt.Errorf("restart jabali-panel (rolled back): %w", err)
	}
	if err := rotateProbePanelHealthy(ctx); err != nil {
		_ = restoreFromBak(envPath, bak)
		_ = rotateRestartService(ctx, "jabali-panel")
		cliAuditErr(ctx, action, "secret", key, nil)
		return fmt.Errorf("panel unhealthy after rotation (rolled back): %w", err)
	}
	_ = purgeBak(bak)
	cliAuditOK(ctx, action, "secret", key, nil)
	fmt.Fprintf(out, "Rotated %s; restarted jabali-panel and verified healthy.\n", key)
	return nil
}

func newRotateDBAppUserCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "db-app-user",
		Short: "Rotate the panel DB app-user (jabali_panel_app) password + DATABASE_URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			return rotateDBAppUser(ctx, cmd.OutOrStdout(), dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and touch nothing")
	return cmd
}

// rotateDBAppUser rotates jabali_panel_app's password: ALTER USER, rewrite the
// db-password file and the DATABASE_URL line in panel.env, restart jabali-panel,
// and verify the new credential — rolling everything back if the probe fails.
func rotateDBAppUser(ctx context.Context, out io.Writer, dryRun bool) error {
	envPath := secretEnvPath()
	pwPath := secretDBPasswordPath()

	envData, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", envPath, err)
	}
	oldDSN, ok := envGet(string(envData), "DATABASE_URL")
	if !ok {
		return fmt.Errorf("no DATABASE_URL in %s", envPath)
	}
	newPw := ids.NewSecret()
	newDSN, err := dsnReplacePassword(oldDSN, newPw)
	if err != nil {
		return fmt.Errorf("rebuild DSN: %w", err)
	}
	newEnv, ok := envReplaceKey(string(envData), "DATABASE_URL", newDSN)
	if !ok { // envGet found it above; this is belt-and-braces
		return fmt.Errorf("DATABASE_URL vanished from %s mid-rotation", envPath)
	}

	if dryRun {
		fmt.Fprintf(out, "DRY RUN — panel DB app-user rotation would:\n")
		fmt.Fprintf(out, "  1. ALTER USER 'jabali_panel_app'@'localhost' IDENTIFIED BY <new>\n")
		fmt.Fprintf(out, "  2. rewrite %s (mode preserved)\n", pwPath)
		fmt.Fprintf(out, "  3. rewrite DATABASE_URL in %s (other keys preserved)\n", envPath)
		fmt.Fprintf(out, "  4. systemctl restart jabali-panel\n")
		fmt.Fprintf(out, "  5. verify jabali_panel_app can authenticate; roll back on failure\n")
		return nil
	}

	// 1. Apply the new password FIRST, so config only ever points at a
	//    credential the DB already accepts.
	if err := rotateRunSQL(ctx, "ALTER USER 'jabali_panel_app'@'localhost' IDENTIFIED BY '"+sqlSingleQuote(newPw)+"'"); err != nil {
		cliAuditErr(ctx, "secrets.rotate.db_app_user", "secret", "db-app-user", nil)
		return fmt.Errorf("ALTER USER jabali_panel_app: %w", err)
	}

	// 2. Snapshot the old on-disk secrets for rollback.
	bakPw, err := backupToBak(pwPath)
	if err != nil {
		return fmt.Errorf("backup %s: %w", pwPath, err)
	}
	bakEnv, err := backupToBak(envPath)
	if err != nil {
		_ = purgeBak(bakPw)
		return fmt.Errorf("backup %s: %w", envPath, err)
	}

	// 3. Rewrite the files atomically (owner+mode preserved).
	writeErr := atomicRewritePreserving(pwPath, newPw+"\n")
	if writeErr == nil {
		writeErr = atomicRewritePreserving(envPath, newEnv)
	}
	if writeErr != nil {
		// Files may be half-written; restore both, revert the DB password.
		rotateRollbackDBAppUser(ctx, pwPath, envPath, bakPw, bakEnv)
		cliAuditErr(ctx, "secrets.rotate.db_app_user", "secret", "db-app-user", nil)
		return fmt.Errorf("rewrite secret files (rolled back): %w", writeErr)
	}

	// 4. Restart the panel so it reconnects with the new DSN.
	if err := rotateRestartService(ctx, "jabali-panel"); err != nil {
		rotateRollbackDBAppUser(ctx, pwPath, envPath, bakPw, bakEnv)
		cliAuditErr(ctx, "secrets.rotate.db_app_user", "secret", "db-app-user", nil)
		return fmt.Errorf("restart jabali-panel (rolled back): %w", err)
	}

	// 5. Verify the new credential actually works.
	if err := rotateProbeDBAppUser(ctx, newPw); err != nil {
		rotateRollbackDBAppUser(ctx, pwPath, envPath, bakPw, bakEnv)
		_ = rotateRestartService(ctx, "jabali-panel")
		cliAuditErr(ctx, "secrets.rotate.db_app_user", "secret", "db-app-user", nil)
		return fmt.Errorf("post-rotation health probe failed (rolled back): %w", err)
	}

	// 6. Verified — purge the rollback snapshots (they hold the old password).
	_ = purgeBak(bakPw)
	_ = purgeBak(bakEnv)
	cliAuditOK(ctx, "secrets.rotate.db_app_user", "secret", "db-app-user", nil)
	fmt.Fprintf(out, "Rotated panel DB app-user password; restarted jabali-panel and verified.\nNew secret stored at %s (root-only).\n", pwPath)
	return nil
}

// rotateRollbackDBAppUser restores both secret files from their snapshots and
// reverts the DB password to the old value read back from the db-password
// snapshot. Best-effort: every step is attempted even if an earlier one errors,
// because a partial rollback is worse than a full one.
func rotateRollbackDBAppUser(ctx context.Context, pwPath, envPath, bakPw, bakEnv string) {
	oldPw := ""
	if data, err := os.ReadFile(bakPw); err == nil {
		oldPw = strings.TrimSpace(string(data))
	}
	_ = restoreFromBak(pwPath, bakPw)
	_ = restoreFromBak(envPath, bakEnv)
	if oldPw != "" {
		_ = rotateRunSQL(ctx, "ALTER USER 'jabali_panel_app'@'localhost' IDENTIFIED BY '"+sqlSingleQuote(oldPw)+"'")
	}
}

// envGet reads a KEY=value line's value from env-file content.
func envGet(content, key string) (string, bool) {
	prefix := key + "="
	for _, ln := range strings.Split(content, "\n") {
		if strings.HasPrefix(ln, prefix) {
			return strings.TrimPrefix(ln, prefix), true
		}
	}
	return "", false
}
