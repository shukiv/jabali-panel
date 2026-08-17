package commands

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mail_admin_cred.go — GH #243 (ADR-0142). Reveal / rotate the Stalwart admin
// login (the username+password the WebAdmin GUI prompts for). That credential
// is the recovery admin seeded as STALWART_RECOVERY_ADMIN=admin:<token> in
// /etc/jabali-panel/stalwart.env, and the very same token is the panel's
// management credential — panel-agent's mail.* commands read the .token file
// per-call, and panel-api caches the env value at boot. So a rotate must:
//   1. write a new token to /etc/jabali-panel/stalwart-admin.token,
//   2. rewrite the STALWART_RECOVERY_ADMIN line in stalwart.env,
//   3. restart jabali-stalwart so it accepts the new password, and
//   4. restart jabali-panel so it refreshes its boot-cached copy.
//
// The jabali-panel restart is scheduled a few seconds out (systemd-run) so the
// in-flight panel-api request that triggered this verb can return the new
// credential BEFORE panel-api is bounced. panel-agent itself needs no restart
// (it reads the token file fresh on every call).
//
//	mail.admin_cred.manage  {action:"reveal"|"rotate"}  ->  {user, password, rotated}

// Paths are vars so tests can redirect them off /etc.
var (
	stalwartAdminTokenFile = "/etc/jabali-panel/stalwart-admin.token"
	stalwartEnvFile        = "/etc/jabali-panel/stalwart.env"
)

const stalwartRecoveryAdminKey = "STALWART_RECOVERY_ADMIN="

// restartServiceFunc / scheduleRestartFunc are vars so tests don't shell out.
var (
	restartServiceFunc = func(ctx context.Context, unit string) error {
		out, err := execCommandContext(ctx, "systemctl", "restart", unit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl restart %s: %v: %s", unit, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	// scheduleRestartFunc bounces a unit a few seconds in the future via a
	// transient systemd unit, so the caller's response is delivered first.
	scheduleRestartFunc = func(ctx context.Context, unit string) error {
		out, err := execCommandContext(ctx, "systemd-run", "--quiet", "--collect",
			"--on-active=3s", "systemctl", "restart", unit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemd-run restart %s: %v: %s", unit, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	// verifyStalwartAuthFunc returns true if (user, secret) authenticate
	// against the loopback Stalwart admin HTTP listener. Used post-restart to
	// confirm a rotated recovery-admin password is live BEFORE we publish the
	// new token to the file panel-agent reads per-call.
	//
	// `systemctl restart` returns before Stalwart binds :8446, so a single
	// probe races startup and a transport error (connection refused) is NOT an
	// auth failure — it means "not ready yet". Poll until Stalwart answers:
	// an HTTP response decides it (401 = bad password; anything else = good),
	// transport errors retry until the deadline.
	verifyStalwartAuthFunc = func(ctx context.Context, user, secret string) bool {
		url := stalwartAdminURLFunc() + jmapSessionPath
		deadline := time.Now().Add(stalwartVerifyTimeout)
		for {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err == nil {
				req.SetBasicAuth(user, secret)
				if resp, derr := stalwartHTTPClientFunc().Do(req); derr == nil {
					code := resp.StatusCode
					resp.Body.Close()
					return code != http.StatusUnauthorized
				}
			}
			// transport error -> Stalwart still starting; wait and retry.
			if time.Now().After(deadline) {
				return false
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(stalwartVerifyPoll):
			}
		}
	}
)

const (
	// stalwartVerifyTimeout bounds how long a rotate waits for Stalwart to
	// come back up and answer the auth probe before giving up + rolling back.
	stalwartVerifyTimeout = 25 * time.Second
	stalwartVerifyPoll    = 1 * time.Second
)

type adminCredParams struct {
	Action string `json:"action"` // "reveal" | "rotate"
}

type adminCredResponse struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Rotated  bool   `json:"rotated"`
}

func mailAdminCredHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p adminCredParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}

	user, err := recoveryAdminUser()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	switch p.Action {
	case "reveal":
		tok, rerr := os.ReadFile(stalwartAdminTokenFile) //nolint:gosec // operator-owned path; 0640 on disk
		if rerr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read token: %v", rerr)}
		}
		return adminCredResponse{User: user, Password: strings.TrimSpace(string(tok)), Rotated: false}, nil

	case "rotate":
		newTok, gerr := generateAdminToken()
		if gerr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: gerr.Error()}
		}
		// Order matters for crash-safety. panel-agent reads the .token file
		// fresh on EVERY mail.* call, so if we wrote it before Stalwart accepts
		// the new value, every management call would 401 in the window. So:
		//   1. rewrite env (after backing it up) and restart Stalwart,
		//   2. VERIFY the new password authenticates,
		//   3. only then publish the new token to the .token file.
		// On any failure before step 3 we restore the env + restart, leaving
		// the still-valid old token file untouched — the mail plane stays up.
		envBackup, berr := os.ReadFile(stalwartEnvFile) //nolint:gosec // operator-owned path; 0640 on disk
		if berr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read env: %v", berr)}
		}
		if eerr := rewriteRecoveryAdmin(stalwartEnvFile, user, newTok); eerr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: eerr.Error()}
		}
		rollback := func() {
			if err := writeFilePreservingOwner(stalwartEnvFile, envBackup); err == nil {
				_ = restartServiceFunc(ctx, "jabali-stalwart")
			}
		}
		// Stalwart must restart to accept the new recovery-admin password.
		if rerr := restartServiceFunc(ctx, "jabali-stalwart"); rerr != nil {
			rollback()
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: rerr.Error()}
		}
		if !verifyStalwartAuthFunc(ctx, user, newTok) {
			rollback()
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "rotated password did not authenticate against Stalwart — reverted, old password still valid"}
		}
		// Verified live — now safe to publish the token panel-agent reads.
		if werr := writeTokenPreservingOwner(stalwartAdminTokenFile, newTok); werr != nil {
			rollback()
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: werr.Error()}
		}
		// panel-api caches the env value at boot — bounce it shortly AFTER we
		// return, so this response (carrying the new password) isn't lost.
		// Non-fatal if scheduling fails: panel-api keeps the stale cred until
		// its next restart; the rotation itself already succeeded.
		_ = scheduleRestartFunc(ctx, "jabali-panel")
		return adminCredResponse{User: user, Password: newTok, Rotated: true}, nil

	default:
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("unknown action %q (want reveal|rotate)", p.Action)}
	}
}

