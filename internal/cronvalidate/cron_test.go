package cronvalidate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCommand(t *testing.T) {
	ownedDocroots := []string{"/home/shuki/example.com/public_html"}

	tests := []struct {
		name     string
		raw      string
		docroots []string
		wantErr  string // expected error code, empty = success
		desc     string
	}{
		// Happy paths
		{
			name:     "wp_cron_with_path_flag",
			raw:      "wp cron event run --path=/home/shuki/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "Standard wp cron with --path= form",
		},
		{
			name:     "wp_cron_with_path_two_token",
			raw:      "wp cron event run --path /home/shuki/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "wp cron with separate --path arg",
		},
		{
			name:     "wp_cron_with_extra_args",
			raw:      "wp cron event run --path=/home/shuki/example.com/public_html --due-now",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "wp with additional arguments",
		},
		{
			name:     "php_absolute_path",
			raw:      "php /home/shuki/example.com/public_html/tools/cleanup.php",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "PHP script with absolute path",
		},
		{
			name:     "php_with_args",
			raw:      "php /home/shuki/example.com/public_html/tools/cleanup.php --verbose --days=30",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "PHP script with additional arguments",
		},

		// Rejection: empty / whitespace
		{
			name:     "empty_string",
			raw:      "",
			docroots: ownedDocroots,
			wantErr:  ErrCodeEmpty,
			desc:     "Empty command",
		},
		{
			name:     "whitespace_only",
			raw:      "   \t  ",
			docroots: ownedDocroots,
			wantErr:  ErrCodeEmpty,
			desc:     "Whitespace-only command",
		},

		// Rejection: too long
		{
			name:     "exceeds_1024",
			raw:      "wp cron event run --path=/home/shuki/example.com/public_html " + string(make([]byte, 1024)),
			docroots: ownedDocroots,
			wantErr:  ErrCodeTooLong,
			desc:     "Command exceeds 1024 bytes",
		},

		// Rejection: wrong binary
		{
			name:     "cat_binary",
			raw:      "cat /etc/passwd",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "Rejected binary: cat",
		},
		{
			name:     "bash_binary",
			raw:      "bash -c 'echo hello'",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "Rejected binary: bash",
		},
		{
			name:     "sudo_binary",
			raw:      "sudo wp cron event run --path=/home/shuki/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "Rejected binary: sudo",
		},
		{
			name:     "full_path_wp",
			raw:      "/usr/bin/wp cron event run --path=/home/shuki/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "Full path to wp binary (not allowed)",
		},

		// Rejection: metacharacters (primary defense)
		{
			name:     "command_injection_dollar",
			raw:      "wp cron event run --path=/x$(id)",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Command injection via $()",
		},
		{
			name:     "command_injection_backtick",
			raw:      "wp cron event run --path=/x`id`",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Command injection via backticks",
		},
		{
			name:     "pipe_injection",
			raw:      "wp cron event run | tee /tmp/output",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Pipe metacharacter",
		},
		{
			name:     "semicolon_injection",
			raw:      "wp cron event run; id",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Semicolon statement separator",
		},
		{
			name:     "ampersand_injection",
			raw:      "wp cron event run && rm -rf /",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Ampersand logical AND",
		},
		{
			name:     "redirect_output",
			raw:      "wp cron event run > /tmp/output",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Output redirection",
		},
		{
			name:     "redirect_input",
			raw:      "wp cron event run < /etc/passwd",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Input redirection",
		},
		{
			name:     "newline_injection",
			raw:      "wp cron\nid",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Newline in command",
		},
		{
			name:     "nul_byte",
			raw:      "wp cron\x00id",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "NUL byte in command",
		},
		{
			name:     "brace_expansion",
			raw:      "wp cron event run --path=/home/{shuki,other}/example.com",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Brace expansion",
		},
		{
			name:     "glob_star_unquoted",
			raw:      "wp cron event run --path=/home/shuki/*.php",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Unquoted glob star",
		},
		{
			name:     "glob_question_unquoted",
			raw:      "wp cron event run --path=/home/shuki/?.php",
			docroots: ownedDocroots,
			wantErr:  ErrCodeMetacharReject,
			desc:     "Unquoted glob question mark",
		},

		// Rejection: path traversal
		{
			name:     "path_traversal_dotdot",
			raw:      "wp cron event run --path=/home/shuki/../other/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "Path traversal via ..",
		},
		{
			name:     "php_path_traversal",
			raw:      "php /home/shuki/../etc/passwd.php",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "PHP path traversal",
		},

		// Rejection: path not in owned docroots
		{
			name:     "path_different_user",
			raw:      "wp cron event run --path=/home/otheruser/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "Path not in owned docroots",
		},
		{
			name:     "prefix_attack",
			raw:      "wp cron event run --path=/home/shukimalicious/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "Prefix match attack (missing / boundary)",
		},

		// Rejection: missing or invalid path args
		{
			name:     "wp_missing_path",
			raw:      "wp cron event run",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "wp command without --path",
		},
		{
			name:     "php_no_args",
			raw:      "php",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "php without filename",
		},
		{
			name:     "php_relative_path",
			raw:      "php tools/cleanup.php",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "php with relative path",
		},
		{
			name:     "php_not_ending_php",
			raw:      "php /home/shuki/example.com/public_html/tools/cleanup.txt",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "php path not ending in .php",
		},

		// Versioned PHP interpreter (GH #299): same arg rules as bare php.
		{
			name:     "php_versioned_script",
			raw:      "php8.5 /home/shuki/example.com/public_html/cron.php",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "Versioned php8.5 with an absolute .php script is allowed",
		},
		{
			name:     "php_versioned_flag",
			raw:      "php8.4 -v",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "Versioned php8.4 with a standalone flag is allowed",
		},
		{
			name:     "php_versioned_relative_rejected",
			raw:      "php8.5 tools/cleanup.php",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "Versioned php still requires an absolute path",
		},
		{
			name:     "php_fake_version_rejected",
			raw:      "php8 /home/shuki/example.com/public_html/cron.php",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "php<major> without a minor is not a versioned interpreter",
		},
		{
			name:     "php_bogus_suffix_rejected",
			raw:      "phpmyadmin /home/shuki/example.com/public_html/cron.php",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "A non-php binary is still rejected",
		},

		// Inline-code PHP flags are rejected (GH #440).
		{
			name:     "php_dash_r_rejected",
			raw:      "php -r 'echo 1;'",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "php -r inline code is rejected",
		},
		{
			name:     "php_dash_r_glued_rejected",
			raw:      "php -rphpinfo",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "glued php -rCODE is rejected",
		},
		{
			name:     "php_dash_R_rejected",
			raw:      "php -R 'echo 1;'",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "php -R inline code is rejected",
		},
		{
			name:     "php_dash_r_after_flag_rejected",
			raw:      "php -d display_errors=0 -r 'echo 1;'",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBinaryNotAllowed,
			desc:     "php -r not at argv[1] is still rejected",
		},
		{
			name:     "php_version_flag_still_allowed",
			raw:      "php -v",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "harmless introspection flag -v stays allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := ValidateCommand(tt.raw, tt.docroots, "")

			if tt.wantErr == "" {
				require.NoError(t, err, tt.desc)
				require.NotNil(t, cmd)
				require.NotEmpty(t, cmd.Argv)
			} else {
				require.Error(t, err, tt.desc)
				valErr, ok := err.(*ValidationError)
				require.True(t, ok, "error should be *ValidationError")
				assert.Equal(t, tt.wantErr, valErr.Code, tt.desc)
			}
		})
	}
}

