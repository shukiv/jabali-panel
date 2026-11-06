package api

import (
	"errors"
	"io"
	"net/http"
	"os"
)

// appendRestoreChunk writes one chunk of a staged restore archive. offset must
// be where the staged file ends now (0 starts a new upload), so chunks land in
// order: no chunk can leave a hole, and no restarted upload can keep a stale
// tail from an earlier one. The total stays under maxRestoreUploadBytes. It is
// the rule uploadintake.Write and the database chunk upload already follow.
//
// On success it returns the bytes written and status 0. Otherwise it returns
// the status and body the handler should answer with.
func appendRestoreChunk(path string, offset int64, body io.Reader) (int64, int, map[string]any) {
	flags := os.O_WRONLY
	if offset == 0 {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0o640)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, http.StatusConflict, map[string]any{"error": "upload_not_found"}
		}
		return 0, http.StatusInternalServerError, map[string]any{"error": "open_staging_failed"}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, http.StatusInternalServerError, map[string]any{"error": "open_staging_failed"}
	}
	if fi.Size() != offset {
		return 0, http.StatusConflict, map[string]any{"error": "bad_offset", "expected": fi.Size()}
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, http.StatusInternalServerError, map[string]any{"error": "seek_failed"}
	}
	// LimitReader bounds the write so a chunk can't push the file past the cap;
	// the extractor enforces the real decompression-bomb budget later.
	remaining := maxRestoreUploadBytes - offset
	written, werr := io.Copy(f, io.LimitReader(body, remaining))
	if werr != nil {
		// Back to the last whole chunk, so the next try at this offset fits.
		_ = f.Truncate(offset)
		return 0, http.StatusBadRequest, map[string]any{"error": "write_failed", "detail": werr.Error()}
	}
	// If we wrote exactly up to the cap AND the body still has bytes, the chunk
	// would have been silently truncated — reject instead so no partial archive
	// is ever restored.
	if written == remaining {
		var probe [1]byte
		if n, _ := body.Read(probe[:]); n > 0 {
			_ = os.Remove(path)
			return 0, http.StatusRequestEntityTooLarge, map[string]any{"error": "archive_too_large"}
		}
	}
	return written, 0, nil
}
