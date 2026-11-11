package commands

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// migrationHostKeyFPRe is a SHA256 host-key fingerprint as the panel stores
// it: "SHA256:<43 base64 chars>", optionally with the prefix or the padding
// left off.
var migrationHostKeyFPRe = regexp.MustCompile(`^(SHA256:)?[A-Za-z0-9+/]{43}=?$`)

// migrationKeyscan lists the source's host keys (known_hosts lines). A var so
// tests can feed its output.
var migrationKeyscan = func(ctx context.Context, host string, port int) ([]byte, error) {
	sub, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return execCommandContext(sub, "ssh-keyscan", "-T", "10", "-p", strconv.Itoa(port),
		"-t", "ed25519,ecdsa,rsa", host).Output()
}

func normalizeMigrationFP(fp string) string {
	return strings.TrimRight(strings.TrimPrefix(strings.TrimSpace(fp), "SHA256:"), "=")
}

// pinMigrationKnownHosts makes the per-job known_hosts hold only the source
// host key whose SHA256 fingerprint is expectedFP, and returns the
// HostKeyAlgorithms value for that key's type. ssh-keyscan only learns the
// public key; the copy's own handshake then has to prove the source holds
// the private half, with StrictHostKeyChecking=yes. When no key the source
// presents matches, nothing is written and the call fails, naming each key
// the source presented.
func pinMigrationKnownHosts(ctx context.Context, host string, port int, knownHostsPath, expectedFP string) (string, *agentwire.AgentError) {
	if !migrationHostKeyFPRe.MatchString(strings.TrimSpace(expectedFP)) {
		return "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "expected_host_key must be a SHA256 host-key fingerprint"}
	}
	// The host is an argument of its own here, not part of user@host.
	if !validMailShadowHost(host) {
		return "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "host must be a host name or an IP address"}
	}
	out, err := migrationKeyscan(ctx, host, port)
	if err != nil && len(out) == 0 {
		return "", &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("could not read the source's host keys (ssh-keyscan): %v", err)}
	}
	want := normalizeMigrationFP(expectedFP)
	hostSpec := knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))
	var lines, algos, presented []string
	rest := out
	for len(rest) > 0 {
		_, _, key, _, next, perr := ssh.ParseKnownHosts(rest)
		if perr != nil {
			break
		}
		rest = next
		fp := ssh.FingerprintSHA256(key)
		if normalizeMigrationFP(fp) != want {
			presented = append(presented, key.Type()+" "+fp)
			continue
		}
		lines = append(lines, knownhosts.Line([]string{hostSpec}, key))
		if key.Type() == ssh.KeyAlgoRSA {
			algos = append(algos, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		} else {
			algos = append(algos, key.Type())
		}
	}
	if len(lines) == 0 {
		if len(presented) == 0 {
			return "", &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
				Message: "the source presented no host keys to ssh-keyscan; the pinned host key can't be checked"}
		}
		return "", &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("none of the source's host keys matches the pinned fingerprint %s; it presented %s",
				expectedFP, strings.Join(presented, ", "))}
	}
	tmp, err := os.CreateTemp(filepath.Dir(knownHostsPath), ".known_hosts-*")
	if err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "known_hosts: " + err.Error()}
	}
	_, werr := tmp.WriteString(strings.Join(lines, "\n") + "\n")
	if cerr := tmp.Chmod(0o600); werr == nil {
		werr = cerr
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), knownHostsPath)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "known_hosts: " + werr.Error()}
	}
	return strings.Join(algos, ","), nil
}

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
//
// expectedFP is the job's pinned host-key fingerprint ("" when the operator
// pinned none). With a pin, the per-job known_hosts is rewritten to hold only
// the source key that matches it (pinMigrationKnownHosts) and ssh runs with
// StrictHostKeyChecking=yes, so the copy talks only to the pinned key.
// Without one, behaviour is unchanged: accept-new on the per-job file.
func loadMigrationSSHTransport(ctx context.Context, secretPath, host string, port int, expectedFP string) (*migrationSSHTransport, *agentwire.AgentError) {
	if port < 1 || port > 65535 {
		port = 22
	}
	knownHosts := strings.TrimSuffix(secretPath, ".env") + ".known_hosts"
	hostKeyOpt := "-o StrictHostKeyChecking=accept-new"
	if expectedFP != "" {
		algos, aerr := pinMigrationKnownHosts(ctx, host, port, knownHosts, expectedFP)
		if aerr != nil {
			return nil, aerr
		}
		hostKeyOpt = "-o StrictHostKeyChecking=yes -o HostKeyAlgorithms=" + algos
	}

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

	// Host-key checking (Gitea #461) uses the per-job known_hosts beside the
	// secret file — never the old blind StrictHostKeyChecking=no + /dev/null.
	// With a pin it holds only the pinned key (strict, above). Without one,
	// accept-new: the first copy records the source's key and a later copy
	// in the same job refuses a different one. panel-api writes no
	// known_hosts of its own, so without a pin the first key is trusted.
	sshOpt := hostKeyOpt + " -o UserKnownHostsFile=" + knownHosts
	sshOpt += " -p " + strconv.Itoa(port)
	if keyTmp != "" {
		sshOpt += " -i " + keyTmp + " -o IdentitiesOnly=yes"
	}
	return &migrationSSHTransport{SSHOpt: sshOpt, SSHPass: sshPass, keyTmp: keyTmp}, nil
}
