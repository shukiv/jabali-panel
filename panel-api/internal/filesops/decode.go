package filesops

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Reply decoders. Every decoder fails closed: a malformed agent response is an
// error, never a zero-valued success. This closes a real gap — the CLI's
// files.list used to `_ = json.Unmarshal(...)` and print an empty listing (exit
// 0) when the agent returned an error blob, so an agent failure looked like an
// empty directory. Both adapters now decode through here.

// ListEntry is one directory entry in a files.list reply.
type ListEntry struct {
	Name       string `json:"name"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	Mode       string `json:"mode"`
	ModTime    string `json:"mod_time"`
	IsSymlink  bool   `json:"is_symlink"`
	HasSubdirs bool   `json:"has_subdirs,omitempty"`
}

// ListResult is a decoded files.list reply.
type ListResult struct {
	Path    string      `json:"path"`
	Entries []ListEntry `json:"entries"`
}

// ReadResult is a decoded files.read reply.
type ReadResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	ContentB64 string `json:"content_b64"`
	IsBinary   bool   `json:"is_binary"`
	Size       int64  `json:"size"`
	Truncated  bool   `json:"truncated"`
	MimeType   string `json:"mime_type"`
}

// ArchiveResult is a decoded files.archive reply.
type ArchiveResult struct {
	ArchivePath string `json:"archive_path"`
	Size        int64  `json:"size"`
}

// StatResult is a decoded files.stat reply. Mirrors the agent's stat output.
type StatResult struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Mode      string `json:"mode"`
	IsDir     bool   `json:"is_dir"`
	ModTime   string `json:"mod_time"`
	IsSymlink bool   `json:"is_symlink"`
}

// DuEntry is one child of a files.du reply (a per-entry byte total).
type DuEntry struct {
	Name       string `json:"name"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	HasSubdirs bool   `json:"has_subdirs"`
}

// DuResult is a decoded files.du reply. Mirrors the agent's filesDuResponse.
type DuResult struct {
	Path    string    `json:"path"`
	Total   int64     `json:"total"`
	Entries []DuEntry `json:"entries"`
}

// ExtractResult is a decoded synchronous files.extract reply. It has no
// non-omittable identity field to guard on: the agent returns dest as the
// caller's raw dest param (files_extract.go), which is empty for the common
// "extract into the archive's parent" action, and extracted/skipped are
// legitimately 0 for an empty archive. So DecodeExtract fails closed on a
// malformed body only (like DecodeList), never on a field-presence check that
// would false-fail a real default-dest extract.
type ExtractResult struct {
	Dest      string `json:"dest"`
	Extracted int    `json:"extracted"`
	Skipped   int    `json:"skipped"`
}

// JobStartResult is a decoded files.extract.start / files.copy.start reply: the
// id of the background job the agent kicked off. Both start verbs return the
// same {job_id} shape.
type JobStartResult struct {
	JobID string `json:"job_id"`
}

