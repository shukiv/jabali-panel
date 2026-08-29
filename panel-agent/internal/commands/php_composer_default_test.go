package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestComposerDefaultSet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JABALI_COMPOSER_CHOICE_ROOT", dir)

	call := func(username, channel string) error {
		raw, _ := json.Marshal(composerDefaultParams{Username: username, Channel: channel})
		_, err := composerDefaultSetHandler(context.Background(), raw)
		return err
	}

	// lts writes the choice file.
	if err := call("alice", "lts"); err != nil {
		t.Fatalf("set lts: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "alice"))
	if err != nil || string(b) != "lts\n" {
		t.Fatalf("choice file = %q (%v), want \"lts\\n\"", b, err)
	}

	// latest clears it.
	if err := call("alice", "latest"); err != nil {
		t.Fatalf("set latest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "alice")); !os.IsNotExist(err) {
		t.Fatalf("choice file should be gone, stat err = %v", err)
	}

	// "" also clears (idempotent, no error when already absent).
	if err := call("alice", ""); err != nil {
		t.Fatalf("clear empty: %v", err)
	}

	// Invalid channel + username are rejected.
	for _, tc := range []struct{ u, ch string }{{"alice", "beta"}, {"Alice", "lts"}, {"../etc", "lts"}} {
		err := call(tc.u, tc.ch)
		if err == nil {
			t.Fatalf("expected error for username=%q channel=%q", tc.u, tc.ch)
		}
		var aerr *agentwire.AgentError
		if !errors.As(err, &aerr) || aerr.Code != agentwire.CodeInvalidArgument {
			t.Fatalf("want InvalidArgument, got %v", err)
		}
	}
}
