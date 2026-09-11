// M30.1 follow-up — agent-side test-connection for backup destinations.
// panel-api can't read the 0600 root:root creds env file or shell out
// with HOME=/root, so the full test runs here as root.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

type backupDestTestParams struct {
	URL            string            `json:"url"`
	CredentialsRef string            `json:"credentials_ref,omitempty"`
	SFTP           *backupSFTPInputs `json:"sftp,omitempty"`
}

type backupDestTestResult struct {
	Status        string `json:"status"`
	StdoutPreview string `json:"stdout_preview,omitempty"`
	Stderr        string `json:"stderr,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

func backupDestTestHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupDestTestParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid_arg: %w", err)
	}
	if p.URL == "" {
		return nil, fmt.Errorf("invalid_arg: url required")
	}
	var extraEnv []string
	if p.CredentialsRef != "" {
		env, err := backup.LoadEnvFile(p.CredentialsRef)
		if err != nil {
			return nil, fmt.Errorf("creds_load_failed: %w", err)
		}
		extraEnv = env
	}
	stdout, stderr, err := backup.SnapshotsRemote(
		ctx,
		nil,
		p.URL,
		backup.DefaultPasswordFile,
		extraEnv,
		bkResticOptions(p.SFTP),
	)
	if err != nil {
		stderrStr := strings.TrimSpace(string(stderr))
		// "repository does not exist" / "unable to open config file"
		// means the SSH/SFTP layer succeeded — auth + reachability
		// are fine, just no restic repo at the path yet. Auto-init
		// it so the first backup doesn't have to. restic's SFTP/S3/B2
		// backends create parent directories during init.
		lower := strings.ToLower(stderrStr)
		if strings.Contains(lower, "repository does not exist") ||
			strings.Contains(lower, "unable to open config file") {
			// SFTP: pre-create the parent path on the remote — restic
			// init does NOT mkdir -p the parent chain itself.
			if p.SFTP != nil && p.SFTP.Host != "" {
				if mkOut, mkErr := backup.MkdirRemoteSFTP(ctx, backup.SFTPInputs{
					Host:    p.SFTP.Host,
					User:    p.SFTP.User,
					Port:    p.SFTP.Port,
					Path:    p.SFTP.Path,
					Auth:    p.SFTP.Auth,
					KeyPath: p.SFTP.KeyPath,
				}, extraEnv); mkErr != nil {
					return backupDestTestResult{
						Status: "error",
						Detail: "remote_mkdir_failed: " + mkErr.Error(),
						Stderr: strings.TrimSpace(string(mkOut)),
					}, nil
				}
			}
			// Serialize this init against the scheduler's per-account init
			// (bkEnsureRepoReady) and any concurrent test-connection on the same
			// destination — two restic inits on one empty repo corrupt it
			// (JAB-405). The loser sees "already initialized" below.
			var initStderr []byte
			var initErr error
			if lockErr := withRepoInitLock(ctx, p.URL, func() error {
				_, initStderr, initErr = backup.InitRemote(
					ctx,
					nil,
					p.URL,
					backup.DefaultPasswordFile,
					extraEnv,
					bkResticOptions(p.SFTP),
				)
				return initErr
			}); lockErr != nil && initErr == nil {
				// The lock could not be acquired, so init never ran. Surface it
				// rather than initialise unserialized.
				return backupDestTestResult{
					Status: "error",
					Detail: "init_lock_failed: " + lockErr.Error(),
				}, nil
			}
			if initErr != nil {
				initErrStr := strings.TrimSpace(string(initStderr))
				lowerInit := strings.ToLower(initErrStr)
				// Treat already-initialized as success (race with a
				// concurrent init or a hand-initialized repo).
				if strings.Contains(lowerInit, "already initialized") ||
					strings.Contains(lowerInit, "config file already exists") {
					return backupDestTestResult{
						Status:        "ok",
						StdoutPreview: "reachable — restic repo already initialized",
					}, nil
				}
				return backupDestTestResult{
					Status: "error",
					Detail: "init_failed: " + initErr.Error(),
					Stderr: initErrStr,
				}, nil
			}
			return backupDestTestResult{
				Status:        "ok",
				StdoutPreview: "reachable — restic repo initialized at destination",
			}, nil
		}
		// Repo exists but the probe failed for a non-missing reason. Classify it
		// so a repo that can't be opened (foreign/rotated password, or a
		// concurrent-init key/config mismatch — JAB-405) gets the same actionable
		// Detail as a real backup run, not a raw restic dump the operator can't
		// act on.
		//
		// For the key/config mismatch, count the key files (below restic, which
		// can't open the repo to list them) so the operator sees the exact key to
		// move vs "config corrupt, start fresh" (JAB-405 Part 2a). This door has no
		// destKind field, so infer it from the request: an SFTP block → sftp, an
		// absolute URL → a local repo path, anything else unsupported. Read-only,
		// fail-soft — a listing failure is folded into the message, never returned
		// in its place. The door does NOT auto-repair (that is Part 2b); it only
		// counts.
		var keys *repoKeyListing
		var listErr error
		if classifyRepoProbe(lower) == repoProbeKeyConfigMismatch {
			kind := ""
			switch {
			case p.SFTP != nil && p.SFTP.Host != "":
				kind = backup.KindSFTP
			case strings.HasPrefix(p.URL, "/"):
				kind = backup.KindLocal
			}
			if ids, lerr := listRepoKeys(ctx, kind, p.URL, p.SFTP, extraEnv); lerr != nil {
				listErr = lerr
			} else {
				keys = &repoKeyListing{ids: ids, named: mismatchKeyID(lower)}
			}
		}
		return backupDestTestFailureResult(p.URL, backup.DefaultPasswordFile, stderrStr, err, keys, listErr), nil
	}
	return backupDestTestResult{
		Status:        "ok",
		StdoutPreview: firstNonEmptyLine(string(stdout)),
	}, nil
}

// backupDestTestFailureResult maps a non-missing probe failure to a test result.
// A repository that exists but cannot be opened (repoProbeUnopenable /
// repoProbeKeyConfigMismatch) gets the shared actionable message in Detail; any
// other failure surfaces the raw error. The raw restic stderr is always kept in
// Stderr. Pure — no restic call — so it is unit-testable from stderr fixtures.
func backupDestTestFailureResult(url, passwordFile, stderrStr string, probeErr error, keys *repoKeyListing, listErr error) backupDestTestResult {
	lower := strings.ToLower(stderrStr)
	switch cls := classifyRepoProbe(lower); cls {
	case repoProbeUnopenable, repoProbeKeyConfigMismatch:
		return backupDestTestResult{
			Status: "error",
			Detail: repoUnopenableMessage(cls, url, passwordFile, lower, keys, listErr),
			Stderr: stderrStr,
		}
	default:
		return backupDestTestResult{
			Status: "error",
			Detail: probeErr.Error(),
			Stderr: stderrStr,
		}
	}
}

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if t != "" {
			return t
		}
	}
	return ""
}

func init() {
	Default.Register("backup.dest.test", backupDestTestHandler)
}
