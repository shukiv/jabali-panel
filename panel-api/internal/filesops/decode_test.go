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
