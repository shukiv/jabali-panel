package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestUserSliceEnsure_InvalidUsername(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	tests := []struct {
		name     string
		username string
	}{
		{"leading underscore", "_user"},
		{"uppercase letter", "Alice"},
		{"starts with digit", "0user"},
		{"special char", "user@name"},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"space", "user name"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := userSliceEnsureParams{
				Username: tt.username,
			}
			paramsJSON, _ := json.Marshal(params)

			_, err := userSliceEnsureHandler(context.Background(), paramsJSON)
			require.NotNil(t, err)

			var aerr *agentwire.AgentError
			require.ErrorAs(t, err, &aerr)
			assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
		})
	}
}

func TestUserSliceEnsure_ValidUsernames(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	tests := []string{
		"alice",
		"user_name",
		"user-name",
		"user123",
		"a",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // exactly 32 chars
	}

	for _, username := range tests {
		t.Run(username, func(t *testing.T) {
			assert.True(t, userSliceUsernameRegex.MatchString(username))
		})
	}
}

func TestUserSliceEnsure_UserNotFound(t *testing.T) {
	requireHostMutationAllowed(t)
	// Note: no t.Parallel() due to global mock state

	// Mock runCmd to simulate user not existing
	oldRunCmd := runCmd
	defer func() {
		testMutex.Lock()
		runCmd = oldRunCmd
		testMutex.Unlock()
	}()

	testMutex.Lock()
	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name == "id" && len(args) >= 2 && args[0] == "-u" {
			return nil, nil, fmt.Errorf("id: nonexistent user %s", args[1])
		}
		return nil, nil, nil
	}
	testMutex.Unlock()

	params := userSliceEnsureParams{
		Username: "nonexistent",
	}
	paramsJSON, _ := json.Marshal(params)

	_, err := userSliceEnsureHandler(context.Background(), paramsJSON)
	require.NotNil(t, err)

	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeNotFound, aerr.Code)
	assert.Contains(t, aerr.Message, "does not exist on the host")
}

func TestUserSliceEnsure_HappyPath_FilesWritten(t *testing.T) {
	requireHostMutationAllowed(t)
	// Note: no t.Parallel() due to global mock state

	tmpdir := t.TempDir()

	// Mock runCmd and systemdRoot
	oldRunCmd := runCmd
	oldSystemdRoot := systemdRoot
	defer func() {
		testMutex.Lock()
		runCmd = oldRunCmd
		systemdRoot = oldSystemdRoot
		testMutex.Unlock()
	}()

	testMutex.Lock()
	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name == "id" && len(args) >= 2 && args[0] == "-u" {
			return []byte("1000\n"), nil, nil
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "daemon-reload" {
			return nil, nil, nil
		}
		return nil, nil, nil
	}

	systemdRoot = func() string {
		return tmpdir
	}
	testMutex.Unlock()

	params := userSliceEnsureParams{
		Username: "testuser",
	}
	paramsJSON, _ := json.Marshal(params)

	resp, err := userSliceEnsureHandler(context.Background(), paramsJSON)
	require.NoError(t, err)
	require.NotNil(t, resp)

	result := resp.(*userSliceEnsureResponse)
	assert.Equal(t, "testuser", result.Username)
	assert.Equal(t, 1000, result.UID)
	assert.False(t, result.NoChange)

	// Verify files were written
	sliceContent, err := os.ReadFile(result.SliceUnitPath)
	require.NoError(t, err)
	assert.Contains(t, string(sliceContent), "Description=Jabali hosted user testuser")
	assert.Contains(t, string(sliceContent), "CPUAccounting=yes")

	fpmContent, err := os.ReadFile(result.FPMDropinPath)
	require.NoError(t, err)
	assert.Contains(t, string(fpmContent), "Slice=jabali-user-testuser.slice")
	assert.Contains(t, string(fpmContent), "User=testuser")
	assert.Contains(t, string(fpmContent), "Group=testuser")

	loginContent, err := os.ReadFile(result.LoginDropinPath)
	require.NoError(t, err)
	assert.Contains(t, string(loginContent), "Slice=jabali-user-testuser.slice")
}

