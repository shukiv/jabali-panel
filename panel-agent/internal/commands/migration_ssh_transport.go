package commands

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// migrationSSHTransport is the SSH side of a copy from a migration source:
// the options for rsync's `-e "ssh <SSHOpt>"` and, for a password login, the
// value to hand sshpass through SSHPASS. Close removes the temporary key file.
type migrationSSHTransport struct {
	SSHOpt  string
	SSHPass string
	keyTmp  string
}

// Close removes the decoded private key's temp file, if one was written.
func (t *migrationSSHTransport) Close() {
	if t != nil && t.keyTmp != "" {
		_ = os.Remove(t.keyTmp)
	}
}

// validateMigrationSecretPath returns the cleaned secret_path, which must be
// <job-id>.env directly under migrationSecretsBaseDir.
//
// A bare strings.HasPrefix is not enough: it happily accepts
// "/etc/jabali-panel/migration-secrets/../../shadow", and the file is read
// below with os.ReadFile as root. Clean the path first, then require the
// directory to match exactly and the basename to be a validated job id --
// the same shape migration_admin_run.go already enforces, which takes a
// job_id and builds the path itself rather than trusting one.
//
// Not reachable today: every caller builds this from a server-generated
// genULID() job id. This is the boundary check CONVENTIONS.md asks for
// regardless of who the caller is, on a root-side arbitrary-file read.
func validateMigrationSecretPath(secretPath string) (string, *agentwire.AgentError) {
	cleanSecret := filepath.Clean(secretPath)
	secretBase := strings.TrimSuffix(filepath.Base(cleanSecret), ".env")
	if filepath.Dir(cleanSecret) != migrationSecretsBaseDir ||
		!strings.HasSuffix(cleanSecret, ".env") ||
		!migrationJobIDRe.MatchString(secretBase) {
		return "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "secret_path must be <job-id>.env directly under " + migrationSecretsBaseDir}
	}
	return cleanSecret, nil
}

// loadMigrationSSHTransport reads SSH auth from a secret env-file already
// checked by validateMigrationSecretPath and builds the ssh options. The
// caller must Close the result.
func loadMigrationSSHTransport(secretPath string, port int) (*migrationSSHTransport, *agentwire.AgentError) {
	// Resolve SSH auth from the secret env-file. Two recognised keys
	// (same shape cpanel/da/hestia Discoverer loadSecret reads):
	//   SSH_PASSWORD=<plain>
	//   SSH_PRIVATE_KEY_B64=<base64-PEM>
	secretBytes, sErr := os.ReadFile(secretPath)
	if sErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("read secret %s: %v", secretPath, sErr)}
	}
	var sshPass string
	var keyTmp string
	for _, line := range strings.Split(string(secretBytes), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "SSH_PASSWORD":
			sshPass = v
		case "SSH_PRIVATE_KEY_B64":
			// Stash the decoded key in a 0600 tempfile rsync can ssh -i.
			b64 := strings.TrimSpace(v)
			tmp, tmpErr := os.CreateTemp("", "jabali-migrate-key-*")
			if tmpErr != nil {
				continue
			}
			_ = os.Chmod(tmp.Name(), 0o600)
			// Decode in-process. Previously this shelled out to
			// `bash -c "echo $b64 | base64 -d > tmp"`, interpolating the
			// secret-file value into a shell string — a shell-injection
			// sink if the value isn't pure base64. Go's decoder removes the
			// shell entirely.
			decoded, dErr := base64.StdEncoding.DecodeString(strings.Map(stripBase64Whitespace, b64))
			if dErr != nil {
				_ = tmp.Close()
				_ = os.Remove(tmp.Name())
				continue
			}
			if _, wErr := tmp.Write(decoded); wErr != nil {
				_ = tmp.Close()
				_ = os.Remove(tmp.Name())
				continue
			}
			_ = tmp.Close()
			keyTmp = tmp.Name()
		}
	}
	if sshPass == "" && keyTmp == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: "no SSH_PASSWORD or SSH_PRIVATE_KEY_B64 in secret file"}
	}

	// Host-key pinning (Gitea #461): consume the per-job known_hosts the
	// panel-api discover stage pinned (sibling of the secret file) and use
	// accept-new, so a key already pinned at discover is verified (a
	// mid-migration key flip is rejected) and an unpinned host is captured on
	// first use — never the old blind StrictHostKeyChecking=no + /dev/null.
	knownHosts := strings.TrimSuffix(secretPath, ".env") + ".known_hosts"
	sshOpt := "-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + knownHosts
	if port < 1 || port > 65535 {
		port = 22
	}
	sshOpt += " -p " + strconv.Itoa(port)
	if keyTmp != "" {
		sshOpt += " -i " + keyTmp + " -o IdentitiesOnly=yes"
	}
	return &migrationSSHTransport{SSHOpt: sshOpt, SSHPass: sshPass, keyTmp: keyTmp}, nil
}