// recoveryAdminUser parses the username from STALWART_RECOVERY_ADMIN=user:secret
// so a rotate preserves whatever username install seeded (default "admin").
func recoveryAdminUser() (string, error) {
	data, err := os.ReadFile(stalwartEnvFile) //nolint:gosec // operator-owned path; 0640 on disk
	if err != nil {
		return "", fmt.Errorf("read %s: %w", stalwartEnvFile, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, stalwartRecoveryAdminKey) {
			continue
		}
		rest := strings.TrimPrefix(line, stalwartRecoveryAdminKey)
		if idx := strings.IndexByte(rest, ':'); idx > 0 {
			return rest[:idx], nil
		}
		return "", fmt.Errorf("%s malformed (no user:secret)", stalwartRecoveryAdminKey)
	}
	return "", fmt.Errorf("%s not found in %s", stalwartRecoveryAdminKey, stalwartEnvFile)
}

// generateAdminToken mirrors install.sh `openssl rand -base64 32`.
func generateAdminToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// writeFilePreservingOwner rewrites a file in place, keeping the existing
// owner/group and forcing 0640 so the jabali-mail group can still read it.
func writeFilePreservingOwner(path string, data []byte) error {
	var uid, gid = -1, -1
	if fi, err := os.Stat(path); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if uid >= 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
	}
	return nil
}

// writeTokenPreservingOwner rewrites the token file (jabali:jabali-mail 0640)
// with a trailing newline so the panel-agent and Stalwart can still read it.
func writeTokenPreservingOwner(path, tok string) error {
	return writeFilePreservingOwner(path, []byte(tok+"\n"))
}

// rewriteRecoveryAdmin replaces the STALWART_RECOVERY_ADMIN line in the env
// file with user:tok, preserving every other line and the file mode.
func rewriteRecoveryAdmin(path, user, tok string) error {
	data, err := os.ReadFile(path) //nolint:gosec // operator-owned path; 0640 on disk
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var uid, gid = -1, -1
	if fi, serr := os.Stat(path); serr == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), stalwartRecoveryAdminKey) {
			lines[i] = fmt.Sprintf("%s%s:%s", stalwartRecoveryAdminKey, user, tok)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%s line not found in %s", stalwartRecoveryAdminKey, path)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o640); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if uid >= 0 {
		_ = os.Chown(path, uid, gid)
	}
	return nil
}

func init() {
	Default.Register("mail.admin_cred.manage", mailAdminCredHandler)
}
