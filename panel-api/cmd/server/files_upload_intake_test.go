package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/uploadintake"
)

// JAB-365 — CLI half of the upload-intake cap/budget/isolation matrix. The HTTP
// half is internal/api/files_upload_intake_http_test.go. Both adapters stage
// through internal/uploadintake, so one owner has ONE budget and ONE in-flight
// cap across the File Manager and `jabali files upload`.

type recordedIngest struct {
	calls  int
	method string
	params map[string]any
	err    error
}

func (r *recordedIngest) call(_ context.Context, method string, params any) (json.RawMessage, error) {
	r.calls++
	r.method = method
	b, _ := json.Marshal(params)
	_ = json.Unmarshal(b, &r.params)
	if r.err != nil {
		return nil, r.err
	}
	return json.RawMessage(`{}`), nil
}

func cliIntakeStaging(t *testing.T) {
	t.Helper()
	prev := uploadintake.Dir
	uploadintake.Dir = t.TempDir()
	t.Cleanup(func() { uploadintake.Dir = prev })
}

func localFile(t *testing.T, size int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "local.bin")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func stagingLeft(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(uploadintake.Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// seedHTTPSessions stages n chunked File Manager sessions of size bytes for owner.
func seedHTTPSessions(t *testing.T, owner string, n, size int) {
	t.Helper()
	big := uploadintake.Limits{MaxBytes: 1 << 20, Budget: 1 << 30}
	for i := 0; i < n; i++ {
		if _, _, err := uploadintake.Append(owner, "seed"+string(rune('a'+i)), 0, strings.NewReader(strings.Repeat("s", size)), big); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCLIUploadIntake_Matrix(t *testing.T) {
	small := uploadintake.Limits{MaxBytes: 10, Budget: 20}

	t.Run("stages under the owner's shared identity and ingests", func(t *testing.T) {
		cliIntakeStaging(t)
		rec := &recordedIngest{}
		n, err := cliUpload(context.Background(), cliUploadDeps{limits: small, call: rec.call},
			"owner1", "alice", localFile(t, 5), "/home/alice/f.bin", true)
		if err != nil || n != 5 {
			t.Fatalf("cliUpload = %d, %v", n, err)
		}
		tmp, _ := rec.params["tmp_path"].(string)
		if want := uploadintake.Prefix() + uploadintake.OwnerTag("owner1") + "-"; !strings.HasPrefix(tmp, want) {
			t.Fatalf("staged at %q, want the shared owner identity %q…", tmp, want)
		}
		if rec.method != "files.ingest" || rec.params["user_id"] != "owner1" || rec.params["username"] != "alice" ||
			rec.params["dest_path"] != "/home/alice/f.bin" || rec.params["overwrite"] != true {
			t.Fatalf("ingest = %s %v", rec.method, rec.params)
		}
		if left := stagingLeft(t); len(left) != 0 {
			t.Fatalf("staging left behind: %v", left)
		}
	})

	t.Run("File Manager staging counts against the same budget", func(t *testing.T) {
		cliIntakeStaging(t)
		seedHTTPSessions(t, "owner1", 2, 10) // 20 bytes = the budget
		rec := &recordedIngest{}
		_, err := cliUpload(context.Background(), cliUploadDeps{limits: small, call: rec.call},
			"owner1", "alice", localFile(t, 1), "/home/alice/f", false)
		if err == nil || !strings.Contains(err.Error(), "budget") {
			t.Fatalf("err = %v, want the shared staging budget refusal", err)
		}
		if rec.calls != 0 {
			t.Fatal("a refused upload must not reach the agent")
		}
	})

	t.Run("five in-flight sessions refuse a sixth", func(t *testing.T) {
		cliIntakeStaging(t)
		seedHTTPSessions(t, "owner1", uploadintake.MaxInFlight, 1)
		rec := &recordedIngest{}
		_, err := cliUpload(context.Background(), cliUploadDeps{limits: uploadintake.Limits{MaxBytes: 10, Budget: 1 << 20}, call: rec.call},
			"owner1", "alice", localFile(t, 1), "/home/alice/f", false)
		if err == nil || !strings.Contains(err.Error(), "in flight") {
			t.Fatalf("err = %v, want the in-flight cap refusal", err)
		}
		if rec.calls != 0 {
			t.Fatal("a refused upload must not reach the agent")
		}
	})

	t.Run("another owner's staging never counts", func(t *testing.T) {
		cliIntakeStaging(t)
		seedHTTPSessions(t, "owner2", uploadintake.MaxInFlight, 10)
		rec := &recordedIngest{}
		if _, err := cliUpload(context.Background(), cliUploadDeps{limits: small, call: rec.call},
			"owner1", "alice", localFile(t, 5), "/home/alice/f", false); err != nil {
			t.Fatalf("owner1 must not be limited by owner2: %v", err)
		}
	})

	t.Run("over the cap is refused before staging", func(t *testing.T) {
		cliIntakeStaging(t)
		rec := &recordedIngest{}
		_, err := cliUpload(context.Background(), cliUploadDeps{limits: small, call: rec.call},
			"owner1", "alice", localFile(t, 11), "/home/alice/f", false)
		if err == nil || !strings.Contains(err.Error(), "SFTP/SSH") {
			t.Fatalf("err = %v, want the over-cap refusal", err)
		}
		if rec.calls != 0 || len(stagingLeft(t)) != 0 {
			t.Fatalf("calls=%d staging=%v, want neither", rec.calls, stagingLeft(t))
		}
	})

	t.Run("only a regular file is uploaded", func(t *testing.T) {
		cliIntakeStaging(t)
		rec := &recordedIngest{}
		_, err := cliUpload(context.Background(), cliUploadDeps{limits: small, call: rec.call},
			"owner1", "alice", t.TempDir(), "/home/alice/f", false)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("err = %v, want a non-regular-file refusal", err)
		}
		if rec.calls != 0 || len(stagingLeft(t)) != 0 {
			t.Fatalf("calls=%d staging=%v, want neither", rec.calls, stagingLeft(t))
		}
	})

	t.Run("an agent failure is an ingest error and leaves no staging", func(t *testing.T) {
		cliIntakeStaging(t)
		cause := errors.New("quota exceeded")
		rec := &recordedIngest{err: cause}
		_, err := cliUpload(context.Background(), cliUploadDeps{limits: small, call: rec.call},
			"owner1", "alice", localFile(t, 5), "/home/alice/f", false)
		var ingestErr *cliIngestError
		if !errors.As(err, &ingestErr) || !errors.Is(err, cause) {
			t.Fatalf("err = %v, want a cliIngestError wrapping the agent error", err)
		}
		if left := stagingLeft(t); len(left) != 0 {
			t.Fatalf("staging left behind: %v", left)
		}
	})
}

// The CLI honours the configured upload_max_size_mb in full, like the File
// Manager. The old 100 MiB clamp bounded an in-memory os.ReadFile (GH #661);
// the agent's files.ingest has no size cap, and the CLI now streams.
func TestCLIUploadLimits_MatchTheFileManager(t *testing.T) {
	for _, mb := range []uint32{0, 50, 200, 4096} {
		if got, want := cliUploadLimits(mb), uploadintake.LimitsFor(mb); got != want {
			t.Errorf("cliUploadLimits(%d) = %+v, want the File Manager's %+v", mb, got, want)
		}
	}
}
