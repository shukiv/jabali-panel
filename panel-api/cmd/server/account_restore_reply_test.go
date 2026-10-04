package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const restoreReplyWithMeta = `{"user":{"id":"01U","username":"alice","is_admin":false},"metadata":{"schema_version":2},"applied":[]}`
const restoreReplyUserOnly = `{"user":{"id":"01U","username":"alice","email":"a@example.test","is_admin":false},"applied":[]}`

func runRestoreReply(t *testing.T, raw string, apply bool, ensureErr error) (metaCalls, userCalls int, out string) {
	t.Helper()
	var buf bytes.Buffer
	handleRestoreReply(&buf, json.RawMessage(raw), apply,
		func(json.RawMessage) { metaCalls++ },
		func(accountRestoreUserBlock) error { userCalls++; return ensureErr })
	return metaCalls, userCalls, buf.String()
}

// --apply=false is the staging-only smoke test: the agent still returns the
// metadata bundle, and the CLI must not reinstate it onto the live panel.
func TestHandleRestoreReply_ReconModeWritesNothing(t *testing.T) {
	for _, raw := range []string{restoreReplyWithMeta, restoreReplyUserOnly} {
		meta, user, out := runRestoreReply(t, raw, false, nil)
		if meta != 0 || user != 0 {
			t.Fatalf("recon mode reinstated panel state: metadata=%d user=%d", meta, user)
		}
		if !strings.Contains(out, "NOT reconstructed") {
			t.Fatalf("recon mode output does not say nothing was applied: %q", out)
		}
	}
}

func TestHandleRestoreReply_AppliedRestoreReinstates(t *testing.T) {
	if meta, user, _ := runRestoreReply(t, restoreReplyWithMeta, true, nil); meta != 1 || user != 0 {
		t.Fatalf("metadata bundle: metadata=%d user=%d, want 1/0", meta, user)
	}
	if meta, user, _ := runRestoreReply(t, restoreReplyUserOnly, true, nil); meta != 0 || user != 1 {
		t.Fatalf("no bundle: metadata=%d user=%d, want 0/1", meta, user)
	}
	_, _, out := runRestoreReply(t, restoreReplyUserOnly, true, errors.New("boom"))
	if !strings.Contains(out, "jabali user create --user-id 01U --username alice") {
		t.Fatalf("a failed user-row rebuild must print the manual command: %q", out)
	}
}
