package uploadintake

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useTempDir(t *testing.T) {
	t.Helper()
	prev := Dir
	Dir = t.TempDir()
	t.Cleanup(func() { Dir = prev })
}

func stagedFiles(t *testing.T) []string {
	t.Helper()
	m, err := filepath.Glob(Prefix() + "*")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The agent ingests only Dir + "/jabali-upload-" + <flat name>. The production
// prefix must equal the agent's gate, and every path the module mints must sit
// flat under it — or every upload fails at ingest.
func TestPrefix_MatchesTheAgentIngestGate(t *testing.T) {
	src, err := os.ReadFile("../../../panel-agent/internal/commands/files_ingest.go")
	if err != nil {
		t.Fatalf("read the agent ingest source: %v", err)
	}
	if want := `tmpUploadPrefix = "` + Prefix() + `"`; !strings.Contains(string(src), want) {
		t.Fatalf("the agent's files.ingest gate no longer matches %q", Prefix())
	}

	useTempDir(t)
	chunk := ChunkPath("owner", "upload-1")
	single, _, err := Stage("owner", strings.NewReader("x"), LimitsFor(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{chunk, single} {
		rest := strings.TrimPrefix(p, Prefix())
		if !strings.HasPrefix(p, Prefix()) || rest == "" || strings.Contains(rest, "/") {
			t.Fatalf("%q must sit flat under %q", p, Prefix())
		}
	}
}

// Gitea #426: a chunked session path is a pure function of the AUTHENTICATED
// owner and the upload id, so another owner who learns the upload id can never
// compute it.
func TestChunkPath_PerOwner(t *testing.T) {
	a, b := ChunkPath("userA", "shared-id"), ChunkPath("userB", "shared-id")
	if a == b {
		t.Fatalf("one upload id for two owners must map to two paths: %s", a)
	}
	if a != ChunkPath("userA", "shared-id") {
		t.Fatal("the path must be deterministic for one (owner, upload id)")
	}
	if OwnerTag("userA") == OwnerTag("userB") {
		t.Fatal("distinct owners need distinct tags")
	}
}

// #425: Stats counts only this owner's staging files, chunked and single-shot.
func TestStats_CountsOnlyTheOwner(t *testing.T) {
	useTempDir(t)
	for i, body := range []string{"aa", "bbbb"} {
		if _, _, err := Append("user1", string(rune('a'+i)), 0, strings.NewReader(body), LimitsFor(1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := Stage("user1", strings.NewReader("c"), LimitsFor(1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Stage("user2", strings.NewReader("zzzzz"), LimitsFor(1)); err != nil {
		t.Fatal(err)
	}
	if n, b := Stats("user1"); n != 3 || b != 7 {
		t.Fatalf("Stats(user1) = (%d, %d), want (3, 7)", n, b)
	}
}

func TestLimitsFor(t *testing.T) {
	if got := LimitsFor(0); got.MaxBytes != DefaultMaxBytes || got.Budget != 2*DefaultMaxBytes {
		t.Fatalf("unset = %+v, want the 1 GiB default and a 2 GiB budget", got)
	}
	if got := LimitsFor(5); got.MaxBytes != 5<<20 || got.Budget != 10<<20 {
		t.Fatalf("5 MB = %+v, want 5 MiB / 10 MiB", got)
	}
}

// Admit refuses on the in-flight count before the budget, and on a budget
// reached exactly (>=).
func TestAdmit(t *testing.T) {
	useTempDir(t)
	l := Limits{MaxBytes: 10, Budget: 10}
	if err := Admit("u", l); err != nil {
		t.Fatalf("empty owner: %v", err)
	}
	if _, _, err := Stage("u", strings.NewReader(strings.Repeat("x", 10)), l); err != nil {
		t.Fatal(err)
	}
	if err := Admit("u", l); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("budget reached exactly: %v, want ErrBudgetExceeded", err)
	}
	big := Limits{MaxBytes: 10, Budget: 1 << 30}
	for i := 1; i < MaxInFlight; i++ {
		if _, _, err := Stage("u", strings.NewReader("x"), big); err != nil {
			t.Fatal(err)
		}
	}
	if err := Admit("u", Limits{MaxBytes: 10, Budget: 0}); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("at the in-flight cap: %v, want ErrTooManyUploads first", err)
	}
}

func TestStage(t *testing.T) {
	t.Run("stages the bytes", func(t *testing.T) {
		useTempDir(t)
		path, n, err := Stage("u", strings.NewReader("hello"), LimitsFor(1))
		if err != nil || n != 5 {
			t.Fatalf("Stage = %q, %d, %v", path, n, err)
		}
		if b, _ := os.ReadFile(path); string(b) != "hello" {
			t.Fatalf("staged %q", b)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
		}
	})
	t.Run("over the cap leaves nothing", func(t *testing.T) {
		useTempDir(t)
		_, _, err := Stage("u", strings.NewReader("123456"), Limits{MaxBytes: 5, Budget: 100})
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v, want ErrTooLarge", err)
		}
		if f := stagedFiles(t); len(f) != 0 {
			t.Fatalf("left behind %v", f)
		}
	})
	t.Run("past the budget removes only itself", func(t *testing.T) {
		useTempDir(t)
		l := Limits{MaxBytes: 6, Budget: 8}
		first, _, err := Stage("u", strings.NewReader("12345"), l)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Stage("u", strings.NewReader("1234"), l); !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("err = %v, want ErrBudgetExceeded", err)
		}
		if f := stagedFiles(t); len(f) != 1 || f[0] != first {
			t.Fatalf("staged %v, want only %s", f, first)
		}
	})
}

func TestAppend(t *testing.T) {
	l := Limits{MaxBytes: 8, Budget: 100}
	t.Run("contiguous chunks assemble", func(t *testing.T) {
		useTempDir(t)
		if _, _, err := Append("u", "s", 0, strings.NewReader("abc"), l); err != nil {
			t.Fatal(err)
		}
		path, n, err := Append("u", "s", 3, strings.NewReader("de"), l)
		if err != nil || n != 2 {
			t.Fatalf("Append = %d, %v", n, err)
		}
		if b, _ := os.ReadFile(path); string(b) != "abcde" {
			t.Fatalf("assembled %q", b)
		}
		if w, err := Written("u", "s"); err != nil || w != 5 {
			t.Fatalf("Written = %d, %v", w, err)
		}
	})
	t.Run("a hole or an overwrite is refused with the resume size", func(t *testing.T) {
		useTempDir(t)
		if _, _, err := Append("u", "s", 0, strings.NewReader("abc"), l); err != nil {
			t.Fatal(err)
		}
		for _, off := range []int64{1, 4} {
			var bad *BadOffsetError
			if _, _, err := Append("u", "s", off, strings.NewReader("x"), l); !errors.As(err, &bad) || bad.Expected != 3 {
				t.Fatalf("offset %d: err = %v, want BadOffsetError{3}", off, err)
			}
		}
	})
	t.Run("a later chunk never creates a session", func(t *testing.T) {
		useTempDir(t)
		if _, _, err := Append("u", "ghost", 3, strings.NewReader("x"), l); !errors.Is(err, ErrUploadNotFound) {
			t.Fatalf("err = %v, want ErrUploadNotFound", err)
		}
		if f := stagedFiles(t); len(f) != 0 {
			t.Fatalf("created %v", f)
		}
	})
	t.Run("another owner's session is out of reach", func(t *testing.T) {
		useTempDir(t)
		if _, _, err := Append("owner", "s", 0, strings.NewReader("abc"), l); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Append("intruder", "s", 3, strings.NewReader("x"), l); !errors.Is(err, ErrUploadNotFound) {
			t.Fatalf("err = %v, want ErrUploadNotFound", err)
		}
		if _, err := Written("intruder", "s"); !errors.Is(err, ErrUploadNotFound) {
			t.Fatalf("Written = %v, want ErrUploadNotFound", err)
		}
	})
	t.Run("growing past the cap removes the session", func(t *testing.T) {
		useTempDir(t)
		if _, _, err := Append("u", "s", 0, strings.NewReader("12345678"), l); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Append("u", "s", 8, strings.NewReader("9"), l); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v, want ErrTooLarge", err)
		}
		if f := stagedFiles(t); len(f) != 0 {
			t.Fatalf("left behind %v", f)
		}
	})
}

func TestIngest(t *testing.T) {
	t.Run("an agent failure discards the staging file", func(t *testing.T) {
		useTempDir(t)
		path, _, err := Stage("u", strings.NewReader("x"), LimitsFor(1))
		if err != nil {
			t.Fatal(err)
		}
		cause := errors.New("agent down")
		err = Ingest(context.Background(), func(context.Context, string, any) (json.RawMessage, error) { return nil, cause },
			IngestParams{TmpPath: path})
		if !errors.Is(err, cause) {
			t.Fatalf("err = %v, want the agent error", err)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal("a failed ingest must not leave staging behind")
		}
	})
	t.Run("sends files.ingest with the agent's wire names", func(t *testing.T) {
		var method string
		var raw []byte
		err := Ingest(context.Background(), func(_ context.Context, m string, p any) (json.RawMessage, error) {
			method = m
			raw, _ = json.Marshal(p)
			return json.RawMessage(`{}`), nil
		}, IngestParams{UserID: "u1", Username: "alice", AdminRoot: true, TmpPath: "/t", DestPath: "/d", Overwrite: true})
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		want := map[string]any{"user_id": "u1", "username": "alice", "admin_root": true, "tmp_path": "/t", "dest_path": "/d", "overwrite": true}
		if method != "files.ingest" || len(got) != len(want) {
			t.Fatalf("call = %s %s", method, raw)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("param %s = %v, want %v (%s)", k, got[k], v, raw)
			}
		}
	})
}
