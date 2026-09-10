package filesops

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestDecodeListFailsClosed(t *testing.T) {
	// AC2: a malformed reply is an error, never an empty (zero-valued) success.
	if _, err := DecodeList([]byte(`{"error":"boom"`)); err == nil {
		t.Fatal("DecodeList(malformed) = nil error, want a decode error")
	}
	got, err := DecodeList([]byte(`{"path":"/home/shuki","entries":[{"name":"a.txt","size":3}]}`))
	if err != nil {
		t.Fatalf("DecodeList(valid): %v", err)
	}
	if got.Path != "/home/shuki" || len(got.Entries) != 1 || got.Entries[0].Name != "a.txt" {
		t.Fatalf("DecodeList decoded wrong: %+v", got)
	}
}

func TestDecodeReadAndTruncation(t *testing.T) {
	if _, err := DecodeRead([]byte(`not json`)); err == nil {
		t.Fatal("DecodeRead(malformed) = nil error, want a decode error")
	}
	full, err := DecodeRead([]byte(`{"path":"/f","content":"hello","truncated":false}`))
	if err != nil {
		t.Fatalf("DecodeRead(full): %v", err)
	}
	if err := full.RequireComplete(); err != nil {
		t.Fatalf("RequireComplete(full) = %v, want nil", err)
	}
	// AC4: a truncated full-content read fails via the single shared rule.
	trunc, err := DecodeRead([]byte(`{"path":"/f","content":"hel","truncated":true}`))
	if err != nil {
		t.Fatalf("DecodeRead(trunc): %v", err)
	}
	if err := trunc.RequireComplete(); !errors.Is(err, ErrTruncated) {
		t.Fatalf("RequireComplete(trunc) = %v, want ErrTruncated", err)
	}
}

func TestReadResultBytes(t *testing.T) {
	// Plain text content.
	r := ReadResult{Content: "hello"}
	b, err := r.Bytes()
	if err != nil || string(b) != "hello" {
		t.Fatalf("Bytes(text) = %q, %v", b, err)
	}
	// Base64 payload wins when present (binary / non-UTF-8 text).
	raw := []byte{0x00, 0xff, 0x10}
	rb := ReadResult{IsBinary: true, ContentB64: base64.StdEncoding.EncodeToString(raw)}
	b, err = rb.Bytes()
	if err != nil {
		t.Fatalf("Bytes(b64): %v", err)
	}
	if len(b) != 3 || b[0] != 0x00 || b[1] != 0xff || b[2] != 0x10 {
		t.Fatalf("Bytes(b64) decoded wrong: %v", b)
	}
	// Malformed base64 fails closed.
	if _, err := (ReadResult{ContentB64: "!!!not-b64!!!"}).Bytes(); err == nil {
		t.Fatal("Bytes(bad b64) = nil error, want a decode error")
	}
}

func TestDecodeArchive(t *testing.T) {
	if _, err := DecodeArchive([]byte(`{bad`)); err == nil {
		t.Fatal("DecodeArchive(malformed) = nil error, want a decode error")
	}
	// AC2: a clean decode with no archive_path still fails closed.
	if _, err := DecodeArchive([]byte(`{"size":10}`)); !errors.Is(err, ErrNoArchivePath) {
		t.Fatalf("DecodeArchive(empty path) = %v, want ErrNoArchivePath", err)
	}
	got, err := DecodeArchive([]byte(`{"archive_path":"/tmp/a.tar.gz","size":42}`))
	if err != nil {
		t.Fatalf("DecodeArchive(valid): %v", err)
	}
	if got.ArchivePath != "/tmp/a.tar.gz" || got.Size != 42 {
		t.Fatalf("DecodeArchive decoded wrong: %+v", got)
	}
}

func TestDecodeStat(t *testing.T) {
	// Malformed body fails closed with a decode error, never a zero success.
	if _, err := DecodeStat([]byte(`{`)); err == nil {
		t.Fatal("DecodeStat(malformed) = nil error, want a decode error")
	}
	// AC2, load-bearing: an agent error blob is valid JSON but not a stat — it
	// decodes to a zero-valued struct with an empty mode and must fail closed.
	if _, err := DecodeStat([]byte(`{"error":"boom"}`)); !errors.Is(err, ErrNoStatMode) {
		t.Fatalf("DecodeStat(error blob) = %v, want ErrNoStatMode", err)
	}
	// JSON null Unmarshals into a struct without error, leaving a zero value.
	// The mode guard — not Unmarshal — is what rejects it, so this row proves
	// the guard is load-bearing.
	if _, err := DecodeStat([]byte(`null`)); !errors.Is(err, ErrNoStatMode) {
		t.Fatalf("DecodeStat(null) = %v, want ErrNoStatMode", err)
	}
	got, err := DecodeStat([]byte(`{"path":"/home/shuki/f.txt","size":7,"mode":"-rw-r--r--","is_dir":false,"mod_time":"2026-01-02T03:04:05Z","is_symlink":true}`))
	if err != nil {
		t.Fatalf("DecodeStat(valid): %v", err)
	}
	if got.Path != "/home/shuki/f.txt" || got.Size != 7 || got.Mode != "-rw-r--r--" ||
		got.IsDir || got.ModTime != "2026-01-02T03:04:05Z" || !got.IsSymlink {
		t.Fatalf("DecodeStat decoded wrong: %+v", got)
	}
}

