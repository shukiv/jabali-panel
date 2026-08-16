// Package backup — restic copy support for M30.1 (ADR-0078).
//
// `restic copy` mirrors snapshots from one repo (source) to another
// (target). The target repo can use a different password but v1 reuses
// /etc/jabali-panel/restic-repo.password for both — keeps the wire
// model simple. Per-destination passwords are M30.2.
//
// Backend env vars (AWS_*, B2_*, AZURE_*, GOOGLE_APPLICATION_CREDENTIALS,
// etc.) are loaded from a 0600 root:root env file and passed through
// exec.Cmd.Env. The DB only stores the file pointer.
package backup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// CopyOpts controls one `restic copy` invocation.
type CopyOpts struct {
	// FromRepo + FromPasswordFile are the SOURCE — typically the local
	// repo at /var/lib/jabali-backups/repo.
	FromRepo         string
	FromPasswordFile string

	// ToRepo + ToPasswordFile are the DESTINATION — restic URL form
	// (s3:…, sftp:…, b2:…, etc.) plus the dest's password file.
	ToRepo         string
	ToPasswordFile string

	// Tags filters which snapshots to copy. The scheduler always passes
	// `job-id=<ULID>` so a copy job mirrors only the snapshots from one
	// backup run, not the entire repo.
	Tags []Tag

	// SnapshotIDs (optional) explicitly copies a list of snapshots
	// instead of the tag-based filter. Used by retry paths that already
	// know which snapshots are missing on the dest.
	SnapshotIDs []string

	// ExtraOptions are restic `-o key=value` flags applied to the
	// destination side (e.g. `sftp.command=ssh -i /root/.ssh/id_x ...`).
	// Each entry is the bare `key=value` pair; the wrapper prepends `-o`.
	ExtraOptions []string
}

// LoadEnvFile parses a KEY=VALUE env file (one per line, # comments
// allowed, no shell expansion). Returns the lines as `KEY=VALUE`
// strings suitable for exec.Cmd.Env. Missing file = empty result + nil
// error so a Local destination passes through unchanged.
func LoadEnvFile(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path) //nolint:gosec // path comes from admin DB, mode-checked at config time
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open env file %s: %w", path, err)
	}
	defer f.Close()
	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			return nil, fmt.Errorf("env file %s: line %q missing '='", path, line)
		}
		out = append(out, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}
	return out, nil
}

// Copy runs `restic copy` from the configured source to the destination.
// Reuses the package's Runner abstraction so tests can intercept.
//
// CommandLine: `restic --repo <FromRepo> --password-file <FromPwFile> \
//                copy --from-repo=<repo> ...` is the older syntax;
//                modern restic uses `restic -r <to> --password-file=<to-pw>
//                copy --from-repo=<from> --from-password-file=<from-pw>`.
// We use the modern form: source is the --from-* set; destination is
// the primary --repo + --password-file.
func Copy(ctx context.Context, runner Runner, opts CopyOpts, extraEnv []string) ([]byte, []byte, error) {
	if opts.FromRepo == "" || opts.FromPasswordFile == "" {
		return nil, nil, errors.New("copy: FromRepo + FromPasswordFile required")
	}
	if opts.ToRepo == "" || opts.ToPasswordFile == "" {
		return nil, nil, errors.New("copy: ToRepo + ToPasswordFile required")
	}
	if runner == nil {
		runner = realRunner{}
	}
	args := []string{
		"--repo", opts.ToRepo,
		"--password-file", opts.ToPasswordFile,
	}
	for _, opt := range opts.ExtraOptions {
		if opt == "" {
			continue
		}
		args = append(args, "-o", opt)
	}
	args = append(args, "copy",
		"--from-repo", opts.FromRepo,
		"--from-password-file", opts.FromPasswordFile,
	)
	// Comma-joined = AND (see Client.Snapshots): restic treats repeated
	// --tag flags as OR groups, and a copy filter must match ALL tags.
	if len(opts.Tags) > 0 {
		args = append(args, "--tag", JoinTagsAND(opts.Tags))
	}
	args = append(args, opts.SnapshotIDs...)

	// Merge process env + caller's extra env. Caller env wins on dup
	// keys (right-most prevails per exec convention).
	env := append(os.Environ(), extraEnv...)

	stdout, stderr, err := runner.Run(ctx, "restic", args, env, nil)
	if err != nil {
		return stdout, stderr, fmt.Errorf("restic copy: %w (stderr: %s)", err, strings.TrimSpace(string(stderr)))
	}
	return stdout, stderr, nil
}

// InitRemote initializes a destination repo at opts.ToRepo using the
// supplied password file + env. Used by the `Test connection` REST
// endpoint when the admin clicks the test button on a brand-new
// destination.
func InitRemote(ctx context.Context, runner Runner, repo, passwordFile string, extraEnv, extraOptions []string) ([]byte, []byte, error) {
	if runner == nil {
		runner = realRunner{}
	}
	args := []string{
		"--repo", repo,
		"--password-file", passwordFile,
	}
	for _, opt := range extraOptions {
		if opt == "" {
			continue
		}
		args = append(args, "-o", opt)
	}
	args = append(args, "init")
	env := append(os.Environ(), extraEnv...)
	stdout, stderr, err := runner.Run(ctx, "restic", args, env, nil)
	if err != nil {
		return stdout, stderr, fmt.Errorf("restic init: %w (stderr: %s)", err, strings.TrimSpace(string(stderr)))
	}
	return stdout, stderr, nil
}

// SnapshotsRemote runs `restic snapshots` on a remote, used by the
// test-connection endpoint to verify creds against an existing repo.
func SnapshotsRemote(ctx context.Context, runner Runner, repo, passwordFile string, extraEnv, extraOptions []string) ([]byte, []byte, error) {
	if runner == nil {
		runner = realRunner{}
	}
	args := []string{
		"--repo", repo,
		"--password-file", passwordFile,
	}
	for _, opt := range extraOptions {
		if opt == "" {
			continue
		}
		args = append(args, "-o", opt)
	}
	args = append(args, "snapshots", "--json")
	env := append(os.Environ(), extraEnv...)
	stdout, stderr, err := runner.Run(ctx, "restic", args, env, nil)
	if err != nil {
		return stdout, stderr, fmt.Errorf("restic snapshots: %w (stderr: %s)", err, strings.TrimSpace(string(stderr)))
	}
	return stdout, stderr, nil
}
