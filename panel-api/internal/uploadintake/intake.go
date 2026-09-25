// Package uploadintake is the Upload Intake module (JAB-365): the one place that
// owns how an upload is staged before the agent ingests it into a tenant tree.
//
// It owns the configured-cap resolution, the owner-scoped staging identity, the
// per-owner in-flight cap and byte budget, the secure append rules for chunked
// sessions, the files.ingest hand-off and the cleanup of a failed session. The
// HTTP File Manager and the CLI are adapters: they authenticate the caller,
// read the bytes and map the errors to their transport.
//
// Every staging file is Dir + "/jabali-upload-" + <owner tag> + "-" + <id>: a
// flat basename under the agent's files.ingest prefix gate, tagged with a hash
// of the owner id, so one owner can neither compute another owner's session
// path nor count against another owner's cap or budget.
package uploadintake

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Dir is the staging directory panel-api and the agent share. It lives outside
// /tmp because both units run with PrivateTmp=true; install.sh creates it
// (jabali:jabali 0750) and grants panel-api ReadWritePaths + an AppArmor rw rule
// on this exact path. A var only so tests can point it at t.TempDir().
var Dir = "/var/lib/jabali-uploads"

// basenamePrefix is the agent's files.ingest gate: it ingests only
// Dir + "/" + basenamePrefix + <flat name>.
const basenamePrefix = "jabali-upload-"

const (
	// MaxInFlight caps how many staging files one owner may hold at once, so
	// a single account cannot fill the service partition shared with MariaDB
	// and panel state (Gitea #425).
	MaxInFlight = 5
	// BudgetMultiple sets the per-owner staging byte budget as a multiple of
	// the per-upload cap (#425).
	BudgetMultiple = 2
	// DefaultMaxBytes is the per-upload cap when upload_max_size_mb is unset.
	DefaultMaxBytes int64 = 1024 * 1024 * 1024
)

var (
	// ErrTooManyUploads means the owner already holds MaxInFlight sessions.
	ErrTooManyUploads = errors.New("uploadintake: too many in-flight uploads")
	// ErrBudgetExceeded means the owner's staged bytes reached the budget.
	ErrBudgetExceeded = errors.New("uploadintake: staging budget exceeded")
	// ErrTooLarge means the upload exceeds the per-upload cap.
	ErrTooLarge = errors.New("uploadintake: upload exceeds the configured maximum")
	// ErrUploadNotFound means a later chunk names a session that does not
	// exist for this owner.
	ErrUploadNotFound = errors.New("uploadintake: upload session not found")
)

// BadOffsetError means a chunk's offset is not the session's current size: a
// hole, a mid-file overwrite or a replay. Expected is where to resume.
type BadOffsetError struct{ Expected int64 }

func (e *BadOffsetError) Error() string {
	return fmt.Sprintf("uploadintake: chunk offset must equal the staged size %d", e.Expected)
}

// Limits are the per-upload cap and the per-owner staging budget.
type Limits struct {
	MaxBytes int64
	Budget   int64
}

// LimitsFor resolves the limits from server_settings.upload_max_size_mb; 0
// means unset and selects DefaultMaxBytes.
func LimitsFor(configuredMB uint32) Limits {
	max := DefaultMaxBytes
	if configuredMB > 0 {
		max = int64(configuredMB) * 1024 * 1024
	}
	return Limits{MaxBytes: max, Budget: BudgetMultiple * max}
}

// Prefix is the path prefix every staging file starts with.
func Prefix() string { return Dir + "/" + basenamePrefix }

// EnsureDir creates Dir if it is missing. It is a no-op on an installed box,
// where install.sh created it; tests and fresh environments rely on it.
func EnsureDir() error { return os.MkdirAll(Dir, 0o750) }

// OwnerTag is a stable, non-secret per-owner basename component. Distinct
// owners get distinct tags, so an owner's sessions can be globbed without any
// in-memory state.
func OwnerTag(ownerID string) string {
	sum := sha256.Sum256([]byte("jabali-upload-user:" + ownerID))
	return hex.EncodeToString(sum[:6])
}

// ChunkPath is the staging path of a chunked session. It depends on the
// authenticated owner as well as the client's upload id, so an owner who
// learns another owner's upload id still cannot compute — and so cannot write
// into or read — that session (Gitea #426).
func ChunkPath(ownerID, uploadID string) string {
	sum := sha256.Sum256([]byte("jabali-upload:" + ownerID + ":" + uploadID))
	return Prefix() + OwnerTag(ownerID) + "-" + hex.EncodeToString(sum[:])
}

