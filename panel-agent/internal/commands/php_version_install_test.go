package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPHPVersionInstall_InvalidVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		wantErr string
	}{
		{"unsupported", "5.6", "unsupported version"},
		{"invalid format", "8", "invalid version format"},
		{"invalid format dotted", "8.1.0", "invalid version format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			params, _ := json.Marshal(phpVersionInstallParams{Version: tt.version})

			_, err := phpVersionInstallHandler(ctx, params)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestPHPVersionInstall_NoParams(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, err := phpVersionInstallHandler(ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version parameter required")
}

// GH #531: an already-installed version must STILL have its required
// extensions ensured. isFPMAlreadyInstalled only proves php<v> is on PATH; a
// version installed without php<v>-mysql passes that check yet 500s every
// migrated domain's mysqli_connect(). The already-installed branch must call
// the ext-backfill seam, not short-circuit straight to a status response.
func TestPHPVersionInstall_AlreadyInstalled_BackfillsExts(t *testing.T) {
	origFPM := isFPMAlreadyInstalledFunc
	origEnsure := ensureRequiredPHPExtsFunc
	t.Cleanup(func() {
		isFPMAlreadyInstalledFunc = origFPM
		ensureRequiredPHPExtsFunc = origEnsure
	})

	isFPMAlreadyInstalledFunc = func(string) bool { return true }
	var called bool
	var gotVersion string
	ensureRequiredPHPExtsFunc = func(_ context.Context, version string) error {
		called = true
		gotVersion = version
		return nil
	}

	params, _ := json.Marshal(phpVersionInstallParams{Version: "8.2"})
	_, err := phpVersionInstallHandler(context.Background(), params)
	require.NoError(t, err)
	assert.True(t, called, "already-installed path must ensure required extensions (GH #531)")
	assert.Equal(t, "8.2", gotVersion)
}

// A backfill failure must surface as an error, not be swallowed — otherwise the
// caller (a migration restore, or the admin API) believes the runtime is
// complete when mysqli is still missing.
func TestPHPVersionInstall_AlreadyInstalled_ExtEnsureError(t *testing.T) {
	origFPM := isFPMAlreadyInstalledFunc
	origEnsure := ensureRequiredPHPExtsFunc
	t.Cleanup(func() {
		isFPMAlreadyInstalledFunc = origFPM
		ensureRequiredPHPExtsFunc = origEnsure
	})

	isFPMAlreadyInstalledFunc = func(string) bool { return true }
	ensureRequiredPHPExtsFunc = func(context.Context, string) error {
		return fmt.Errorf("apt boom")
	}

	params, _ := json.Marshal(phpVersionInstallParams{Version: "8.3"})
	_, err := phpVersionInstallHandler(context.Background(), params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure required extensions")
}

func TestVersionSupportValidation(t *testing.T) {
	t.Parallel()

	for _, v := range SupportedPHPVersions {
		assert.True(t, isVersionSupported(v), "version %s should be supported", v)
	}

	assert.False(t, isVersionSupported("5.6"))
	assert.False(t, isVersionSupported("9.0"))
}
