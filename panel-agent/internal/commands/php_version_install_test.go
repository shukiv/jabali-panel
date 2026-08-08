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

// The agent's extension lists and install.sh's provision_php_extensions must
// name the same set. install.sh converges every installed version on each
// `jabali update` (04:30 fleet-wide); the agent closes the same-day window for
// versions added via the panel. If the sets drift, a version behaves
// differently depending on which path touched it last.
func TestPHPExtListsMatchInstallSh(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile(filepath.Join("..", "..", "..", "install.sh"))
	require.NoError(t, err, "read repo install.sh")

	var quoted string
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, `local exts="`); ok {
			quoted = strings.TrimSuffix(rest, `"`)
			break
		}
	}
	require.NotEmpty(t, quoted, `install.sh provision_php_extensions 'local exts="…"' line not found`)

	shSet := map[string]bool{}
	for _, e := range strings.Fields(quoted) {
		shSet[e] = true
	}
	goSet := map[string]bool{}
	for _, e := range append(append([]string{}, phpRequiredExts...), phpOptionalExts...) {
		goSet[e] = true
	}
	assert.Equal(t, shSet, goSet,
		"phpRequiredExts+phpOptionalExts must match install.sh provision_php_extensions")
}

// intl is required, not optional: idn_to_ascii backs IDN handling in
// commercial panels (WiseCP) and framework apps, and a version without it
// passes install yet fails those apps at runtime.
func TestPHPRequiredExtsIncludeIntl(t *testing.T) {
	t.Parallel()
	assert.Contains(t, phpRequiredExts, "intl")
}

func setExtSeams(t *testing.T, installed func(string) bool, probe func(string) bool,
	install func([]string) error, reload func(context.Context, string) ([]string, []string)) {
	t.Helper()
	origInstalled, origProbe := isPkgInstalledFunc, probePackageFunc
	origInstall, origReload := installPackagesFunc, reloadVersionFPMUnitsFunc
	t.Cleanup(func() {
		isPkgInstalledFunc, probePackageFunc = origInstalled, origProbe
		installPackagesFunc, reloadVersionFPMUnitsFunc = origInstall, origReload
	})
	isPkgInstalledFunc, probePackageFunc = installed, probe
	installPackagesFunc, reloadVersionFPMUnitsFunc = install, reload
}

// A flaky optional package (redis, sqlite3, …) must never fail the backfill —
// only a required package may. And once anything installed, the version's
// tenant masters must be reloaded or the running runtime never gains the
// extension.
func TestEnsureRequiredPHPExts_OptionalFailureNotFatal(t *testing.T) {
	var batches [][]string
	reloaded := false
	setExtSeams(t,
		func(string) bool { return false }, // nothing installed
		func(string) bool { return true },  // everything in apt
		func(pkgs []string) error {
			batches = append(batches, pkgs)
			for _, p := range pkgs {
				if strings.HasSuffix(p, "-redis") {
					return fmt.Errorf("apt boom on optional")
				}
			}
			return nil
		},
		func(context.Context, string) ([]string, []string) {
			reloaded = true
			return []string{"jabali-fpm@bob.service"}, nil
		},
	)

	err := ensureRequiredPHPExts(context.Background(), "8.2")
	require.NoError(t, err, "optional install failure must not fail the call")
	require.Len(t, batches, 2, "required and optional must install as separate apt batches")
	assert.Contains(t, batches[0], "php8.2-intl", "intl belongs to the required batch")
	assert.Contains(t, batches[1], "php8.2-redis")
	assert.True(t, reloaded, "masters must reload after the required batch landed")
}

func TestEnsureRequiredPHPExts_RequiredFailureFatal(t *testing.T) {
	reloaded := false
	setExtSeams(t,
		func(string) bool { return false },
		func(string) bool { return true },
		func(pkgs []string) error { return fmt.Errorf("apt boom") },
		func(context.Context, string) ([]string, []string) {
			reloaded = true
			return nil, nil
		},
	)

	err := ensureRequiredPHPExts(context.Background(), "8.2")
	require.Error(t, err)
	assert.False(t, reloaded, "no reload when the required batch failed")
}

func TestEnsureRequiredPHPExts_CompleteVersionIsNoop(t *testing.T) {
	setExtSeams(t,
		func(string) bool { return true }, // everything already installed
		func(string) bool { return true },
		func(pkgs []string) error {
			t.Errorf("apt must not run for a complete version, got %v", pkgs)
			return nil
		},
		func(context.Context, string) ([]string, []string) {
			t.Error("no reload for a no-op backfill")
			return nil, nil
		},
	)

	require.NoError(t, ensureRequiredPHPExts(context.Background(), "8.2"))
}

func TestVersionSupportValidation(t *testing.T) {
	t.Parallel()

	for _, v := range SupportedPHPVersions {
		assert.True(t, isVersionSupported(v), "version %s should be supported", v)
	}

	assert.False(t, isVersionSupported("5.6"))
	assert.False(t, isVersionSupported("9.0"))
}
