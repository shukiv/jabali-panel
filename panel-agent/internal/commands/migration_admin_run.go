// Agent commands for SPA-driven M35 migrations:
//
//	migration.secrets_write    — writes /etc/jabali-panel/migration-
//	                             secrets/<job-id>.env with given content
//	                             (root:jabali 0640 per ADR-0094 §
//	                             "tracked risks").
//	migration.pull_source_run  — systemd-run-launches
//	                             'jabali migrate pull-source --job-id'
//	                             so the SSH pull + extract survive
//	                             a panel-api restart.
//	migration.import_run       — systemd-run-launches
//	                             'jabali migrate import --job-id ...'
//	                             so the multi-stage restore likewise
//	                             survives.
//
// All three run as root (agent runs as root). Validation:
//   - job_id: ULID-shape (26 alphanumeric)
//   - secrets file path: under /etc/jabali-panel/migration-secrets/
//   - systemd unit name: prefixed jabali-migrate-<verb>-<jobid>
//
// Pattern matches system.update_run (M29) — same transient-unit
// approach + reset-failed before each spawn so a previous run's
// failed state doesn't block ALREADY_EXISTS.
package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const migrationSecretsBaseDir = "/etc/jabali-panel/migration-secrets"

var migrationJobIDRe = regexp.MustCompile(`^[0-9A-Za-z]{26}$`)

// migrationSecretsWriteParams takes either a password OR a private-key
// blob (operator's choice). Both forms are written into the env file
// in the format `loadSecret` (cpanel/discover.go) expects.
type migrationSecretsWriteParams struct {
	JobID         string `json:"job_id"`
	SSHPassword   string `json:"ssh_password,omitempty"`
	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
	PluginToken   string `json:"plugin_token,omitempty"` // GH #648 wordpress_plugin
}

func init() {
	Default.Register("migration.secrets_write", migrationSecretsWriteHandler)
	Default.Register("migration.secrets_clone", migrationSecretsCloneHandler)
	Default.Register("migration.pull_source_run", migrationPullSourceRunHandler)
	Default.Register("migration.import_run", migrationImportRunHandler)
	Default.Register("migration.import_wp_run", migrationImportWPRunHandler)
}

// migrationSecretsCloneParams duplicates the env-file from src_job_id
// into dst_job_id. Used by the bulk-create handler so every child
// migration inherits the discovery draft's SSH credentials without
// re-prompting the operator. ADR-0094 §"per-source-kind support".
type migrationSecretsCloneParams struct {
	SrcJobID string `json:"src_job_id"`
	DstJobID string `json:"dst_job_id"`
}

func migrationSecretsCloneHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p migrationSecretsCloneParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error()}
	}
	if !migrationJobIDRe.MatchString(p.SrcJobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "src_job_id must be 26-char alnum (ULID)"}
	}
	if !migrationJobIDRe.MatchString(p.DstJobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "dst_job_id must be 26-char alnum (ULID)"}
	}
	src := filepath.Join(migrationSecretsBaseDir, p.SrcJobID+".env")
	dst := filepath.Join(migrationSecretsBaseDir, p.DstJobID+".env")
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "src secret not found: " + err.Error()}
	}
	if err := os.MkdirAll(migrationSecretsBaseDir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "mkdir secrets dir: " + err.Error()}
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "write tmp: " + err.Error()}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "rename: " + err.Error()}
	}
	return map[string]any{"ok": true}, nil
}

func migrationSecretsWriteHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p migrationSecretsWriteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error()}
	}
	if !migrationJobIDRe.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be 26-char alnum (ULID)"}
	}
	if p.SSHPassword == "" && p.SSHPrivateKey == "" && p.PluginToken == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "ssh_password, ssh_private_key, or plugin_token required"}
	}
	if err := os.MkdirAll(migrationSecretsBaseDir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "mkdir secrets dir: " + err.Error()}
	}
	target := filepath.Join(migrationSecretsBaseDir, p.JobID+".env")
	tmp := target + ".tmp"
	var b strings.Builder
	b.WriteString("# Generated by SPA migration secrets-upload (M35)\n")
	b.WriteString("# Reaped on terminal job state via WipeJobSecret.\n")
	if p.SSHPassword != "" {
		b.WriteString("SSH_PASSWORD=")
		b.WriteString(p.SSHPassword)
		b.WriteByte('\n')
	}
	if p.SSHPrivateKey != "" {
		// loadSecret in cpanel/discover.go splits the env file on
		// '\n' then strings.Cut(line, "=") — that only sees the
		// first line of a multi-line PEM key. Encode the whole
		// PEM blob as standard base64 (single-line) so the parser
		// recovers it via SSH_PRIVATE_KEY_B64.
		b.WriteString("SSH_PRIVATE_KEY_B64=")
		b.WriteString(base64.StdEncoding.EncodeToString(
			[]byte(strings.TrimRight(p.SSHPrivateKey, "\n") + "\n"),
		))
		b.WriteByte('\n')
	}
	if p.PluginToken != "" {
		b.WriteString("PLUGIN_TOKEN=")
		b.WriteString(strings.TrimSpace(p.PluginToken))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(tmp, []byte(b.String()), 0o640); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "write tmp: " + err.Error()}
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "rename: " + err.Error()}
	}
	// Best-effort group ownership so the panel-api can read the
	// file (root:jabali 0640). Failure is non-fatal — rotate path
	// fires the same chown on jabali update.
	_ = exec.Command("chown", "root:jabali", target).Run()
	return map[string]string{"path": target}, nil
}

type migrationPullSourceRunParams struct {
	JobID   string `json:"job_id"`
	SSHUser string `json:"ssh_user,omitempty"` // defaults to 'root'
}

type migrationRunResponse struct {
	Unit      string `json:"unit"`
	StartedAt string `json:"started_at"`
}

func migrationPullSourceRunHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p migrationPullSourceRunParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error()}
	}
	if !migrationJobIDRe.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be 26-char alnum (ULID)"}
	}
	sshUser := p.SSHUser
	if sshUser == "" {
		sshUser = "root"
	}
	if !looksLikeUnixUsername(sshUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "ssh_user looks unsafe"}
	}
	unit := fmt.Sprintf("jabali-migrate-pull-%s.service", p.JobID)
	// Stop any still-running prior attempt + reset failed-state so the
	// transient unit name is free. Both best-effort; systemd-run below
	// is the gate that actually errors if the unit can't be created.
	_ = exec.CommandContext(ctx, "systemctl", "stop", "--quiet", unit).Run()
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", unit).Run()
	startedAt := time.Now().UTC()
	cmd := exec.CommandContext(ctx, "systemd-run",
		"--unit="+unit,
		"--no-block",
		"--collect",
		"/usr/local/bin/jabali", "migrate", "pull-source",
		"--job-id="+p.JobID,
		"--ssh-user="+sshUser,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemd-run: %v: %s", err, string(out))}
	}
	return migrationRunResponse{Unit: unit, StartedAt: startedAt.Format(time.RFC3339Nano)}, nil
}

// migrationImportWPRunParams / handler — GH #647. systemd-run-launches
// `jabali migrate import-wp` for a staged wordpress_ssh job.
type migrationImportWPRunParams struct {
	JobID      string `json:"job_id"`
	DestUser   string `json:"dest_user"`
	DestDomain string `json:"dest_domain"`
}

// wpMigDestDomainRe bounds the dest domain so it can never inject a path
// segment (it becomes /home/<user>/domains/<domain>/public_html).
var wpMigDestDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

func migrationImportWPRunHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p migrationImportWPRunParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error()}
	}
	if !migrationJobIDRe.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be 26-char alnum (ULID)"}
	}
	if !looksLikeUnixUsername(p.DestUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "dest_user looks unsafe"}
	}
	if !wpMigDestDomainRe.MatchString(p.DestDomain) || strings.Contains(p.DestDomain, "..") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "dest_domain invalid"}
	}
	unit := fmt.Sprintf("jabali-migrate-importwp-%s.service", p.JobID)
	_ = exec.CommandContext(ctx, "systemctl", "stop", "--quiet", unit).Run()
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", unit).Run()
	startedAt := time.Now().UTC()
	cmd := exec.CommandContext(ctx, "systemd-run",
		"--unit="+unit, "--no-block", "--collect",
		"/usr/local/bin/jabali", "migrate", "import-wp",
		"--job-id="+p.JobID, "--dest-user="+p.DestUser, "--dest-domain="+p.DestDomain,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemd-run: %v: %s", err, string(out))}
	}
	return migrationRunResponse{Unit: unit, StartedAt: startedAt.Format(time.RFC3339Nano)}, nil
}

