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

// ErrTruncated reports that a full-content read came back truncated because the
// file exceeds the agent's read cap. Callers that need the whole file (download,
// CLI read/download) turn this into a failure so a partial file is never written
// or served as if complete; a preview, which is meant to be partial, does not
// call RequireComplete and so never sees it.
var ErrTruncated = errors.New("file exceeds the read limit and was truncated by the agent; fetch it over SFTP/SSH instead")

// ErrNoArchivePath reports that a files.archive reply decoded cleanly but named
// no archive — an empty archive_path, which cannot be streamed back.
var ErrNoArchivePath = errors.New("agent did not return an archive path")

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
