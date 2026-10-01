package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestPHPPoolApplyHandler(t *testing.T) {
	tests := []struct {
		name      string
		input     phpPoolApplyParams
		wantError bool
		wantCode  string
	}{
		// Valid-params happy path is intentionally not unit-tested:
		// it would require the pool template on disk AND real systemctl
		// reload, both of which the plan forbids in validation-only
		// tests. The happy path is covered by the E2E test in step 9.

		// Username validation
		{
			name: "invalid: empty username",
			input: phpPoolApplyParams{
				Username:                  "",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username starts with digit",
			input: phpPoolApplyParams{
				Username:                  "1alice",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username contains uppercase",
			input: phpPoolApplyParams{
				Username:                  "Alice",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username contains space",
			input: phpPoolApplyParams{
				Username:                  "alice bob",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username contains backslash",
			input: phpPoolApplyParams{
				Username:                  `alice\bob`,
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username too long (>32 chars)",
			input: phpPoolApplyParams{
				Username:                  "a1234567890123456789012345678901234",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},

		// PHP version validation
		{
			name: "invalid: missing PHP version",
			input: phpPoolApplyParams{
				Username:                  "alice",
				PHPVersion:                "",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: malformed PHP version",
			input: phpPoolApplyParams{
				Username:                  "alice",
				PHPVersion:                "8",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},

		// PM mode validation
		{
			name: "invalid: bad pm_mode",
			input: phpPoolApplyParams{
				Username:                  "alice",
				PHPVersion:                "8.4",
				PmMode:                    "badmode",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},

		// PM max children validation
		{
			name: "invalid: zero pm_max_children",
			input: phpPoolApplyParams{
				Username:                  "alice",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             0,
				ProcessIdleTimeoutSeconds: 60,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},

		// Process idle timeout validation
		{
			name: "invalid: zero process_idle_timeout_seconds",
			input: phpPoolApplyParams{
				Username:                  "alice",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 0,
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},

		// Admin value validation
		{
			name: "invalid: forbidden admin_value directive",
			input: phpPoolApplyParams{
				Username:                  "alice",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
				AdminValues: []KV{
					{Name: "open_basedir", Value: "/home/alice"},
				},
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: unknown admin_value directive",
			input: phpPoolApplyParams{
				Username:                  "alice",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
				AdminValues: []KV{
					{Name: "unknown_directive", Value: "value"},
				},
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},

		// Admin flag validation
		{
			name: "invalid: unknown admin_flag directive",
			input: phpPoolApplyParams{
				Username:                  "alice",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
				AdminFlags: []KV{
					{Name: "unknown_flag", Value: "on"},
				},
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: admin_flag bad value",
			input: phpPoolApplyParams{
				Username:                  "alice",
				PHPVersion:                "8.4",
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
				AdminFlags: []KV{
					{Name: "display_errors", Value: "maybe"},
				},
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)

			_, err := phpPoolApplyHandler(context.Background(), params)

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

// TestPoolApplyVersionPin tests the version pin file lifecycle.
func TestPoolApplyVersionPin(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("JABALI_PHP_VER_PIN_ROOT", filepath.Join(tmpDir, "user-phpver"))
	os.Setenv("JABALI_FPM_CONFIG_ROOT", filepath.Join(tmpDir, "fpm"))
	defer func() {
		os.Unsetenv("JABALI_PHP_VER_PIN_ROOT")
		os.Unsetenv("JABALI_FPM_CONFIG_ROOT")
	}()

	// Test readVersionPinFile on non-existent file
	ver, err := readVersionPinFile("testuser")
	if err != nil {
		t.Errorf("readVersionPinFile on non-existent should return empty string, got err: %v", err)
	}
	if ver != "" {
		t.Errorf("expected empty version for non-existent file, got: %s", ver)
	}

	// Test writeVersionPinFile
	if err := writeVersionPinFile("testuser", "8.5"); err != nil {
		t.Fatalf("writeVersionPinFile failed: %v", err)
	}

	// Verify file was written
	pinPath := filepath.Join(tmpDir, "user-phpver", "testuser")
	if _, err := os.Stat(pinPath); err != nil {
		t.Fatalf("version pin file not created: %v", err)
	}

	// Verify content
	ver, err = readVersionPinFile("testuser")
	if err != nil {
		t.Fatalf("readVersionPinFile after write failed: %v", err)
	}
	if ver != "8.5" {
		t.Errorf("expected version 8.5, got: %s", ver)
	}
}

// TestPerUserFPMConfig tests per-user FPM config generation.
func TestPerUserFPMConfig(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("JABALI_FPM_CONFIG_ROOT", filepath.Join(tmpDir, "fpm"))
	defer os.Unsetenv("JABALI_FPM_CONFIG_ROOT")

	if err := writePerUserFPMConfig("testuser", "8.5"); err != nil {
		t.Fatalf("writePerUserFPMConfig failed: %v", err)
	}

	confPath := filepath.Join(tmpDir, "fpm", "testuser.conf")
	if _, err := os.Stat(confPath); err != nil {
		t.Fatalf("FPM config file not created: %v", err)
	}

	content, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "pid = /run/php/jabali-testuser/fpm.pid") {
		t.Errorf("config missing pid directive")
	}
	if !strings.Contains(contentStr, "error_log = /var/log/php-fpm-testuser.log") {
		t.Errorf("config missing error_log directive")
	}
	if !strings.Contains(contentStr, "include=/etc/php/8.5/fpm/pool.d/jabali-testuser.conf") {
		t.Errorf("config missing include directive")
	}
}

// --- GH #329: per-domain PHP version (versioned pool slug) ---

// TestPoolApplySlugRejectsBadSlug verifies the handler rejects a malformed
// slug before any filesystem work (pure validation).
func TestPoolApplySlugRejectsBadSlug(t *testing.T) {
	for _, bad := range []string{"Alice-php8.2", "alice/php8.2", "alice-php8.2/../x", ".."} {
		p := phpPoolApplyParams{
			Username:                  "alice",
			PHPVersion:                "8.4",
			Slug:                      bad,
			PmMode:                    "ondemand",
			PmMaxChildren:             20,
			ProcessIdleTimeoutSeconds: 60,
		}
		raw, _ := json.Marshal(p)
		_, err := phpPoolApplyHandler(context.Background(), raw)
		if err == nil {
			t.Errorf("slug %q: expected error, got nil", bad)
			continue
		}
		ae, ok := err.(*agentwire.AgentError)
		if !ok || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("slug %q: expected InvalidArgument, got %v", bad, err)
		}
	}
}

// TestPoolHelpersSlugKeyed verifies the per-master FPM config, version pin,
// and systemd drop-in are all keyed by the SLUG, not the username — so a
// versioned pool writes its own parallel set of files and never clobbers the
// default per-user pool.
func TestPoolHelpersSlugKeyed(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JABALI_FPM_CONFIG_ROOT", filepath.Join(tmp, "fpm"))
	os.Setenv("JABALI_PHP_VER_PIN_ROOT", filepath.Join(tmp, "user-phpver"))
	os.Setenv("JABALI_SYSTEMD_ROOT", filepath.Join(tmp, "systemd"))
	os.Setenv("JABALI_PHP_POOL_SKIP_RELOAD", "1")
	defer func() {
		os.Unsetenv("JABALI_FPM_CONFIG_ROOT")
		os.Unsetenv("JABALI_PHP_VER_PIN_ROOT")
		os.Unsetenv("JABALI_SYSTEMD_ROOT")
		os.Unsetenv("JABALI_PHP_POOL_SKIP_RELOAD")
	}()
	if err := os.MkdirAll(filepath.Join(tmp, "systemd"), 0755); err != nil {
		t.Fatal(err)
	}

	const slug = "alice-php8.2"

	// Per-master FPM config keyed by slug.
	if err := writePerUserFPMConfig(slug, "8.2"); err != nil {
		t.Fatalf("writePerUserFPMConfig: %v", err)
	}
	conf, err := os.ReadFile(filepath.Join(tmp, "fpm", slug+".conf"))
	if err != nil {
		t.Fatalf("slug fpm conf not created: %v", err)
	}
	cs := string(conf)
	if !strings.Contains(cs, "pid = /run/php/jabali-alice-php8.2/fpm.pid") {
		t.Errorf("pid not slug-keyed: %s", cs)
	}
	if !strings.Contains(cs, "include=/etc/php/8.2/fpm/pool.d/jabali-alice-php8.2.conf") {
		t.Errorf("include not slug-keyed: %s", cs)
	}
	// The default per-user config must NOT have been written.
	if _, err := os.Stat(filepath.Join(tmp, "fpm", "alice.conf")); !os.IsNotExist(err) {
		t.Errorf("versioned apply must not write the default alice.conf")
	}

	// Version pin keyed by slug.
	if err := writeVersionPinFile(slug, "8.2"); err != nil {
		t.Fatalf("writeVersionPinFile: %v", err)
	}
	if ver, _ := readVersionPinFile(slug); ver != "8.2" {
		t.Errorf("slug pin = %q, want 8.2", ver)
	}
	if _, err := os.Stat(filepath.Join(tmp, "user-phpver", "alice")); !os.IsNotExist(err) {
		t.Errorf("versioned apply must not write the default user pin")
	}

	// systemd drop-in for the versioned instance points at the real user +
	// existing slice.
	if err := ensureVersionedFPMDropin(context.Background(), slug, "alice"); err != nil {
		t.Fatalf("ensureVersionedFPMDropin: %v", err)
	}
	dropin, err := os.ReadFile(filepath.Join(tmp, "systemd", "jabali-fpm@"+slug+".service.d", "slice.conf"))
	if err != nil {
		t.Fatalf("drop-in not created: %v", err)
	}
	ds := string(dropin)
	for _, want := range []string{"Slice=jabali-user-alice.slice", "User=alice", "Group=alice"} {
		if !strings.Contains(ds, want) {
			t.Errorf("drop-in missing %q: %s", want, ds)
		}
	}
}

// An admin_value is rendered raw into the pool conf, so a newline in it would
// add a line past the directive allowlist and forbiddenDirectives. Any control
// character is refused. validatePoolIniOverrides is called directly: the
// handler checks /etc/php/<ver>/fpm/pool.d first, which a CI runner lacks.
func TestPoolApplyRejectsControlCharInAdminValue(t *testing.T) {
	for _, v := range []string{
		"256M\nphp_admin_value[open_basedir] = /",
		"256M\r\nuser = root",
		"256M\x00",
		"256M\t",
	} {
		err := validatePoolIniOverrides([]KV{{Name: "memory_limit", Value: v}}, nil)
		if err == nil || err.Code != agentwire.CodeInvalidArgument || !strings.Contains(err.Message, "control character") {
			t.Fatalf("value %q: err = %v, want invalid_argument naming the control character", v, err)
		}
	}
	if err := validatePoolIniOverrides([]KV{{Name: "memory_limit", Value: "256M"}, {Name: "date.timezone", Value: "Europe/Berlin"}},
		[]KV{{Name: "log_errors", Value: "off"}}); err != nil {
		t.Fatalf("clean overrides refused: %v", err)
	}
}
