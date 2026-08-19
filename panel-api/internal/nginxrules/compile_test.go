package nginxrules

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestCompile(t *testing.T) {
	tests := []struct {
		name     string
		domain   *models.Domain
		want     string
		wantBool bool // true to check contains, false for exact match
	}{
		{
			name:   "nil domain returns empty string",
			domain: nil,
			want:   "",
		},
		{
			name: "empty rules returns empty string",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{},
			},
			want: "",
		},
		{
			name: "custom_header without always flag",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:  "custom_header",
						Name:  "X-Custom",
						Value: "test-value",
					},
				},
			},
			want:     `add_header X-Custom "test-value";`,
			wantBool: true,
		},
		{
			name: "custom_header with always flag true",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:   "custom_header",
						Name:   "X-Custom",
						Value:  "test-value",
						Always: boolPtr(true),
					},
				},
			},
			want:     `add_header X-Custom "test-value" always;`,
			wantBool: true,
		},
		{
			name: "custom_header with always flag false",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:   "custom_header",
						Name:   "X-Custom",
						Value:  "test-value",
						Always: boolPtr(false),
					},
				},
			},
			want:     `add_header X-Custom "test-value";`,
			wantBool: true,
		},
		{
			name: "custom_header with special characters",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:  "custom_header",
						Name:  "X-Quoted",
						Value: `test"value`,
					},
				},
			},
			want:     `add_header X-Quoted "test\"value";`,
			wantBool: true,
		},
		{
			name: "rewrite with default flag",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:        "rewrite",
						Pattern:     "^/old/(.*)$",
						Replacement: "/new/$1",
						Flag:        "",
					},
				},
			},
			want:     `rewrite ^/old/(.*)$ "/new/$1" last;`,
			wantBool: true,
		},
		{
			name: "rewrite with explicit flag",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:        "rewrite",
						Pattern:     "^/old/(.*)$",
						Replacement: "/new/$1",
						Flag:        "permanent",
					},
				},
			},
			want:     `rewrite ^/old/(.*)$ "/new/$1" permanent;`,
			wantBool: true,
		},
		{
			name: "proxy_pass with headers",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:   "proxy_pass",
						Path:   "/api",
						Target: "http://localhost:9000",
					},
				},
			},
			want:     `location ^~ /api {`,
			wantBool: true,
		},
		{
			name: "proxy_pass with location needing quotes",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:   "proxy_pass",
						Path:   "/api v1",
						Target: "http://localhost:9000",
					},
				},
			},
			want:     `location ^~ "/api v1" {`,
			wantBool: true,
		},
		{
			name: "ip_access allow_list mode",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type: "ip_access",
						Path: "/admin",
						Mode: "allow_list",
						IPs:  []string{"192.168.1.0/24", "10.0.0.1"},
					},
				},
			},
			want:     `allow 192.168.1.0/24;`,
			wantBool: true,
		},
		{
			name: "ip_access deny_list mode",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type: "ip_access",
						Path: "/blocked",
						Mode: "deny_list",
						IPs:  []string{"203.0.113.0/24"},
					},
				},
			},
			want:     `deny 203.0.113.0/24;`,
			wantBool: true,
		},
		{
			name: "php_setting",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:  "php_setting",
						Name:  "upload_max_filesize",
						Value: "100M",
					},
				},
			},
			want:     `fastcgi_param PHP_VALUE "upload_max_filesize=100M";`,
			wantBool: true,
		},
		{
			name: "max_upload_size",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type: "max_upload_size",
						Size: "50M",
					},
				},
			},
			want:     `client_max_body_size 50M;`,
			wantBool: true,
		},
		{
			name: "unknown rule type is silently skipped",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type: "unknown_rule",
					},
				},
			},
			want: "",
		},
		{
			name: "multiple rules in order",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:  "custom_header",
						Name:  "X-Test",
						Value: "value1",
					},
					{
						Type: "max_upload_size",
						Size: "100M",
					},
				},
			},
			want:     `add_header X-Test "value1";`,
			wantBool: true,
		},
		{
			name: "backslash escaping in values",
			domain: &models.Domain{
				NginxRules: []models.NginxRule{
					{
						Type:  "custom_header",
						Name:  "X-Path",
						Value: `C:\path\to\file`,
					},
				},
			},
			want:     `add_header X-Path "C:\\path\\to\\file";`,
			wantBool: true,
		},
		{
			// GH #1175: a reverse-proxy domain carries no persisted rule;
			// the proxy_pass block is synthesised from the column.
			name: "reverse_proxy_port synthesises a root proxy_pass",
			domain: &models.Domain{
				ReverseProxyPort: 30000,
			},
			want:     "proxy_pass http://127.0.0.1:30000;",
			wantBool: true,
		},
		{
			name: "reverse_proxy_port sets the forwarding headers",
			domain: &models.Domain{
				ReverseProxyPort: 34567,
			},
			want:     "proxy_set_header X-Forwarded-Proto $scheme;",
			wantBool: true,
		},
		{
			name: "no rules and no reverse proxy is empty",
			domain: &models.Domain{
				ReverseProxyPort: 0,
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compile(tt.domain)
			if tt.wantBool {
				// "contains" check: the emitted vhost fragment must carry the
				// expected directive verbatim.
				if !strings.Contains(got, tt.want) {
					t.Errorf("Compile(%v) = %q, want it to contain %q", tt.domain, got, tt.want)
				}
			} else {
				// Exact match
				if got != tt.want {
					t.Errorf("Compile(%v) = %q, want %q", tt.domain, got, tt.want)
				}
			}
		})
	}
}