func TestDecodeDu(t *testing.T) {
	// Malformed body fails closed with a decode error.
	if _, err := DecodeDu([]byte(`{`)); err == nil {
		t.Fatal("DecodeDu(malformed) = nil error, want a decode error")
	}
	// AC2, load-bearing: an agent error blob decodes into a zero struct with an
	// empty path and must fail closed.
	if _, err := DecodeDu([]byte(`{"error":"boom"}`)); !errors.Is(err, ErrNoDuPath) {
		t.Fatalf("DecodeDu(error blob) = %v, want ErrNoDuPath", err)
	}
	// JSON null decodes without error into a zero value; the path guard rejects it.
	if _, err := DecodeDu([]byte(`null`)); !errors.Is(err, ErrNoDuPath) {
		t.Fatalf("DecodeDu(null) = %v, want ErrNoDuPath", err)
	}
	// Regression against a zero-value guard: a du of an EMPTY directory is a real
	// success — total 0, no entries — and must decode cleanly. Guarding on total
	// or entries instead of path would false-fail this.
	empty, err := DecodeDu([]byte(`{"path":"/home/shuki/empty","total":0,"entries":[]}`))
	if err != nil {
		t.Fatalf("DecodeDu(empty dir) = %v, want nil (0 bytes is a valid du)", err)
	}
	if empty.Path != "/home/shuki/empty" || empty.Total != 0 || len(empty.Entries) != 0 {
		t.Fatalf("DecodeDu(empty dir) decoded wrong: %+v", empty)
	}
	got, err := DecodeDu([]byte(`{"path":"/home/shuki","total":4096,"entries":[{"name":"a","is_dir":true,"size":4096,"has_subdirs":true}]}`))
	if err != nil {
		t.Fatalf("DecodeDu(valid): %v", err)
	}
	if got.Path != "/home/shuki" || got.Total != 4096 || len(got.Entries) != 1 ||
		got.Entries[0].Name != "a" || !got.Entries[0].IsDir || got.Entries[0].Size != 4096 || !got.Entries[0].HasSubdirs {
		t.Fatalf("DecodeDu decoded wrong: %+v", got)
	}
}

func TestDecodeExtract(t *testing.T) {
	// A malformed (truncated) body fails closed — the AC2 guarantee for extract.
	if _, err := DecodeExtract([]byte(`{`)); err == nil {
		t.Fatal("DecodeExtract(malformed) = nil error, want a decode error")
	}
	// Regression: a valid extract into the archive's parent carries an EMPTY dest
	// (the agent echoes the raw, omitted dest param) and 0/0 counts for an empty
	// archive. This must decode cleanly — a dest-presence guard would 500 the
	// common "extract here" action in production.
	def, err := DecodeExtract([]byte(`{"dest":"","extracted":0,"skipped":0}`))
	if err != nil {
		t.Fatalf("DecodeExtract(default-dest) = %v, want nil (empty dest is valid)", err)
	}
	if def.Dest != "" || def.Extracted != 0 || def.Skipped != 0 {
		t.Fatalf("DecodeExtract(default-dest) decoded wrong: %+v", def)
	}
	got, err := DecodeExtract([]byte(`{"dest":"/home/shuki/out","extracted":3,"skipped":1}`))
	if err != nil {
		t.Fatalf("DecodeExtract(valid): %v", err)
	}
	if got.Dest != "/home/shuki/out" || got.Extracted != 3 || got.Skipped != 1 {
		t.Fatalf("DecodeExtract decoded wrong: %+v", got)
	}
}

func TestDecodeJobStart(t *testing.T) {
	if _, err := DecodeJobStart([]byte(`{`)); err == nil {
		t.Fatal("DecodeJobStart(malformed) = nil error, want a decode error")
	}
	// An error blob has no job_id and must fail closed, so the UI never polls an
	// empty id as if a job had started.
	if _, err := DecodeJobStart([]byte(`{"error":"boom"}`)); !errors.Is(err, ErrNoJobID) {
		t.Fatalf("DecodeJobStart(error blob) = %v, want ErrNoJobID", err)
	}
	if _, err := DecodeJobStart([]byte(`null`)); !errors.Is(err, ErrNoJobID) {
		t.Fatalf("DecodeJobStart(null) = %v, want ErrNoJobID", err)
	}
	got, err := DecodeJobStart([]byte(`{"job_id":"job-abc123"}`))
	if err != nil {
		t.Fatalf("DecodeJobStart(valid): %v", err)
	}
	if got.JobID != "job-abc123" {
		t.Fatalf("DecodeJobStart decoded wrong: %+v", got)
	}
}

func TestDecodeJobStatus(t *testing.T) {
	if _, err := DecodeJobStatus([]byte(`{`)); err == nil {
		t.Fatal("DecodeJobStatus(malformed) = nil error, want a decode error")
	}
	// An error blob has no status and must fail closed.
	if _, err := DecodeJobStatus([]byte(`{"error":"boom"}`)); !errors.Is(err, ErrNoJobStatus) {
		t.Fatalf("DecodeJobStatus(error blob) = %v, want ErrNoJobStatus", err)
	}
	if _, err := DecodeJobStatus([]byte(`null`)); !errors.Is(err, ErrNoJobStatus) {
		t.Fatalf("DecodeJobStatus(null) = %v, want ErrNoJobStatus", err)
	}
	// A running job with 0 bytes done so far is a real success (done 0 is not the
	// identity field — status is).
	got, err := DecodeJobStatus([]byte(`{"job_id":"job-1","status":"running","done":0,"total":100,"started_at":"2026-01-02T03:04:05Z"}`))
	if err != nil {
		t.Fatalf("DecodeJobStatus(running) = %v, want nil", err)
	}
	if got.JobID != "job-1" || got.Status != "running" || got.Done != 0 || got.Total != 100 {
		t.Fatalf("DecodeJobStatus decoded wrong: %+v", got)
	}
}