func TestUserSliceEnsure_ShortCircuit_FilesMatch(t *testing.T) {
	requireHostMutationAllowed(t)
	// Note: no t.Parallel() due to global mock state

	tmpdir := t.TempDir()

	// Mock runCmd and systemdRoot
	oldRunCmd := runCmd
	oldSystemdRoot := systemdRoot
	defer func() {
		testMutex.Lock()
		runCmd = oldRunCmd
		systemdRoot = oldSystemdRoot
		testMutex.Unlock()
	}()

	reloadCalled := false
	testMutex.Lock()
	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name == "id" && len(args) >= 2 && args[0] == "-u" {
			return []byte("1001\n"), nil, nil
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "daemon-reload" {
			reloadCalled = true
			return nil, nil, nil
		}
		return nil, nil, nil
	}

	systemdRoot = func() string {
		return tmpdir
	}
	testMutex.Unlock()

	// Pre-create files with expected content
	username := "testuser2"
	fpmDropinDir := filepath.Join(tmpdir, fmt.Sprintf("jabali-fpm@%s.service.d", username))
	loginDropinDir := filepath.Join(tmpdir, "user@1001.service.d")

	require.NoError(t, os.MkdirAll(fpmDropinDir, 0755))
	require.NoError(t, os.MkdirAll(loginDropinDir, 0755))

	sliceUnitPath := filepath.Join(tmpdir, fmt.Sprintf("jabali-user-%s.slice", username))
	fpmDropinPath := filepath.Join(fpmDropinDir, "slice.conf")
	loginDropinPath := filepath.Join(loginDropinDir, "jabali.conf")

	sliceContent := buildSliceUnitContent(username)
	fpmContent := buildFPMDropinContent(username)
	loginContent := buildLoginDropinContent(username)

	require.NoError(t, os.WriteFile(sliceUnitPath, []byte(sliceContent), 0644))
	require.NoError(t, os.WriteFile(fpmDropinPath, []byte(fpmContent), 0644))
	require.NoError(t, os.WriteFile(loginDropinPath, []byte(loginContent), 0644))

	// First call should short-circuit
	reloadCalled = false
	params := userSliceEnsureParams{
		Username: username,
	}
	paramsJSON, _ := json.Marshal(params)

	resp, err := userSliceEnsureHandler(context.Background(), paramsJSON)
	require.NoError(t, err)

	result := resp.(*userSliceEnsureResponse)
	assert.True(t, result.NoChange)
	assert.False(t, reloadCalled, "daemon-reload should not be called when files match")
}

func TestUserSliceEnsure_InvalidParams(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	_, err := userSliceEnsureHandler(context.Background(), []byte("invalid json"))
	require.NotNil(t, err)

	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
}

func TestUserSliceEnsure_ReloadFailure(t *testing.T) {
	requireHostMutationAllowed(t)
	// Note: no t.Parallel() due to global mock state

	tmpdir := t.TempDir()

	oldRunCmd := runCmd
	oldSystemdRoot := systemdRoot
	defer func() {
		testMutex.Lock()
		runCmd = oldRunCmd
		systemdRoot = oldSystemdRoot
		testMutex.Unlock()
	}()

	testMutex.Lock()
	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name == "id" && len(args) >= 2 && args[0] == "-u" {
			return []byte("1000\n"), nil, nil
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "daemon-reload" {
			return nil, []byte("some error"), fmt.Errorf("daemon-reload failed")
		}
		return nil, nil, nil
	}

	systemdRoot = func() string {
		return tmpdir
	}
	testMutex.Unlock()

	params := userSliceEnsureParams{
		Username: "testuser",
	}
	paramsJSON, _ := json.Marshal(params)

	_, err := userSliceEnsureHandler(context.Background(), paramsJSON)
	require.NotNil(t, err)

	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInternal, aerr.Code)
	assert.Contains(t, aerr.Message, "failed to reload systemd daemon")
}

