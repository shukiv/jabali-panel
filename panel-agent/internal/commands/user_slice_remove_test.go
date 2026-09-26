package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestUserSliceRemove_InvalidUsername(t *testing.T) {
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
			params := userSliceRemoveParams{
				Username: tt.username,
			}
			paramsJSON, _ := json.Marshal(params)

			_, err := userSliceRemoveHandler(context.Background(), paramsJSON)
			require.NotNil(t, err)

			var aerr *agentwire.AgentError
			require.ErrorAs(t, err, &aerr)
			assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
		})
	}
}

func TestUserSliceRemove_HappyPath_FilesExist(t *testing.T) {
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

	stopCalled := 0
	disableCalled := 0
	reloadCalled := 0

	testMutex.Lock()
	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name == "id" && len(args) >= 2 && args[0] == "-u" {
			return []byte("1000\n"), nil, nil
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "stop" {
			stopCalled++
			return nil, nil, nil
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "disable" {
			disableCalled++
			return nil, nil, nil
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "daemon-reload" {
			reloadCalled++
			return nil, nil, nil
		}
		return nil, nil, nil
	}

	systemdRoot = func() string {
		return tmpdir
	}
	testMutex.Unlock()

	// Pre-create files
	username := "testuser"
	sliceUnitPath := filepath.Join(tmpdir, fmt.Sprintf("jabali-user-%s.slice", username))
	fpmDropinDir := filepath.Join(tmpdir, fmt.Sprintf("jabali-fpm@%s.service.d", username))
	fpmDropinPath := filepath.Join(fpmDropinDir, "slice.conf")
	loginDropinDir := filepath.Join(tmpdir, "user@1000.service.d")
	loginDropinPath := filepath.Join(loginDropinDir, "jabali.conf")

	require.NoError(t, os.MkdirAll(fpmDropinDir, 0755))
	require.NoError(t, os.MkdirAll(loginDropinDir, 0755))
	require.NoError(t, os.WriteFile(sliceUnitPath, []byte("test"), 0644))
	require.NoError(t, os.WriteFile(fpmDropinPath, []byte("test"), 0644))
	require.NoError(t, os.WriteFile(loginDropinPath, []byte("test"), 0644))

	// Call remove
	params := userSliceRemoveParams{
		Username: username,
	}
	paramsJSON, _ := json.Marshal(params)

	resp, err := userSliceRemoveHandler(context.Background(), paramsJSON)
	require.NoError(t, err)

	result := resp.(*userSliceRemoveResponse)
	assert.Equal(t, username, result.Username)
	assert.True(t, result.Removed)
	assert.False(t, result.AlreadyAbsent)

	// Verify files were removed
	_, err = os.Stat(sliceUnitPath)
	assert.True(t, os.IsNotExist(err), "slice unit should be removed")

	_, err = os.Stat(fpmDropinPath)
	assert.True(t, os.IsNotExist(err), "FPM dropin should be removed")

	_, err = os.Stat(loginDropinPath)
	assert.True(t, os.IsNotExist(err), "login dropin should be removed")

	// Verify directories were removed
	_, err = os.Stat(fpmDropinDir)
	assert.True(t, os.IsNotExist(err), "FPM dropin directory should be removed")

	_, err = os.Stat(loginDropinDir)
	assert.True(t, os.IsNotExist(err), "login dropin directory should be removed")

	// Verify system calls
	assert.Greater(t, stopCalled, 0, "systemctl stop should be called")
	assert.Greater(t, disableCalled, 0, "systemctl disable should be called")
	assert.Equal(t, 1, reloadCalled, "systemctl daemon-reload should be called once")
}

func TestUserSliceRemove_AlreadyAbsent(t *testing.T) {
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
		if name == "systemctl" {
			return nil, nil, nil
		}
		return nil, nil, nil
	}

	systemdRoot = func() string {
		return tmpdir
	}
	testMutex.Unlock()

	// Don't create any files
	params := userSliceRemoveParams{
		Username: "testuser",
	}
	paramsJSON, _ := json.Marshal(params)

	resp, err := userSliceRemoveHandler(context.Background(), paramsJSON)
	require.NoError(t, err)

	result := resp.(*userSliceRemoveResponse)
	assert.False(t, result.Removed)
	assert.True(t, result.AlreadyAbsent)
}

func TestUserSliceRemove_PartialAbsent(t *testing.T) {
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
		if name == "systemctl" {
			return nil, nil, nil
		}
		return nil, nil, nil
	}

	systemdRoot = func() string {
		return tmpdir
	}
	testMutex.Unlock()

	// Create only the slice unit (not the dropins)
	username := "testuser"
	sliceUnitPath := filepath.Join(tmpdir, fmt.Sprintf("jabali-user-%s.slice", username))
	require.NoError(t, os.WriteFile(sliceUnitPath, []byte("test"), 0644))

	params := userSliceRemoveParams{
		Username: username,
	}
	paramsJSON, _ := json.Marshal(params)

	resp, err := userSliceRemoveHandler(context.Background(), paramsJSON)
	require.NoError(t, err)

	result := resp.(*userSliceRemoveResponse)
	assert.True(t, result.Removed, "should report removed since slice was deleted")
	assert.False(t, result.AlreadyAbsent)

	// Verify slice was removed
	_, err = os.Stat(sliceUnitPath)
	assert.True(t, os.IsNotExist(err))
}