func singlePath(ownerID, id string) string {
	return Prefix() + OwnerTag(ownerID) + "-" + id
}

// Stats counts the owner's staging files and their bytes — chunked and
// single-shot alike. No shared state; it survives restarts.
func Stats(ownerID string) (count int, bytes int64) {
	matches, _ := filepath.Glob(Prefix() + OwnerTag(ownerID) + "-*")
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() {
			count++
			bytes += fi.Size()
		}
	}
	return count, bytes
}

// Admit reports whether the owner may start a new session: fewer than
// MaxInFlight staging files and staged bytes below the budget.
func Admit(ownerID string, l Limits) error {
	count, total := Stats(ownerID)
	if count >= MaxInFlight {
		return ErrTooManyUploads
	}
	if total >= l.Budget {
		return ErrBudgetExceeded
	}
	return nil
}

// Stage streams r into a new single-shot session for the owner and returns its
// path and size. The session is created O_EXCL; on any failure nothing is left
// staged. The caller hands the path to Ingest, or Discards it.
func Stage(ownerID string, r io.Reader, l Limits) (path string, written int64, err error) {
	if err := EnsureDir(); err != nil {
		return "", 0, err
	}
	if err := Admit(ownerID, l); err != nil {
		return "", 0, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", 0, err
	}
	path = singlePath(ownerID, hex.EncodeToString(id))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	written, err = io.Copy(f, io.LimitReader(r, l.MaxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		Discard(path)
		return "", 0, err
	}
	if written > l.MaxBytes {
		Discard(path)
		return "", 0, ErrTooLarge
	}
	if err := checkBudgetAfterWrite(ownerID, path, l); err != nil {
		return "", 0, err
	}
	return path, written, nil
}

// Append writes one chunk of the owner's session uploadID at offset and returns
// the session path and the bytes written. The first chunk (offset 0) creates the
// session O_EXCL, after Admit; retrying it reopens the session. A later chunk
// needs the session to exist, and every chunk's offset must equal the staged
// size: no holes, no overwrite, no cross-session injection (#426). A chunk that
// takes the session past the cap, or the owner past the budget, removes the
// session.
func Append(ownerID, uploadID string, offset int64, r io.Reader, l Limits) (path string, written int64, err error) {
	if err := EnsureDir(); err != nil {
		return "", 0, err
	}
	path = ChunkPath(ownerID, uploadID)
	if offset == 0 {
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			if err := Admit(ownerID, l); err != nil {
				return path, 0, err
			}
		}
	}

	flags := os.O_WRONLY
	if offset == 0 {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil && offset == 0 && errors.Is(err, os.ErrExist) {
		// Idempotent retry of the first chunk after a blip: the offset check
		// below decides.
		f, err = os.OpenFile(path, os.O_WRONLY, 0o600)
	}
	if err != nil {
		if offset > 0 && errors.Is(err, os.ErrNotExist) {
			return path, 0, ErrUploadNotFound
		}
		return path, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return path, 0, err
	}
	if offset != fi.Size() {
		f.Close()
		return path, 0, &BadOffsetError{Expected: fi.Size()}
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		f.Close()
		return path, 0, err
	}
	written, err = io.Copy(f, io.LimitReader(r, l.MaxBytes-offset+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		Discard(path)
		return path, 0, err
	}
	if offset+written > l.MaxBytes {
		Discard(path)
		return path, 0, ErrTooLarge
	}
	if err := checkBudgetAfterWrite(ownerID, path, l); err != nil {
		return path, 0, err
	}
	return path, written, nil
}

// checkBudgetAfterWrite removes the session just written when it took the
// owner above the budget. Reaching the budget exactly is allowed; the next new
// session is refused by Admit.
func checkBudgetAfterWrite(ownerID, path string, l Limits) error {
	if _, total := Stats(ownerID); total > l.Budget {
		Discard(path)
		return ErrBudgetExceeded
	}
	return nil
}

// Written is how many bytes the owner's chunked session holds, for a client
// resuming after a reload or a network blip. An owner can only see their own
// sessions: the path is derived from the owner.
func Written(ownerID, uploadID string) (int64, error) {
	fi, err := os.Stat(ChunkPath(ownerID, uploadID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrUploadNotFound
		}
		return 0, err
	}
	return fi.Size(), nil
}

// Discard removes a staging file the adapter will not ingest.
func Discard(path string) { _ = os.Remove(path) }