func TestFileMatch(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	tmpdir := t.TempDir()
	testFile := filepath.Join(tmpdir, "test.conf")
	content := []byte("test content")

	// File doesn't exist yet
	assert.False(t, filesMatch(testFile, content))

	// Write file
	require.NoError(t, os.WriteFile(testFile, content, 0644))

	// Now it matches
	assert.True(t, filesMatch(testFile, content))

	// Different content doesn't match
	assert.False(t, filesMatch(testFile, []byte("different")))
}

func TestWriteFileAtomically(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	tmpdir := t.TempDir()
	testFile := filepath.Join(tmpdir, "test.conf")
	content := []byte("test content")

	require.NoError(t, writeFileAtomically(testFile, content, 0644))

	readContent, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, content, readContent)

	stat, err := os.Stat(testFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), stat.Mode().Perm())
}

func TestBuildSliceUnitContent(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	content := buildSliceUnitContent("testuser")
	assert.Contains(t, content, "Description=Jabali hosted user testuser")
	assert.Contains(t, content, "Before=slices.target")
	assert.Contains(t, content, "CPUAccounting=yes")
	assert.Contains(t, content, "MemoryAccounting=yes")
	assert.Contains(t, content, "TasksAccounting=yes")
	assert.NotContains(t, content, "PartOf=") // Explicitly should not have PartOf
}

func TestBuildFPMDropinContent(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	content := buildFPMDropinContent("testuser")
	assert.Contains(t, content, "Slice=jabali-user-testuser.slice")
	assert.Contains(t, content, "User=testuser")
	assert.Contains(t, content, "Group=testuser")
}

func TestBuildLoginDropinContent(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	content := buildLoginDropinContent("testuser")
	assert.Contains(t, content, "Slice=jabali-user-testuser.slice")
	// Should be in [Service] section, not [Slice]
	assert.NotContains(t, content, "[Slice]")
}

// GH #410 follow-up: user.slice.ensure self-heals jabali-redis-clients
// membership so migrated/reprovisioned tenants can reach the Redis socket.
func TestUserSliceEnsure_RedisGroupSelfHeal(t *testing.T) {
	requireHostMutationAllowed(t)
	run := func(t *testing.T, idNGOut string) []string {
		t.Helper()
		tmpdir := t.TempDir()
		oldRunCmd := runCmd
		oldSystemdRoot := systemdRoot
		defer func() {
			testMutex.Lock()
			runCmd = oldRunCmd
			systemdRoot = oldSystemdRoot
			testMutex.Unlock()
		}()
		var calls []string
		testMutex.Lock()
		runCmd = func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if name == "id" && len(args) >= 2 && args[0] == "-u" {
				return []byte("1000\n"), nil, nil
			}
			if name == "id" && len(args) >= 1 && args[0] == "-nG" {
				return []byte(idNGOut), nil, nil
			}
			return nil, nil, nil
		}
		systemdRoot = func() string { return tmpdir }
		testMutex.Unlock()
		paramsJSON, _ := json.Marshal(userSliceEnsureParams{Username: "testuser"})
		_, err := userSliceEnsureHandler(context.Background(), paramsJSON)
		require.NoError(t, err)
		return calls
	}

	has := func(calls []string, want string) bool {
		for _, c := range calls {
			if c == want {
				return true
			}
		}
		return false
	}

	// Not in the group -> usermod + fpm restart.
	calls := run(t, "testuser other\n")
	assert.True(t, has(calls, "usermod -aG jabali-redis-clients testuser"),
		"should add missing tenant to jabali-redis-clients; calls=%v", calls)
	assert.True(t, has(calls, "systemctl restart jabali-fpm@testuser.service"),
		"should restart fpm master after adding the group")

	// Already in the group -> no usermod, no restart.
	calls = run(t, "testuser jabali-redis-clients\n")
	assert.False(t, has(calls, "usermod -aG jabali-redis-clients testuser"),
		"must not re-add when already a member")
	assert.False(t, has(calls, "systemctl restart jabali-fpm@testuser.service"),
		"must not restart fpm when membership unchanged")
}