func TestValidateSchedule(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr string // expected error code, empty = success
		desc    string
	}{
		// Happy paths
		{
			name:    "hourly_every_hour",
			expr:    "0 * * * *",
			wantErr: "",
			desc:    "Hourly (every hour)",
		},
		{
			name:    "daily_3am",
			expr:    "0 3 * * *",
			wantErr: "",
			desc:    "Daily at 3 AM",
		},
		{
			name:    "weekly_sunday_3am",
			expr:    "0 3 * * 0",
			wantErr: "",
			desc:    "Weekly on Sunday at 3 AM",
		},
		{
			name:    "monthly_1st",
			expr:    "0 0 1 * *",
			wantErr: "",
			desc:    "Monthly on 1st at midnight",
		},
		{
			name:    "every_5_min",
			expr:    "*/5 * * * *",
			wantErr: "",
			desc:    "Every 5 minutes",
		},
		{
			name:    "every_minute",
			expr:    "* * * * *",
			wantErr: "",
			desc:    "Every minute (1-minute resolution, allowed)",
		},
		{
			name:    "complex_expression",
			expr:    "0,30 9-17 * * 1-5",
			wantErr: "",
			desc:    "Complex: 9:00 and 9:30 on weekdays",
		},

		// Rejection: empty / whitespace
		{
			name:    "empty_string",
			expr:    "",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Empty schedule expression",
		},
		{
			name:    "whitespace_only",
			expr:    "   \t  ",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Whitespace-only expression",
		},

		// Rejection: shortcuts
		{
			name:    "shortcut_hourly",
			expr:    "@hourly",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Shortcut @hourly not allowed",
		},
		{
			name:    "shortcut_daily",
			expr:    "@daily",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Shortcut @daily not allowed",
		},
		{
			name:    "shortcut_weekly",
			expr:    "@weekly",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Shortcut @weekly not allowed",
		},
		{
			name:    "shortcut_monthly",
			expr:    "@monthly",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Shortcut @monthly not allowed",
		},
		{
			name:    "shortcut_reboot",
			expr:    "@reboot",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Shortcut @reboot not allowed",
		},
		{
			name:    "shortcut_every",
			expr:    "@every 1h",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Shortcut @every not allowed",
		},

		// Rejection: wrong field count
		{
			name:    "six_fields",
			expr:    "0 0 0 * * *",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "6 fields (with seconds) not allowed",
		},
		{
			name:    "four_fields",
			expr:    "0 0 * *",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Only 4 fields",
		},

		// Rejection: invalid syntax
		{
			name:    "bad_syntax",
			expr:    "bad syntax here",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Invalid cron syntax",
		},
		{
			name:    "invalid_month",
			expr:    "0 0 1 13 *",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Invalid month (13)",
		},
		{
			name:    "invalid_dow",
			expr:    "0 0 * * 7",
			wantErr: ErrCodeBadScheduleSyntax,
			desc:    "Invalid day of week (7)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSchedule(tt.expr)

			if tt.wantErr == "" {
				assert.NoError(t, err, tt.desc)
			} else {
				require.Error(t, err, tt.desc)
				valErr, ok := err.(*ValidationError)
				require.True(t, ok, "error should be *ValidationError")
				assert.Equal(t, tt.wantErr, valErr.Code, tt.desc)
			}
		})
	}
}