func TestQuoteNginxString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "simple",
			want:  `"simple"`,
		},
		{
			input: `has"quotes`,
			want:  `"has\"quotes"`,
		},
		{
			input: `has\backslash`,
			want:  `"has\\backslash"`,
		},
		{
			input: `both"and\`,
			want:  `"both\"and\\"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := quoteNginxString(tt.input)
			if got != tt.want {
				t.Errorf("quoteNginxString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestQuoteNginxLocation(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "/simple",
			want:  "/simple",
		},
		{
			input: "/path with spaces",
			want:  `"/path with spaces"`,
		},
		{
			input: `/path	with	tabs`,
			want:  `"/path	with	tabs"`,
		},
		{
			input: `/path"with"quotes`,
			want:  `"/path\"with\"quotes"`,
		},
		{
			input: `/path'with'single`,
			want:  `"/path'with'single"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := quoteNginxLocation(tt.input)
			if got != tt.want {
				t.Errorf("quoteNginxLocation(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}

// TestCompileProxyPassDefaults pins the upload-safe defaults baked into
// every proxy_pass block (GH#172): http/1.1 keepalive, request buffering
// off, and default timeouts. Non-websocket proxies get Connection "";
// websocket proxies get Connection "upgrade" + the Upgrade header.
func TestCompileProxyPassDefaults(t *testing.T) {
	d := &models.Domain{
		NginxRules: []models.NginxRule{
			{Type: "proxy_pass", Path: "/app", Target: "http://127.0.0.1:8080"},
		},
	}
	got := Compile(d)
	for _, want := range []string{
		"location ^~ /app {",
		"proxy_pass http://127.0.0.1:8080;",
		"proxy_http_version 1.1;",
		"proxy_request_buffering off;",
		`proxy_set_header Connection "";`,
		"proxy_connect_timeout 300s;",
		"proxy_send_timeout 300s;",
		"proxy_read_timeout 300s;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("proxy_pass output missing %q\n--- got ---\n%s", want, got)
		}
	}
	// Non-websocket must NOT carry the upgrade header.
	if strings.Contains(got, "Upgrade $http_upgrade") {
		t.Errorf("non-websocket proxy unexpectedly set Upgrade header:\n%s", got)
	}
}

// TestCompileProxyPassWebsocket verifies the websocket branch still emits
// the Upgrade/Connection-upgrade pair and an overridable read timeout.
func TestCompileProxyPassWebsocket(t *testing.T) {
	ws := true
	d := &models.Domain{
		NginxRules: []models.NginxRule{
			{Type: "proxy_pass", Path: "/ws", Target: "http://127.0.0.1:9000", Websocket: &ws, ReadTimeout: "600s"},
		},
	}
	got := Compile(d)
	for _, want := range []string{
		"proxy_http_version 1.1;",
		"proxy_set_header Upgrade $http_upgrade;",
		`proxy_set_header Connection "upgrade";`,
		"proxy_read_timeout 600s;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("websocket proxy output missing %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, `Connection "";`) {
		t.Errorf("websocket proxy should not emit empty Connection header:\n%s", got)
	}
}

func TestCompile_StaticAlias(t *testing.T) {
	d := &models.Domain{NginxRules: models.NginxRules{
		{Type: "static_alias", Path: "/dj/static/", Target: "/home/u/djtest/staticfiles/"},
	}}
	got := Compile(d)
	for _, want := range []string{
		"location ^~ /dj/static/ {",
		"alias /home/u/djtest/staticfiles/;",
		"expires 30d;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("compiled output missing %q:\n%s", want, got)
		}
	}
}

// TestReverseProxySynthesisEdges covers GH #1175 behaviour the contains/exact
// table can't express: suppression when the tenant already has a root
// proxy_pass, and that synthesis never mutates the domain's backing slice.
func TestReverseProxySynthesisEdges(t *testing.T) {
	t.Run("explicit root proxy_pass suppresses the synthetic loopback one", func(t *testing.T) {
		d := &models.Domain{
			ReverseProxyPort: 30001,
			NginxRules: []models.NginxRule{
				{Type: "proxy_pass", Path: "/", Target: "http://example.test:8080"},
			},
		}
		got := Compile(d)
		if strings.Contains(got, "127.0.0.1:30001") {
			t.Fatalf("synthetic loopback proxy_pass should be suppressed, got:\n%s", got)
		}
		if strings.Count(got, "location ^~ /") != 1 {
			t.Fatalf("want exactly one root location, got:\n%s", got)
		}
	})

	t.Run("synthesis does not mutate the domain NginxRules slice", func(t *testing.T) {
		d := &models.Domain{
			ReverseProxyPort: 30002,
			NginxRules: []models.NginxRule{
				{Type: "custom_header", Name: "X-A", Value: "1"},
			},
		}
		_ = Compile(d)
		if len(d.NginxRules) != 1 {
			t.Fatalf("Compile mutated NginxRules: len=%d", len(d.NginxRules))
		}
	})
}
