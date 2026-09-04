// Package filesops is the transport-neutral core of the scoped file manager.
//
// The REST handlers (internal/api/files.go) and the operator CLI
// (cmd/server/files_cmd.go) both drive the SAME panel-agent files.* verbs with
// the SAME params. Before this package each adapter hand-built those calls —
// the REST side from typed structs, the CLI side from inline map[string]any —
// so the two wire contracts could drift silently, and the pre-flight validation
// (rename/move/copy target rules) and reply decoding were copy-pasted with
// subtle differences.
//
// filesops owns the parts that MUST be identical across adapters:
//
//   - the agent verb strings (Method* constants),
//   - the typed request params for every non-upload verb (see params.go), whose
//     JSON tags are drift-guarded against panel-agent by params_test.go,
//   - the shared pre-flight validation and destination derivation for rename,
//     move, copy and archive (below),
//   - the reply decoders that fail closed on a malformed agent response and,
//     for a full-content read, on a truncated one (see decode.go).
//
// It is deliberately transport-agnostic: no net/http, no cobra, no os.Exit, no
// audit, no user resolution. Adapters own everything that legitimately differs
// between them — HTTP streaming/rendering and status codes, CLI local-file IO
// and audit, per-call timeouts (execution policy), and the upload/ingest path,
// which stays adapter-side by design.
package filesops

import (
	"errors"
	"path/filepath"
	"strings"
)

// Agent verb strings. Single-owned here so the REST and CLI adapters cannot
// drift a literal apart from panel-agent's registered command name.
const (
	MethodList         = "files.list"
	MethodRead         = "files.read"
	MethodStat         = "files.stat"
	MethodDu           = "files.du"
	MethodMkdir        = "files.mkdir"
	MethodWrite        = "files.write"
	MethodRename       = "files.rename"
	MethodMove         = "files.move"
	MethodCopy         = "files.copy"
	MethodCopyStart    = "files.copy.start"
	MethodChmod        = "files.chmod"
	MethodDelete       = "files.delete"
	MethodArchive      = "files.archive"
	MethodExtract      = "files.extract"
	MethodExtractStart = "files.extract.start"
)

// Scope is the resolved target-user identity every files.* verb carries.
// Adapters resolve it — REST from verified claims (and the GH #1184 admin gate,
// which sets AdminRoot to switch the agent to the root scope + deny-list), the
// CLI from --user — and hand it in. filesops never resolves a user itself.
type Scope struct {
	UserID    string
	Username  string
	AdminRoot bool
}

// Validation errors returned by the target helpers below. They are typed so an
// adapter maps each to its own transport error (the REST handler to a 400 with
// a stable code, the CLI to a non-zero exit with a message) — filesops emits no
// HTTP status and no CLI text of its own.
var (
	// ErrNewNameNotBare rejects a rename target that is not a single path
	// segment: a separator, or "." / ".." which would escape the parent dir.
	ErrNewNameNotBare = errors.New("new_name must be a single path segment")
	// ErrDestDirTraversal rejects a move/copy destination containing "..".
	ErrDestDirTraversal = errors.New(`dest_dir must not contain ".."`)
	// ErrNoPaths rejects an archive request with no paths.
	ErrNoPaths = errors.New("at least one path is required")
)

// ValidateNewName enforces that a rename target is a bare name — no path
// separators (either slash), and not "." or "..". This is the union of the two
// adapters' historical checks: the REST handler already rejected all four, the
// CLI only rejected a forward slash, so a CLI rename to "..", "." or a
// backslash name used to reach the agent (which rejected it out of scope). The
// agent remains authoritative; this is the pre-flight, defense-in-depth layer.
func ValidateNewName(name string) error {
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return ErrNewNameNotBare
	}
	return nil
}

// ValidateDestDir enforces that a move/copy destination directory does not
// contain "..". The agent WriteScopes the resolved destination regardless; this
// keeps a client from coercing a move into the docroot's parent with a bare
// "..".
func ValidateDestDir(dir string) error {
	if strings.Contains(dir, "..") {
		return ErrDestDirTraversal
	}
	return nil
}

// RenameTarget validates newName and derives the agent's new_path — the rename
// stays in the source's own directory, so the target is Join(dir(path),
// newName). Both adapters call this so the validation and the derivation have
// one owner.
func RenameTarget(path, newName string) (string, error) {
	if err := ValidateNewName(newName); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), newName), nil
}

// MoveTarget validates destDir and derives the agent's new_path for a move —
// the source keeps its basename inside the destination directory.
func MoveTarget(path, destDir string) (string, error) {
	if err := ValidateDestDir(destDir); err != nil {
		return "", err
	}
	return filepath.Join(destDir, filepath.Base(path)), nil
}

// CopyTarget validates destDir and derives the agent's dst_path for a copy —
// same rule as MoveTarget; kept as a distinct name so call sites read clearly.
func CopyTarget(path, destDir string) (string, error) {
	if err := ValidateDestDir(destDir); err != nil {
		return "", err
	}
	return filepath.Join(destDir, filepath.Base(path)), nil
}

// ValidateArchivePaths rejects an archive request with no paths.
func ValidateArchivePaths(paths []string) error {
	if len(paths) == 0 {
		return ErrNoPaths
	}
	return nil
}
