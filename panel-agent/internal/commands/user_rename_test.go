package commands

import (
	"context"
	"encoding/json"
	"testing"
)

// TestUserRename_ValidatesInput covers the input guards that reject before any
// host mutation (bad/identical names) — the exec-driven happy path is covered by
// the .86 rename drill under JABALI_ALLOW_HOST_MUTATION.
func TestUserRename_ValidatesInput(t *testing.T) {
	cases := []struct {
		name, oldU, newU string
	}{
		{"identical names", "alice", "alice"},
		{"invalid new (spaces)", "alice", "Bad Name"},
		{"invalid new (uppercase)", "alice", "Bob"},
		{"invalid old", "Bad", "bob"},
		{"empty new", "alice", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params, _ := json.Marshal(userRenameParams{OldUsername: tc.oldU, NewUsername: tc.newU, UID: 1001})
			if _, err := userRenameHandler(context.Background(), params); err == nil {
				t.Errorf("expected a validation error for %q -> %q", tc.oldU, tc.newU)
			}
		})
	}
}
