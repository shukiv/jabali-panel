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

func secretRedisACLPath() string {
	if v := os.Getenv("JABALI_REDIS_ACL_FILE"); v != "" {
		return v
	}
	return "/etc/redis/users.acl"
}

func redisSocketPath() string {
	if v := os.Getenv("JABALI_REDIS_SOCKET"); v != "" {
		return v
	}
	return "/run/redis/redis.sock"
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

// rotateRedisSetPassword rotates the jabali_panel Redis ACL user's password on
// the RUNNING server, authenticating as jabali_panel with authToken. It uses
// `ACL SETUSER … resetpass >new` (replace the password in place) and never
// `ACL LOAD` / a Redis restart — those would drop the runtime `wp_<osuser>`
// tenant ACL users the panel appended, until the reconciler recreates them.
var rotateRedisSetPassword = func(ctx context.Context, authToken, newToken string) error {
	cmd := exec.CommandContext(ctx, "redis-cli", "-s", redisSocketPath(), "--user", "jabali_panel",
		"--no-auth-warning", "ACL", "SETUSER", "jabali_panel", "resetpass", ">"+newToken)
	cmd.Env = append(os.Environ(), "REDISCLI_AUTH="+authToken)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("redis-cli ACL SETUSER: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// redis-cli prints command errors to stdout with exit 0; treat anything
	// that is not an OK/empty acknowledgement as a failure.
	if o := strings.TrimSpace(string(out)); o != "" && o != "OK" {
		return fmt.Errorf("redis ACL SETUSER returned: %s", o)
	}
	return nil
}

// rotateProbePanelHealthy verifies jabali-panel comes back and STAYS up after a
// rotation of a secret it consumes. A single `is-active` check is not enough:
// the .60 drill showed systemd reports active within ~1s of restart while the
// panel is still binding its socket (a transient nginx 502), and conversely a
// panel that can't use the new secret crash-loops (active→failed). So poll for
// a STABLE active state — several consecutive successes — within a timeout, and
// only then declare healthy; otherwise the caller rolls the rotation back.
var rotateProbePanelHealthy = func(ctx context.Context) error {
	deadline := time.Now().Add(20 * time.Second)
	stable := 0
	for {
		if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "jabali-panel").Run(); err == nil {
			if stable++; stable >= 3 { // ~3 consecutive active checks
				return nil
			}
		} else {
			stable = 0 // flapped: reset, keep watching for a crash-loop
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("jabali-panel did not reach a stable active state within timeout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// sqlSingleQuote renders a value as a MariaDB string literal body: backslash
// first (MariaDB treats '\' as an escape unless NO_BACKSLASH_ESCAPES), then
// single quotes doubled. Today's inputs are base64url (ids.NewSecret) and the
// old value read back from db-password (openssl rand -hex), neither of which
// contains these — but escape defensively so the function is safe for any value.
func sqlSingleQuote(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(v, "'", "''")
}

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
	cmd.AddCommand(newRotateDBAppUserCmd(), newRotateJWTCmd(), newRotateRedisPanelTokenCmd(), newRotateAllCmd())
	return cmd
}

func newRotateAllCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "all",
		Short: "Rotate every built panel secret in a lockout-safe order",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			return rotateAll(ctx, cmd.OutOrStdout(), dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and touch nothing")
	return cmd
}

// rotateAll runs the built rotations in a lockout-safe order, each with its own
// verify+rollback, and stops at the first failure (already-rotated secrets stay
// rotated — every step is independently valid, so the operator re-runs the
// remainder). It does NOT cover postgres/mariadb root, panel/mail TLS reissue,
// pdns, or the deferred wp-cache-hmac — those are separate per the runbook.
func rotateAll(ctx context.Context, out io.Writer, dryRun bool) error {
	steps := []struct {
		name string
		fn   func(context.Context, io.Writer, bool) error
	}{
		{"db-app-user", rotateDBAppUser},
		{"redis-panel-token", rotateRedisPanelToken},
		{"jwt", func(c context.Context, w io.Writer, d bool) error {
			return rotateSingleEnvKey(c, w, d, "JWT_SECRET", "secrets.rotate.jwt")
		}},
	}
	for _, s := range steps {
		fmt.Fprintf(out, "== %s ==\n", s.name)
		if err := s.fn(ctx, out, dryRun); err != nil {
			return fmt.Errorf("rotate all stopped at %s: %w", s.name, err)
		}
	}
	fmt.Fprintf(out, "All built rotations complete. postgres/mariadb root, panel/mail TLS, pdns and the deferred wp-cache-hmac are handled separately — see docs/secret-rotation.md.\n")
	return nil
}

func newRotateRedisPanelTokenCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "redis-panel-token",
		Short: "Rotate JABALI_REDIS_PANEL_TOKEN (panel.env + redis aclfile, live ACL SETUSER)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			return rotateRedisPanelToken(ctx, cmd.OutOrStdout(), dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and touch nothing")
	return cmd
}

// rotateRedisPanelToken rotates the panel's Redis credential. It changes the
// password LIVE on the running Redis (so no ACL LOAD / restart wipes tenant
// ACLs), rewrites both JABALI_REDIS_PANEL_TOKEN in panel.env and the
// jabali_panel line in the aclfile (for persistence across a future Redis
// restart), restarts the panel to reconnect, and rolls everything back —
// including the live Redis password — if the panel comes up unhealthy.
func rotateRedisPanelToken(ctx context.Context, out io.Writer, dryRun bool) error {
	envPath := secretEnvPath()
	aclPath := secretRedisACLPath()

	envData, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", envPath, err)
	}
	oldToken, ok := envGet(string(envData), "JABALI_REDIS_PANEL_TOKEN")
	if !ok {
		return fmt.Errorf("no JABALI_REDIS_PANEL_TOKEN in %s", envPath)
	}
	aclData, err := os.ReadFile(aclPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", aclPath, err)
	}
	newToken := ids.NewSecret()
	newEnv, ok := envReplaceKey(string(envData), "JABALI_REDIS_PANEL_TOKEN", newToken)
	if !ok {
		return fmt.Errorf("JABALI_REDIS_PANEL_TOKEN vanished from %s mid-rotation", envPath)
	}
	newACL, ok := aclReplacePanelToken(string(aclData), newToken)
	if !ok {
		return fmt.Errorf("no `user jabali_panel` line in %s", aclPath)
	}

	if dryRun {
		fmt.Fprintf(out, "DRY RUN — panel Redis token rotation would:\n")
		fmt.Fprintf(out, "  1. redis-cli ACL SETUSER jabali_panel resetpass ><new> (LIVE, no ACL LOAD)\n")
		fmt.Fprintf(out, "  2. rewrite JABALI_REDIS_PANEL_TOKEN in %s\n", envPath)
		fmt.Fprintf(out, "  3. rewrite the jabali_panel line in %s (default + tenant users preserved)\n", aclPath)
		fmt.Fprintf(out, "  4. systemctl restart jabali-panel\n")
		fmt.Fprintf(out, "  5. verify healthy; roll back (incl. the live Redis password) on failure\n")
		return nil
	}

	// 1. Snapshot both files FIRST (read-only on live state; root-only .bak),
	//    so a backup-write failure is caught before we touch the live Redis
	//    password.
	bakEnv, err := backupToBak(envPath)
	if err != nil {
		return fmt.Errorf("backup %s: %w", envPath, err)
	}
	bakACL, err := backupToBak(aclPath)
	if err != nil {
		_ = purgeBak(bakEnv)
		return fmt.Errorf("backup %s: %w", aclPath, err)
	}

	// 2. Change the password on the running Redis (auth with the old token,
	//    still valid). On failure nothing on disk changed — purge and stop.
	if err := rotateRedisSetPassword(ctx, oldToken, newToken); err != nil {
		_ = purgeBak(bakEnv)
		_ = purgeBak(bakACL)
		cliAuditErr(ctx, "secrets.rotate.redis_panel_token", "secret", "redis-panel-token", nil)
		return fmt.Errorf("live Redis password change: %w", err)
	}

	// rollback reverts the live Redis password to the old token and, ONLY if
	// that succeeds, restores the files. If the live revert fails, Redis still
	// accepts only the new token, so the files MUST stay at the new token to
	// match it — restoring them to the old value would wedge the dispatcher
	// (panel.env ≠ live Redis) with no way back. So on a failed revert we leave
	// the files at the new token and surface the split loudly.
	rollback := func() error {
		if err := rotateRedisSetPassword(ctx, newToken, oldToken); err != nil {
			return fmt.Errorf("live Redis revert FAILED — panel.env/aclfile left at the NEW token to match Redis, NOT restored: %w", err)
		}
		_ = restoreFromBak(envPath, bakEnv)
		_ = restoreFromBak(aclPath, bakACL)
		return nil
	}
	// fail rolls back, restarts the panel to match whatever token the files now
	// hold, and composes the error (flagging an incomplete rollback loudly).
	fail := func(stage string, cause error) error {
		rbErr := rollback()
		_ = rotateRestartService(ctx, "jabali-panel")
		cliAuditErr(ctx, "secrets.rotate.redis_panel_token", "secret", "redis-panel-token", nil)
		if rbErr != nil {
			return fmt.Errorf("%s: %w; ROLLBACK INCOMPLETE: %v", stage, cause, rbErr)
		}
		return fmt.Errorf("%s (rolled back): %w", stage, cause)
	}

	// 3. Persist the new token to both files.
	writeErr := atomicRewritePreserving(envPath, newEnv)
	if writeErr == nil {
		writeErr = atomicRewritePreserving(aclPath, newACL)
	}
	if writeErr != nil {
		return fail("rewrite secret files", writeErr)
	}

	// 4. Restart the panel so it reconnects with the new token.
	if err := rotateRestartService(ctx, "jabali-panel"); err != nil {
		return fail("restart jabali-panel", err)
	}

	// 5. Verify the panel comes back and stays up.
	if err := rotateProbePanelHealthy(ctx); err != nil {
		return fail("panel unhealthy after rotation", err)
	}

	_ = purgeBak(bakEnv)
	_ = purgeBak(bakACL)
	cliAuditOK(ctx, "secrets.rotate.redis_panel_token", "secret", "redis-panel-token", nil)
	fmt.Fprintf(out, "Rotated Redis panel token (live + panel.env + aclfile); restarted jabali-panel and verified.\n")
	return nil
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

	// 1. Snapshot the on-disk secrets FIRST. These only read the live files
	//    and write a root-only .bak — no live effect — so a backup-write
	//    failure (disk full, /etc read-only) is caught BEFORE we change the DB
	//    password and can't strand a DB whose credential we already rotated
	//    against files that still say the old one.
	bakPw, err := backupToBak(pwPath)
	if err != nil {
		return fmt.Errorf("backup %s: %w", pwPath, err)
	}
	bakEnv, err := backupToBak(envPath)
	if err != nil {
		_ = purgeBak(bakPw)
		return fmt.Errorf("backup %s: %w", envPath, err)
	}

	// 2. Apply the new password. On failure nothing on disk changed — purge
	//    the snapshots and stop.
	if err := rotateRunSQL(ctx, "ALTER USER 'jabali_panel_app'@'localhost' IDENTIFIED BY '"+sqlSingleQuote(newPw)+"'"); err != nil {
		_ = purgeBak(bakPw)
		_ = purgeBak(bakEnv)
		cliAuditErr(ctx, "secrets.rotate.db_app_user", "secret", "db-app-user", nil)
		return fmt.Errorf("ALTER USER jabali_panel_app: %w", err)
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

	// 5. Verify the panel came back and stays up (catches a crash-loop on the
	//    new config), then that the new credential itself authenticates.
	if err := rotateProbePanelHealthy(ctx); err != nil {
		rotateRollbackDBAppUser(ctx, pwPath, envPath, bakPw, bakEnv)
		_ = rotateRestartService(ctx, "jabali-panel")
		cliAuditErr(ctx, "secrets.rotate.db_app_user", "secret", "db-app-user", nil)
		return fmt.Errorf("panel unhealthy after rotation (rolled back): %w", err)
	}
	if err := rotateProbeDBAppUser(ctx, newPw); err != nil {
		rotateRollbackDBAppUser(ctx, pwPath, envPath, bakPw, bakEnv)
		_ = rotateRestartService(ctx, "jabali-panel")
		cliAuditErr(ctx, "secrets.rotate.db_app_user", "secret", "db-app-user", nil)
		return fmt.Errorf("post-rotation credential probe failed (rolled back): %w", err)
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
