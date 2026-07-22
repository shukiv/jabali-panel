package api

import (
	"testing"
)

func TestValidateNginxDirectives(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantError bool
		errMsg    string
	}{
		{
			name:      "empty string",
			input:     "",
			wantError: false,
		},
		{
			name:      "whitespace only",
			input:     "   \n  \t  \n",
			wantError: false,
		},
		{
			name:      "comment only",
			input:     "# this is a comment",
			wantError: false,
		},
		{
			name:      "multiple comment lines",
			input:     "# comment 1\n# comment 2\n# comment 3",
			wantError: false,
		},
		{
			name:      "simple add_header",
			input:     `add_header X-Foo "bar";`,
			wantError: false,
		},
		{
			name:      "proxy_pass basic (JAB-66: removed from raw allowlist)",
			input:     "proxy_pass http://localhost:3000;",
			wantError: true,
			errMsg:    "forbidden directive: proxy_pass",
		},
		{
			name:      "proxy_pass with trailing comment (JAB-66: forbidden)",
			input:     "proxy_pass http://localhost:3000; # forward traffic",
			wantError: true,
			errMsg:    "forbidden directive: proxy_pass",
		},
		{
			name:      "multiple valid directives",
			input:     "add_header X-Foo bar;\nproxy_set_header X-Real-IP $remote_addr;",
			wantError: false,
		},
		{
			name:      "hash inside double-quoted string is not a comment",
			input:     `add_header X-Foo "value with # hash";`,
			wantError: false,
		},
		{
			name:      "multiple directives with hash in string",
			input:     `add_header X-Foo "# not a comment";\nadd_header X-Bar baz;`,
			wantError: false,
		},
		{
			name:      "hash inside single-quoted string is not a comment",
			input:     `set $var 'value with # hash';`,
			wantError: false,
		},
		{
			name:      "mixed case directive (case-insensitive)",
			input:     "AdD_HeAdEr X-Foo bar;",
			wantError: false,
		},
		{
			name:      "mixed case proxy_pass (JAB-66: forbidden regardless of case)",
			input:     "PROXY_PASS http://localhost:3000;",
			wantError: true,
			errMsg:    "forbidden directive: proxy_pass",
		},
		{
			name:      "try_files directive",
			input:     "try_files $uri /index.php?$query_string;",
			wantError: false,
		},
		{
			name:      "rewrite directive",
			input:     "rewrite ^/old/(.*) /new/$1 permanent;",
			wantError: false,
		},
		{
			name:      "if block with directives",
			input:     "if ($request_method = POST) {\nreturn 405;\n}",
			wantError: false,
		},
		{
			name:      "location block",
			input:     "location /api {\nadd_header X-Api 1;\n}",
			wantError: false,
		},
		{
			name:      "nested location",
			input:     "location / {\nif ($condition) {\nreturn 200;\n}\n}",
			wantError: false,
		},
		{
			name:      "three levels of nesting",
			input:     "location / {\nif ($cond) {\nlimit_except GET HEAD {\ndeny all;\n}\n}\n}",
			wantError: false,
		},
		{
			name:      "forbidden: include /etc/passwd",
			input:     "include /etc/passwd;",
			wantError: true,
			errMsg:    "forbidden directive: include",
		},
		{
			name:      "forbidden: root /etc",
			input:     "root /etc;",
			wantError: true,
			errMsg:    "forbidden directive: root",
		},
		{
			name:      "forbidden: alias /etc/",
			input:     "alias /etc/;",
			wantError: true,
			errMsg:    "forbidden directive: alias",
		},
		{
			name:      "forbidden: access_log off",
			input:     "access_log off;",
			wantError: true,
			errMsg:    "forbidden directive: access_log",
		},
		{
			name:      "forbidden: access_log /var/log/nginx/access.log",
			input:     "access_log /var/log/nginx/access.log;",
			wantError: true,
			errMsg:    "forbidden directive: access_log",
		},
		{
			name:      "forbidden: error_log",
			input:     "error_log /var/log/nginx/error.log warn;",
			wantError: true,
			errMsg:    "forbidden directive: error_log",
		},
		{
			name:      "forbidden: ssl_certificate",
			input:     "ssl_certificate /etc/ssl/certs/example.com.crt;",
			wantError: true,
			errMsg:    "forbidden directive: ssl_certificate",
		},
		{
			name:      "forbidden: ssl_certificate_key",
			input:     "ssl_certificate_key /etc/ssl/private/example.com.key;",
			wantError: true,
			errMsg:    "forbidden directive: ssl_certificate_key",
		},
		{
			name:      "forbidden: load_module",
			input:     "load_module /usr/lib/nginx/modules/ngx_module.so;",
			wantError: true,
			errMsg:    "forbidden directive: load_module",
		},
		{
			name:      "forbidden: lua_code_cache off",
			input:     "lua_code_cache off;",
			wantError: true,
			errMsg:    "forbidden directive: lua_code_cache",
		},
		{
			name:      "forbidden: perl_modules",
			input:     "perl_modules /usr/share/perl/5.26;",
			wantError: true,
			errMsg:    "forbidden directive: perl_modules",
		},
		{
			name:      "forbidden: listen",
			input:     "listen 80;",
			wantError: true,
			errMsg:    "forbidden directive: listen",
		},
		{
			name:      "forbidden: server_name",
			input:     "server_name example.com;",
			wantError: true,
			errMsg:    "forbidden directive: server_name",
		},
		{
			name:      "forbidden: env VAR",
			input:     "env MY_VAR;",
			wantError: true,
			errMsg:    "forbidden directive: env",
		},
		{
			name:      "forbidden: user nobody",
			input:     "user nobody;",
			wantError: true,
			errMsg:    "forbidden directive: user",
		},
		{
			name:      "forbidden: worker_processes",
			input:     "worker_processes 4;",
			wantError: true,
			errMsg:    "forbidden directive: worker_processes",
		},
		{
			name:      "forbidden directive in middle of valid directives",
			input:     "add_header X-Foo bar;\nroot /etc;\nadd_header X-Baz qux;",
			wantError: true,
			errMsg:    "forbidden directive: root",
		},
		{
			name:      "unbalanced: extra closing brace",
			input:     "add_header X-Foo bar;\n}",
			wantError: true,
			errMsg:    "forbidden directive: unbalanced braces (extra closing })",
		},
		{
			name:      "unbalanced: unclosed opening brace",
			input:     "location / {",
			wantError: true,
			errMsg:    "forbidden directive: unbalanced braces (unclosed {)",
		},
		{
			name:      "nesting depth exceeded: 4 levels",
			input:     "location / {\nif ($c) {\nlimit_except GET {\nreturn 405;\nlocation /deep {\ndeny all;\n}\n}\n}\n}",
			wantError: true,
			errMsg:    "forbidden directive: nesting depth exceeded",
		},
		{
			name:      "null byte injection",
			input:     "add_header X-Foo bar\x00baz;",
			wantError: true,
			errMsg:    "forbidden directive: null byte detected",
		},
		{
			name:      "gzip directives",
			input:     "gzip on;\ngzip_types text/plain;\ngzip_comp_level 6;",
			wantError: false,
		},
		{
			name:      "limit_req_zone directive",
			input:     "limit_req_zone $binary_remote_addr zone=one:10m rate=10r/s;",
			wantError: false,
		},
		{
			name:      "fastcgi_param directive",
			input:     "fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;",
			wantError: false,
		},
		{
			name:      "auth_basic_user_file (JAB-66: removed — file-read oracle)",
			input:     "auth_basic_user_file /etc/nginx/.htpasswd;",
			wantError: true,
			errMsg:    "forbidden directive: auth_basic_user_file",
		},
		{
			name:      "empty line between directives",
			input:     "add_header X-Foo bar;\n\nproxy_set_header X-Real-IP $remote_addr;",
			wantError: false,
		},
		{
			name:      "trailing whitespace and comments",
			input:     "add_header X-Foo bar;  \t  # comment\nproxy_set_header X-Real-IP $remote_addr; # another",
			wantError: false,
		},
		{
			name:      "quoted string with escaped quote",
			input:     `add_header X-Foo "bar\"baz";`,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNginxDirectives(tt.input)
			if (err != "") != tt.wantError {
				t.Errorf("validateNginxDirectives() error = %q, wantError %v", err, tt.wantError)
				return
			}
			if tt.wantError && tt.errMsg != "" && err != tt.errMsg {
				// Check if the expected message is a substring (for some error messages)
				if !contains(err, tt.errMsg) {
					t.Errorf("validateNginxDirectives() error = %q, want substring %q", err, tt.errMsg)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestStripComments(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "add_header X-Foo bar;",
			expected: "add_header X-Foo bar;",
		},
		{
			input:    "add_header X-Foo bar; # this is a comment",
			expected: "add_header X-Foo bar; ",
		},
		{
			input:    "# full line comment",
			expected: "",
		},
		{
			input:    `add_header X "value with # hash";`,
			expected: `add_header X "value with # hash";`,
		},
		{
			input:    `add_header X 'value with # hash';`,
			expected: `add_header X 'value with # hash';`,
		},
		{
			input:    `add_header X "quoted"; # comment`,
			expected: `add_header X "quoted"; `,
		},
		{
			input:    `set $x 'single'; # comment after single-quoted`,
			expected: `set $x 'single'; `,
		},
		{
			input:    "no comment here",
			expected: "no comment here",
		},
		{
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripComments(tt.input)
			if got != tt.expected {
				t.Errorf("stripComments(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExtractDirective(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "add_header X-Foo bar;",
			expected: "add_header",
		},
		{
			input:    "proxy_pass http://localhost:3000;",
			expected: "proxy_pass",
		},
		{
			input:    "   location   /api {",
			expected: "location",
		},
		{
			input:    "if ($cond) {",
			expected: "if",
		},
		{
			input:    "}",
			expected: "}",
		},
		{
			input:    "{",
			expected: "{",
		},
		{
			input:    "",
			expected: "",
		},
		{
			input:    "   ",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractDirective(tt.input)
			if got != tt.expected {
				t.Errorf("extractDirective(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// GH #526: a tenant may edit their own domain's docroot but is confined to the
// domain's OWN tree — a stricter check than the admin whole-home one.
func TestValidateTenantDocumentRoot(t *testing.T) {
	const u, d = "alice", "example.com"
	base := "/home/alice/domains/example.com"
	ok := []string{
		"",                     // reset to default
		base,                   // the domain root
		base + "/public_html",  // the default
		base + "/myapp/public", // a framework public/ subdir (the #526 use case)
		base + "/public_html/sub",
	}
	for _, dr := range ok {
		if err := validateTenantDocumentRoot(dr, u, d); err != nil {
			t.Errorf("expected %q valid, got %v", dr, err)
		}
	}
	bad := []string{
		"/home/alice/domains/other.com/public_html", // another domain (theirs, but out of tree)
		"/home/alice/secret",                        // elsewhere in the home
		"/home/bob/domains/example.com/public_html", // another user
		"/etc/passwd",                             // outside home
		base + "/../other.com/public_html",        // traversal
		"/home/alice/domains/example.com/../evil", // traversal
	}
	for _, dr := range bad {
		if err := validateTenantDocumentRoot(dr, u, d); err == nil {
			t.Errorf("expected %q rejected, got nil", dr)
		}
	}
}