type migrationImportRunParams struct {
	JobID           string `json:"job_id"`
	TargetUser      string `json:"target_user"`
	TargetEmail     string `json:"target_email,omitempty"`
	TargetPassword  string `json:"target_password,omitempty"`
	TargetPackageID string `json:"target_package_id,omitempty"`
}

// importSystemdRunArgs assembles the systemd-run argv for a migration import.
// runProps are extra systemd-run options (e.g. --property=EnvironmentFile=…)
// that MUST come before the command; cmdOpts are extra `jabali migrate import`
// flags. Keeping --property in runProps (never in the command tail) is the
// whole point: a systemd-run flag placed after /usr/local/bin/jabali is handed
// to jabali's argv and dies with "unknown flag: --property" (GH #746).
func importSystemdRunArgs(jobID, targetUser string, runProps, cmdOpts []string) []string {
	args := []string{
		"--unit=" + fmt.Sprintf("jabali-migrate-import-%s.service", jobID),
		"--no-block",
		"--collect",
	}
	args = append(args, runProps...)
	args = append(args,
		"/usr/local/bin/jabali", "migrate", "import",
		"--job-id="+jobID,
		"--target-user="+targetUser,
	)
	args = append(args, cmdOpts...)
	return args
}

func migrationImportRunHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p migrationImportRunParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error()}
	}
	if !migrationJobIDRe.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be 26-char alnum (ULID)"}
	}
	if !looksLikeUnixUsername(p.TargetUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "target_user looks unsafe"}
	}
	var runProps, cmdOpts []string
	if p.TargetEmail != "" {
		cmdOpts = append(cmdOpts, "--target-email="+p.TargetEmail)
	}
	if p.TargetPassword != "" {
		// Reject control chars so a crafted password can't inject extra
		// KEY=VALUE lines into the EnvironmentFile (env-injection). A real
		// account password never contains these.
		if strings.ContainsAny(p.TargetPassword, "\n\r\x00") {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "target password contains invalid control characters"}
		}
		// #496: never put the password on the systemd-run argv (it lands in
		// /proc/<pid>/cmdline AND the transient unit's ExecStart). Hand it to
		// the unit via a 0600 EnvironmentFile; `jabali migrate import` reads
		// JABALI_TARGET_PASSWORD and removes the file after consuming it.
		envPath := fmt.Sprintf("/run/jabali-migrate-%s.env", p.JobID)
		content := "JABALI_TARGET_PASSWORD=" + p.TargetPassword + "\n" +
			"JABALI_TARGET_PASSWORD_FILE=" + envPath + "\n"
		if werr := os.WriteFile(envPath, []byte(content), 0o600); werr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write target-password env: %v", werr)}
		}
		// --property is a systemd-run flag — it lands in runProps (before the
		// command), NOT in cmdOpts.
		runProps = append(runProps, "--property=EnvironmentFile="+envPath)
	}
	if p.TargetPackageID != "" {
		cmdOpts = append(cmdOpts, "--target-package-id="+p.TargetPackageID)
	}
	args := importSystemdRunArgs(p.JobID, p.TargetUser, runProps, cmdOpts)
	unit := fmt.Sprintf("jabali-migrate-import-%s.service", p.JobID)
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", unit).Run()
	startedAt := time.Now().UTC()
	cmd := exec.CommandContext(ctx, "systemd-run", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemd-run: %v: %s", err, string(out))}
	}
	return migrationRunResponse{Unit: unit, StartedAt: startedAt.Format(time.RFC3339Nano)}, nil
}

// looksLikeUnixUsername — defensive whitelist on operator-supplied
// usernames passed straight to systemd-run argv. Same shape the
// directadmin/Hestia packages use.
func looksLikeUnixUsername(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
