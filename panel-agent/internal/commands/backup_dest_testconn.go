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
			_, initStderr, initErr := backup.InitRemote(
				ctx,
				nil,
				p.URL,
				backup.DefaultPasswordFile,
				extraEnv,
				bkResticOptions(p.SFTP),
			)
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
		return backupDestTestResult{
			Status: "error",
			Detail: err.Error(),
			Stderr: stderrStr,
		}, nil
	}
	return backupDestTestResult{
		Status:        "ok",
		StdoutPreview: firstNonEmptyLine(string(stdout)),
	}, nil
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
