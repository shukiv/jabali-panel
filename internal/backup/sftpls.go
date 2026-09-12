// Package backup — SFTP remote-ls helper for M30.1 destinations (JAB-405 Part 2a).
//
// A repository corrupted by a concurrent-init race (two `restic init` on one
// empty repo → more than one key file but a single config only one key can
// decrypt) cannot be opened by restic at all — `restic key list` itself dies
// with the same `ciphertext verification failed`, because restic opens config
// before it lists keys. So to tell an operator whether a key/config mismatch is
// the race (move a key aside and retry) or a genuinely corrupt config (start
// fresh), the agent counts the key files by listing the repository's keys/
// directory DIRECTLY, below restic: `ssh user@host -- ls -1 <path>/keys` for an
// SFTP repo, os.ReadDir for a local one.
package backup

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// buildSSHConnArgs assembles the argv prefix shared by every remote ssh exec:
// `[sshpass -e] ssh [-i KEY -o IdentitiesOnly=yes] -o StrictHostKeyChecking=...
// [auth opts] [-p PORT] user@host`. The caller appends `--` and the remote
// command. Extracted from buildSSHMkdirArgs so mkdir and ls share one auth path;
// TestBuildSSHMkdirArgs_Pin holds the mkdir argv byte-for-byte across the
// extraction.
func buildSSHConnArgs(in SFTPInputs) []string {
	parts := []string{}
	if in.Auth == "password" {
		parts = append(parts, "sshpass", "-e")
	}
	parts = append(parts, "ssh")
	if in.Auth == "key" && in.KeyPath != "" {
		parts = append(parts, "-i", in.KeyPath, "-o", "IdentitiesOnly=yes")
	}
	parts = append(parts, "-o", "StrictHostKeyChecking=accept-new")
	if in.Auth == "password" {
		parts = append(parts, "-o", "PreferredAuthentications=password",
			"-o", "PubkeyAuthentication=no")
	} else {
		parts = append(parts, "-o", "BatchMode=yes")
	}
	if in.Port > 0 && in.Port != 22 {
		parts = append(parts, "-p", fmt.Sprintf("%d", in.Port))
	}
	parts = append(parts, fmt.Sprintf("%s@%s", in.User, in.Host))
	return parts
}

// buildSSHListArgs assembles the argv for `[conn] -- ls -1 <remotePath>`.
// remotePath is passed as a single argv element (no shell), so spaces or shell
// metacharacters in the repository path cannot split into extra arguments.
func buildSSHListArgs(in SFTPInputs, remotePath string) []string {
	return append(buildSSHConnArgs(in), "--", "ls", "-1", remotePath)
}

// ListRemoteSFTP runs `ssh user@host -- ls -1 <remotePath>` over the same auth
// path restic's sftp.command uses (sshpass for password, `-i <key>` for key
// auth, `-p N` for non-22 ports) and returns the raw combined output. It is
// READ-ONLY — it never creates, moves, or deletes anything. Callers parse the
// output for restic key-file names. On error the combined output is returned so
// the caller can fold a short diagnostic into a fail-soft message.
func ListRemoteSFTP(ctx context.Context, in SFTPInputs, remotePath string, extraEnv []string) ([]byte, error) {
	if in.Host == "" || in.User == "" {
		return nil, fmt.Errorf("sftp: host+user required")
	}
	args := buildSSHListArgs(in, remotePath)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = append(cmd.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("ssh ls -1 %s: %w (output: %s)",
			remotePath, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