// JobStatusResult is a decoded files.job.status reply. Mirrors the agent's
// fileJobSnapshot. Result carries the finished job's verb-specific payload
// (e.g. an ExtractResult) and is left as raw JSON here.
type JobStatusResult struct {
	JobID     string          `json:"job_id"`
	Status    string          `json:"status"`
	Done      int64           `json:"done"`
	Total     int64           `json:"total"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	StartedAt string          `json:"started_at"`
}

// ErrTruncated reports that a full-content read came back truncated because the
// file exceeds the agent's read cap. Callers that need the whole file (download,
// CLI read/download) turn this into a failure so a partial file is never written
// or served as if complete; a preview, which is meant to be partial, does not
// call RequireComplete and so never sees it.
var ErrTruncated = errors.New("file exceeds the read limit and was truncated by the agent; fetch it over SFTP/SSH instead")

// ErrNoArchivePath reports that a files.archive reply decoded cleanly but named
// no archive — an empty archive_path, which cannot be streamed back.
var ErrNoArchivePath = errors.New("agent did not return an archive path")

// ErrNoStatMode reports that a files.stat reply decoded cleanly but carried no
// file mode. Mode is the field a successful stat can never omit: the agent sets
// it from FileInfo.Mode().String(), which is never the empty string for a real
// file. An empty mode therefore means the body was not a stat result at all — an
// agent error blob, or a JSON null, decoding into a zero-valued struct. (Path is
// only a normalized echo of the caller's input, so it is the weaker identity
// field to hang this check on.)
var ErrNoStatMode = errors.New("agent did not return a file mode for the stat")

// ErrNoDuPath reports that a files.du reply decoded cleanly but carried no path.
// Path is the field a successful du can never omit: the agent sets it to the
// resolved canonical directory it measured. Total and Entries are NOT usable as
// the identity field — a du of an empty directory legitimately returns total 0
// and no entries, so guarding on either would false-fail a real empty dir. An
// empty path therefore means the body was not a du result at all (an agent error
// blob, or a JSON null decoding into a zero-valued struct).
var ErrNoDuPath = errors.New("agent did not return a path for the disk-usage reply")

// ErrNoJobID reports that a files.extract.start / files.copy.start reply decoded
// cleanly but carried no job id — an id the caller must have to poll the job.
var ErrNoJobID = errors.New("agent did not return a job id")

// ErrNoJobStatus reports that a files.job.status reply decoded cleanly but
// carried no status. Every real job snapshot has a non-empty status
// (queued/running/done/failed); an empty one means the body was not a snapshot.
var ErrNoJobStatus = errors.New("agent did not return a status for the job")

// DecodeList decodes a files.list reply, failing closed on a malformed body.
func DecodeList(raw []byte) (ListResult, error) {
	var r ListResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return ListResult{}, fmt.Errorf("decode files.list reply: %w", err)
	}
	return r, nil
}

// DecodeRead decodes a files.read reply, failing closed on a malformed body.
// Truncation is reported via the Truncated field; callers needing a complete
// file follow up with RequireComplete.
func DecodeRead(raw []byte) (ReadResult, error) {
	var r ReadResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return ReadResult{}, fmt.Errorf("decode files.read reply: %w", err)
	}
	return r, nil
}

// DecodeArchive decodes a files.archive reply, failing closed on a malformed
// body or an empty archive_path.
func DecodeArchive(raw []byte) (ArchiveResult, error) {
	var r ArchiveResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return ArchiveResult{}, fmt.Errorf("decode files.archive reply: %w", err)
	}
	if r.ArchivePath == "" {
		return ArchiveResult{}, ErrNoArchivePath
	}
	return r, nil
}

// DecodeStat decodes a files.stat reply, failing closed on a malformed body or a
// reply that carries no file mode (see ErrNoStatMode).
func DecodeStat(raw []byte) (StatResult, error) {
	var r StatResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return StatResult{}, fmt.Errorf("decode files.stat reply: %w", err)
	}
	if r.Mode == "" {
		return StatResult{}, ErrNoStatMode
	}
	return r, nil
}

// DecodeDu decodes a files.du reply, failing closed on a malformed body or a
// reply that carries no path (see ErrNoDuPath).
func DecodeDu(raw []byte) (DuResult, error) {
	var r DuResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return DuResult{}, fmt.Errorf("decode files.du reply: %w", err)
	}
	if r.Path == "" {
		return DuResult{}, ErrNoDuPath
	}
	return r, nil
}

// DecodeExtract decodes a synchronous files.extract reply, failing closed on a
// malformed body. See ExtractResult for why there is no field-presence guard
// here (a valid default-dest extract carries an empty dest).
func DecodeExtract(raw []byte) (ExtractResult, error) {
	var r ExtractResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return ExtractResult{}, fmt.Errorf("decode files.extract reply: %w", err)
	}
	return r, nil
}

// DecodeJobStart decodes a files.extract.start / files.copy.start reply, failing
// closed on a malformed body or a missing job id (see ErrNoJobID).
func DecodeJobStart(raw []byte) (JobStartResult, error) {
	var r JobStartResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return JobStartResult{}, fmt.Errorf("decode files job-start reply: %w", err)
	}
	if r.JobID == "" {
		return JobStartResult{}, ErrNoJobID
	}
	return r, nil
}

// DecodeJobStatus decodes a files.job.status reply, failing closed on a
// malformed body or a reply that carries no status (see ErrNoJobStatus).
func DecodeJobStatus(raw []byte) (JobStatusResult, error) {
	var r JobStatusResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return JobStatusResult{}, fmt.Errorf("decode files.job.status reply: %w", err)
	}
	if r.Status == "" {
		return JobStatusResult{}, ErrNoJobStatus
	}
	return r, nil
}

// RequireComplete returns ErrTruncated when the read came back truncated. It is
// the single owner of the "a full read must not be truncated" rule, shared by
// every full-content read path so they cannot drift.
func (r ReadResult) RequireComplete() error {
	if r.Truncated {
		return ErrTruncated
	}
	return nil
}

// Bytes returns the file's raw bytes from a read reply: the base64 payload when
// the agent sent one (binary, or text that is not valid UTF-8), otherwise the
// plain Content. Keying on the base64 payload's presence — rather than the
// is_binary flag — is the robust choice when the two ever disagree.
func (r ReadResult) Bytes() ([]byte, error) {
	if r.ContentB64 != "" {
		b, err := base64.StdEncoding.DecodeString(r.ContentB64)
		if err != nil {
			return nil, fmt.Errorf("decode files.read content_b64: %w", err)
		}
		return b, nil
	}
	return []byte(r.Content), nil
}