func TestHasUnquotedMetachar(t *testing.T) {
	tests := []struct {
		input string
		want  bool
		desc  string
	}{
		// Clean inputs
		{
			input: "wp cron event run",
			want:  false,
			desc:  "Normal command",
		},
		{
			input: "php /home/user/script.php",
			want:  false,
			desc:  "PHP path",
		},

		// Quoted metacharacters (allowed)
		{
			input: "wp post list --post_status='publish,draft'",
			want:  false,
			desc:  "Glob chars inside single quotes allowed",
		},
		{
			input: `wp post list --post_status="publish,draft"`,
			want:  false,
			desc:  "Glob chars inside double quotes allowed",
		},

		// Unquoted metacharacters (rejected)
		{
			input: "wp | cat",
			want:  true,
			desc:  "Pipe metachar",
		},
		{
			input: "wp & bg",
			want:  true,
			desc:  "Ampersand metachar",
		},
		{
			input: "wp; id",
			want:  true,
			desc:  "Semicolon metachar",
		},
		{
			input: "wp$(id)",
			want:  true,
			desc:  "Dollar with parentheses",
		},
		{
			input: "wp`id`",
			want:  true,
			desc:  "Backticks",
		},
		{
			input: "wp > /tmp/out",
			want:  true,
			desc:  "Greater than",
		},
		{
			input: "wp < /tmp/in",
			want:  true,
			desc:  "Less than",
		},
		{
			input: "wp\\nid",
			want:  true,
			desc:  "Backslash",
		},
		{
			input: "wp{a,b}",
			want:  true,
			desc:  "Braces",
		},
		{
			input: "wp*.txt",
			want:  true,
			desc:  "Unquoted star glob",
		},
		{
			input: "wp?.txt",
			want:  true,
			desc:  "Unquoted question glob",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := hasUnquotedMetachar(tt.input)
			assert.Equal(t, tt.want, got, tt.desc)
		})
	}
}

func TestValidatePathArg(t *testing.T) {
	ownedDocroots := []string{"/home/shuki/example.com/public_html"}

	tests := []struct {
		name     string
		path     string
		docroots []string
		wantErr  string
		desc     string
	}{
		{
			name:     "exact_match",
			path:     "/home/shuki/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "Path equals docroot exactly",
		},
		{
			name:     "inside_docroot",
			path:     "/home/shuki/example.com/public_html/wp-admin",
			docroots: ownedDocroots,
			wantErr:  "",
			desc:     "Path inside docroot",
		},
		{
			name:     "has_dotdot",
			path:     "/home/shuki/../other/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "Path contains .. (caught before EvalSymlinks)",
		},
		{
			name:     "outside_docroot",
			path:     "/home/otheruser/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "Path outside owned docroot",
		},
		{
			name:     "prefix_attack",
			path:     "/home/shukimalicious/example.com/public_html",
			docroots: ownedDocroots,
			wantErr:  ErrCodeBadPathArg,
			desc:     "Prefix attack (missing / boundary)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePathArg(tt.path, tt.docroots)

			if tt.wantErr == "" {
				assert.NoError(t, err, tt.desc)
			} else {
				require.Error(t, err, tt.desc)
				valErr, ok := err.(*ValidationError)
				require.True(t, ok)
				assert.Equal(t, tt.wantErr, valErr.Code, tt.desc)
			}
		})
	}
}

// Benchmark for ValidateCommand
func BenchmarkValidateCommand(b *testing.B) {
	ownedDocroots := []string{"/home/shuki/example.com/public_html"}
	cmd := "wp cron event run --path=/home/shuki/example.com/public_html --due-now"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ValidateCommand(cmd, ownedDocroots, "")
	}
}

// Benchmark for ValidateSchedule
func BenchmarkValidateSchedule(b *testing.B) {
	expr := "0 3 * * *"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ValidateSchedule(expr)
	}
}