func TestUserSliceRemove_UserNotFound_SkipLoginDropin(t *testing.T) {
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
			// User doesn't exist, so id -u fails
			return nil, []byte("id: no such user"), fmt.Errorf("id failed")
		}
		if name == "systemctl" {
			return nil, nil, nil
		}
		return nil, nil, nil
	}

	systemdRoot = func() string {
		return tmpdir
	}
	testMutex.Unlock()

	// Create only the slice unit and FPM dropin (login dropin won't be accessible)
	username := "testuser"
	sliceUnitPath := filepath.Join(tmpdir, fmt.Sprintf("jabali-user-%s.slice", username))
	fpmDropinDir := filepath.Join(tmpdir, fmt.Sprintf("jabali-fpm@%s.service.d", username))
	fpmDropinPath := filepath.Join(fpmDropinDir, "slice.conf")

	require.NoError(t, os.MkdirAll(fpmDropinDir, 0755))
	require.NoError(t, os.WriteFile(sliceUnitPath, []byte("test"), 0644))
	require.NoError(t, os.WriteFile(fpmDropinPath, []byte("test"), 0644))

	params := userSliceRemoveParams{
		Username: username,
	}
	paramsJSON, _ := json.Marshal(params)

	resp, err := userSliceRemoveHandler(context.Background(), paramsJSON)
	require.NoError(t, err, "should not error even if user not found")

	result := resp.(*userSliceRemoveResponse)
	assert.True(t, result.Removed)

	// Verify that at least slice and FPM dropin were removed
	_, err = os.Stat(sliceUnitPath)
	assert.True(t, os.IsNotExist(err))

	_, err = os.Stat(fpmDropinPath)
	assert.True(t, os.IsNotExist(err))
}

func TestUserSliceRemove_InvalidParams(t *testing.T) {
	t.Parallel()

	_, err := userSliceRemoveHandler(context.Background(), []byte("invalid json"))
	require.NotNil(t, err)

	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
}

func TestRemoveFile(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()
	testFile := filepath.Join(tmpdir, "test.txt")

	// File doesn't exist: not removed, not an error
	removed, err := removeFile(testFile)
	assert.False(t, removed)
	assert.NoError(t, err)

	// Create and remove
	require.NoError(t, os.WriteFile(testFile, []byte("test"), 0644))
	removed, err = removeFile(testFile)
	assert.True(t, removed)
	assert.NoError(t, err)

	// Verify it's gone
	_, err = os.Stat(testFile)
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveEmptyDir(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()
	testDir := filepath.Join(tmpdir, "testdir")
	require.NoError(t, os.Mkdir(testDir, 0755))

	// Should remove empty dir
	removeEmptyDir(testDir)

	_, err := os.Stat(testDir)
	assert.True(t, os.IsNotExist(err))
}

// A unit file that exists but cannot be removed is reported, not hidden. The
// handler used to discard every os.Remove error except ENOENT, so a file it
// could not remove came back as removed=false, already_absent=true: success,
// with the unit left on the host. The teardown still goes on (a stale unit
// file is better than a login account left behind by an aborted user.delete),
// but the response and the log say what stayed.
func TestUserSliceRemove_ReportsAUnitFileItCannotRemove(t *testing.T) {
	tmpdir := t.TempDir()
	oldRunCmd, oldSystemdRoot := runCmd, systemdRoot
	defer func() {
		testMutex.Lock()
		runCmd, systemdRoot = oldRunCmd, oldSystemdRoot
		testMutex.Unlock()
	}()
	testMutex.Lock()
	runCmd = func(context.Context, string, ...string) ([]byte, []byte, error) { return nil, nil, nil }
	systemdRoot = func() string { return tmpdir }
	testMutex.Unlock()

	// os.Remove fails on a non-empty directory whatever the caller's uid, so
	// this stands in for an immutable or read-only unit file.
	stuck := filepath.Join(tmpdir, "jabali-user-alice.slice")
	require.NoError(t, os.MkdirAll(stuck, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stuck, "keep"), []byte("x"), 0o644))

	paramsJSON, _ := json.Marshal(userSliceRemoveParams{Username: "alice"})
	resp, err := userSliceRemoveHandler(context.Background(), paramsJSON)
	require.NoError(t, err, "a leftover unit file must not abort the caller's teardown")

	result := resp.(*userSliceRemoveResponse)
	assert.False(t, result.AlreadyAbsent, "a file that is still there is not already absent")
	assert.Contains(t, result.Failed, stuck, "the unit file that stayed must be reported")
}
