package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestFilesWriteHandler(t *testing.T) {
	testUser := currentTestUser(t)

	// Generate large content string for testing the 100MB cap
	largeContent := strings.Repeat("x", 101*1024*1024)

	tests := []struct {
		name      string
		input     filesWriteParams
		wantError bool
		wantCode  string
	}{
		{
			name: "invalid: empty username",
			input: filesWriteParams{
				UserID:   "user123",
				Username: "",
				Path:     "/file.txt",
				Content:  "test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: empty path",
			input: filesWriteParams{
				UserID:   "user123",
				Username: testUser.Username,
				Path:     "",
				Content:  "test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: content exceeds 100MB",
			input: filesWriteParams{
				UserID:   "user123",
				Username: testUser.Username,
				Path:     "/file.txt",
				Content:  largeContent,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: path traversal with ..",
			input: filesWriteParams{
				UserID:   "user123",
				Username: testUser.Username,
				Path:     "../etc/passwd",
				Content:  "test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: absolute path outside home",
			input: filesWriteParams{
				UserID:   "user123",
				Username: testUser.Username,
				Path:     "/etc/passwd",
				Content:  "test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: path with null byte",
			input: filesWriteParams{
				UserID:   "user123",
				Username: testUser.Username,
				Path:     "/file\x00.txt",
				Content:  "test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: path with control character",
			input: filesWriteParams{
				UserID:   "user123",
				Username: testUser.Username,
				Path:     "/file\x01.txt",
				Content:  "test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)

			_, err := filesWriteHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("filesWriteHandler: expected error = %v, got %v", tt.wantError, err)
			}

			if tt.wantError && tt.wantCode != "" {
				var ae *agentwire.AgentError
				if !isAgentError(err, &ae) {
					t.Errorf("expected AgentError, got %T", err)
				} else if ae.Code != tt.wantCode {
					t.Errorf("expected code %q, got %q", tt.wantCode, ae.Code)
				}
			}
		})
	}
}

// TestFilesWriteHandler_NewFileGroupWwwData is an integration gate for GH #533:
// a file created via files.write must land group www-data (so nginx, running as
// www-data, can serve it through the 0640 group-read bit) — NOT the user's own
// primary group, which shows up to operators as "User:User". Chowning to
// www-data needs privilege and a real /home/<user>, so this is gated on
// root + JABALI_FMTEST_USER=<an existing hosting user>; it skips in the
// unprivileged CI job (the error-case unit tests above still run there).
func TestFilesWriteHandler_NewFileGroupWwwData(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown a new file to www-data; set JABALI_FMTEST_USER and run as root")
	}
	username := os.Getenv("JABALI_FMTEST_USER")
	if username == "" {
		t.Skip("set JABALI_FMTEST_USER=<existing hosting user> to run this integration gate")
	}
	if _, err := user.Lookup(username); err != nil {
		t.Skipf("JABALI_FMTEST_USER %q not found: %v", username, err)
	}
	wg, err := user.LookupGroup("www-data")
	if err != nil {
		t.Skip("www-data group not present; skipping")
	}
	wantGid, _ := strconv.Atoi(wg.Gid)

	abs := filepath.Join("/home", username, fmt.Sprintf("gh533-fmtest-%d.txt", os.Getpid()))
	defer os.Remove(abs)

	params, _ := json.Marshal(filesWriteParams{
		UserID:   "fmtest",
		Username: username,
		Path:     abs,
		Content:  "gh533",
	})
	if _, err := filesWriteHandler(context.Background(), params); err != nil {
		t.Fatalf("files.write failed: %v", err)
	}

	fi, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat %s: %v", abs, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat_t unavailable")
	}
	if int(st.Gid) != wantGid {
		t.Errorf("new file group = %d, want www-data (%d) — GH #533 regression", st.Gid, wantGid)
	}

	// Append mode that CREATES a brand-new file must also land group www-data
	// (GH #533 follow-up: previously left root-owned, no chown).
	absAppend := filepath.Join("/home", username, fmt.Sprintf("gh533-fmtest-append-%d.txt", os.Getpid()))
	defer os.Remove(absAppend)
	appendParams, _ := json.Marshal(filesWriteParams{
		UserID:   "fmtest",
		Username: username,
		Path:     absAppend,
		Content:  "gh533-append",
		Mode:     "append",
	})
	if _, err := filesWriteHandler(context.Background(), appendParams); err != nil {
		t.Fatalf("files.write (append create) failed: %v", err)
	}
	afi, err := os.Stat(absAppend)
	if err != nil {
		t.Fatalf("stat %s: %v", absAppend, err)
	}
	ast, ok := afi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat_t unavailable (append)")
	}
	if int(ast.Gid) != wantGid {
		t.Errorf("append-created file group = %d, want www-data (%d) — GH #533 regression", ast.Gid, wantGid)
	}
}
