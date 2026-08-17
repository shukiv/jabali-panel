package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestPHPPoolRemoveHandler(t *testing.T) {
	requireHostMutationAllowed(t) // GH #994: reaches `systemctl stop/disable jabali-fpm@<user>`
	tests := []struct {
		name      string
		input     phpPoolRemoveParams
		wantError bool
		wantCode  string
	}{
		// Valid case
		{
			name: "valid: username correct",
			input: phpPoolRemoveParams{
				Username: "alice",
			},
			wantError: false,
		},

		// Username validation
		{
			name: "invalid: empty username",
			input: phpPoolRemoveParams{
				Username: "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username starts with digit",
			input: phpPoolRemoveParams{
				Username: "1alice",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username contains uppercase",
			input: phpPoolRemoveParams{
				Username: "Alice",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username contains space",
			input: phpPoolRemoveParams{
				Username: "alice bob",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username too long (>32 chars)",
			input: phpPoolRemoveParams{
				Username: "a1234567890123456789012345678901234",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)

			_, err := phpPoolRemoveHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("wantError=%v, got err=%v", tt.wantError, err)
			}

			if tt.wantError && err != nil {
				aerr, ok := err.(*agentwire.AgentError)
				if !ok {
					t.Errorf("expected AgentError, got %T: %v", err, err)
				} else if aerr.Code != tt.wantCode {
					t.Errorf("wantCode=%s, got code=%s: %s", tt.wantCode, aerr.Code, aerr.Message)
				}
			}
		})
	}
}

// TestPoolRemoveCleanup tests that remove cleans up side-files.
func TestPoolRemoveCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("JABALI_FPM_CONFIG_ROOT", filepath.Join(tmpDir, "fpm"))
	os.Setenv("JABALI_PHP_VER_PIN_ROOT", filepath.Join(tmpDir, "user-phpver"))
	os.Setenv("JABALI_PHP_POOL_SKIP_RELOAD", "1")
	defer func() {
		os.Unsetenv("JABALI_FPM_CONFIG_ROOT")
		os.Unsetenv("JABALI_PHP_VER_PIN_ROOT")
		os.Unsetenv("JABALI_PHP_POOL_SKIP_RELOAD")
	}()

	// Create side-files
	fpmConfPath := filepath.Join(tmpDir, "fpm", "alice.conf")
	verPinPath := filepath.Join(tmpDir, "user-phpver", "alice")

	os.MkdirAll(filepath.Dir(fpmConfPath), 0755)
	os.MkdirAll(filepath.Dir(verPinPath), 0755)
	os.WriteFile(fpmConfPath, []byte("test"), 0644)
	os.WriteFile(verPinPath, []byte("8.5\n"), 0644)

	// Verify files exist
	if _, err := os.Stat(fpmConfPath); err != nil {
		t.Fatalf("setup: fpm config not created: %v", err)
	}
	if _, err := os.Stat(verPinPath); err != nil {
		t.Fatalf("setup: version pin not created: %v", err)
	}

	// Call remove handler
	params, _ := json.Marshal(phpPoolRemoveParams{Username: "alice"})
	_, err := phpPoolRemoveHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("phpPoolRemoveHandler failed: %v", err)
	}

	// Verify side-files were cleaned up
	if _, err := os.Stat(fpmConfPath); err == nil {
		t.Errorf("expected fpm config to be removed")
	}
	if _, err := os.Stat(verPinPath); err == nil {
		t.Errorf("expected version pin to be removed")
	}
}
