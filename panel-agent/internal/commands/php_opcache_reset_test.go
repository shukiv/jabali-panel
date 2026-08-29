package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestPHPOpcacheReset_Validation(t *testing.T) {
	t.Setenv("JABALI_PHP_POOL_SKIP_RELOAD", "1") // no real systemctl in tests
	cases := []struct {
		name    string
		params  phpOpcacheResetParams
		wantErr bool
	}{
		{"default pool ok", phpOpcacheResetParams{Username: "alice", Slug: "alice"}, false},
		{"versioned pool ok", phpOpcacheResetParams{Username: "alice", Slug: "alice-php8.4"}, false},
		{"bad username", phpOpcacheResetParams{Username: "Alice", Slug: "alice"}, true},
		{"slug traversal", phpOpcacheResetParams{Username: "alice", Slug: "../bob"}, true},
		{"slug of another user", phpOpcacheResetParams{Username: "alice", Slug: "bob"}, true},
		{"versioned slug of another user", phpOpcacheResetParams{Username: "alice", Slug: "bob-php8.4"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.params)
			_, err := phpOpcacheResetHandler(context.Background(), raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				var aerr *agentwire.AgentError
				if !errors.As(err, &aerr) || aerr.Code != agentwire.CodeInvalidArgument {
					t.Fatalf("expected InvalidArgument, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
