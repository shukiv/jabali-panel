package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A rendered kratos.yml fragment carrying every shape the panel host appears
// in: the https://<host> URLs (base_url, cors origin, return URLs, ui_url,
// webauthn origins) AND the bare webauthn/passkey rp.id. The DSN (a unix
// socket, no host) and the secrets must survive a rehost byte-for-byte.
const kratosFixtureHost = "old.example.com"

func kratosFixture(host string) string {
	return `dsn: "mysql://kratosuser:s3cr3tpw@unix(/var/run/mysqld/mysqld.sock)/kratosdb?parseTime=true"

serve:
  public:
    base_url: "https://` + host + `:8443/.ory/"
    cors:
      allowed_origins:
        - "https://` + host + `:8443"

selfservice:
  default_browser_return_url: "https://` + host + `:8443/dashboard"
  allowed_return_urls:
    - "https://` + host + `:8443"
    - "https://` + host + `:8443/login"
  methods:
    passkey:
      config:
        rp:
          id: "` + host + `"
          origins:
            - "https://` + host + `:8443"
    webauthn:
      config:
        rp:
          id: "` + host + `"
          origins:
            - "https://` + host + `:8443"
  flows:
    login:
      ui_url: "https://` + host + `:8443/login"

secrets:
  cookie:
    - deadbeefcafe0000deadbeefcafe0000
  default:
    - 00000000111122223333444455556666
`
}

func TestKratosConfigBaseURLHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       string
		wantHost string
		wantOK   bool
	}{
		{"with port", `    base_url: "https://mx.example.com:8443/.ory/"`, "mx.example.com", true},
		{"portless", `    base_url: "https://mx.example.com/.ory/"`, "mx.example.com", true},
		{"no base_url", "serve:\n  public:\n    host: unix:/run/x.sock\n", "", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := kratosConfigBaseURLHost([]byte(tt.in))
			if ok != tt.wantOK || got != tt.wantHost {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, ok, tt.wantHost, tt.wantOK)
			}
		})
	}
}

func TestRewriteKratosHost(t *testing.T) {
	t.Parallel()

	t.Run("rewrites every shape, preserves DSN and secrets", func(t *testing.T) {
		t.Parallel()
		in := kratosFixture(kratosFixtureHost)
		out, n := rewriteKratosHost([]byte(in), kratosFixtureHost, "new.host.com")
		s := string(out)
		// base_url + cors origin + return_url + return_url/login + ui_url +
		// ui_url + passkey rp.id + passkey origin + webauthn rp.id + webauthn origin = 10.
		if n != 10 {
			t.Fatalf("replacements = %d, want 10", n)
		}
		if strings.Contains(s, kratosFixtureHost) {
			t.Fatalf("old host still present:\n%s", s)
		}
		if !strings.Contains(s, `base_url: "https://new.host.com:8443/.ory/"`) {
			t.Errorf("base_url not rewritten:\n%s", s)
		}
		if !strings.Contains(s, `id: "new.host.com"`) {
			t.Errorf("webauthn/passkey rp.id not rewritten")
		}
		// Port suffix preserved on URLs.
		if !strings.Contains(s, `https://new.host.com:8443/login`) {
			t.Errorf("port suffix not preserved on ui_url")
		}
		// DSN + secrets byte-for-byte.
		if !strings.Contains(s, `mysql://kratosuser:s3cr3tpw@unix(/var/run/mysqld/mysqld.sock)/kratosdb?parseTime=true`) {
			t.Errorf("DSN mutated")
		}
		if !strings.Contains(s, "deadbeefcafe0000deadbeefcafe0000") || !strings.Contains(s, "00000000111122223333444455556666") {
			t.Errorf("secrets mutated")
		}
	})

	t.Run("superstring safe both directions", func(t *testing.T) {
		t.Parallel()
		in := "a: \"https://example.com:8443\"\nb: \"https://mx.example.com:8443\"\n"
		// Rename example.com -> z.com must NOT touch mx.example.com.
		out, n := rewriteKratosHost([]byte(in), "example.com", "z.com")
		s := string(out)
		if n != 1 {
			t.Fatalf("replacements = %d, want 1", n)
		}
		if !strings.Contains(s, `https://z.com:8443`) || !strings.Contains(s, `https://mx.example.com:8443`) {
			t.Fatalf("superstring boundary broken:\n%s", s)
		}
		// Heal-back direction: mx.example.com -> example.com.
		in2 := "a: \"https://mx.example.com:8443\"\n"
		out2, n2 := rewriteKratosHost([]byte(in2), "mx.example.com", "example.com")
		if n2 != 1 || !strings.Contains(string(out2), `https://example.com:8443`) {
			t.Fatalf("heal-back failed: n=%d out=%q", n2, out2)
		}
	})

	t.Run("no occurrences", func(t *testing.T) {
		t.Parallel()
		out, n := rewriteKratosHost([]byte("nothing here\n"), "absent.example.com", "x.com")
		if n != 0 || string(out) != "nothing here\n" {
			t.Fatalf("expected no-op, got n=%d out=%q", n, out)
		}
	})
}

// withKratosRehostSeams points the verb at a temp kratos.yml and a recording
// reload fn for the duration of one (non-parallel) test.
func withKratosRehostSeams(t *testing.T, contents string, reloadErr error) (path string, restarts *int) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "kratos.yml")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	prevPath, prevReload := kratosConfigPath, kratosRehostReloadFn
	kratosConfigPath = path
	kratosRehostReloadFn = func(context.Context) error { n++; return reloadErr }
	t.Cleanup(func() { kratosConfigPath = prevPath; kratosRehostReloadFn = prevReload })
	return path, &n
}

func callRehost(t *testing.T, hostname string) (kratosConfigRehostResponse, error) {
	t.Helper()
	raw, err := kratosConfigRehostHandler(context.Background(), json.RawMessage(`{"hostname":"`+hostname+`"}`))
	if err != nil {
		return kratosConfigRehostResponse{}, err
	}
	// The handler returns the typed struct as `any`; round-trip through JSON to
	// assert on it the way the wire would.
	b, _ := json.Marshal(raw)
	var resp kratosConfigRehostResponse
	if uerr := json.Unmarshal(b, &resp); uerr != nil {
		t.Fatalf("unmarshal response: %v", uerr)
	}
	return resp, nil
}

func TestKratosConfigRehostHandler_RewritesAndRestarts(t *testing.T) {
	path, restarts := withKratosRehostSeams(t, kratosFixture(kratosFixtureHost), nil)

	resp, err := callRehost(t, "new.host.com")
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !resp.Rewritten || resp.Replacements != 10 {
		t.Fatalf("resp = %+v, want rewritten with 10 replacements", resp)
	}
	if *restarts != 1 {
		t.Fatalf("restarts = %d, want 1", *restarts)
	}
	out, _ := os.ReadFile(path)
	if strings.Contains(string(out), kratosFixtureHost) {
		t.Fatalf("old host survived on disk")
	}
	// Mode preserved by the atomic write.
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o, want 640", fi.Mode().Perm())
	}
}

func TestKratosConfigRehostHandler_NoChurnWhenCurrent(t *testing.T) {
	_, restarts := withKratosRehostSeams(t, kratosFixture(kratosFixtureHost), nil)

	resp, err := callRehost(t, kratosFixtureHost) // same host
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if resp.Rewritten {
		t.Fatalf("expected no rewrite when host already current")
	}
	if *restarts != 0 {
		t.Fatalf("restarts = %d, want 0 (no churn)", *restarts)
	}
}

func TestKratosConfigRehostHandler_RejectsInvalidHost(t *testing.T) {
	path, restarts := withKratosRehostSeams(t, kratosFixture(kratosFixtureHost), nil)
	before, _ := os.ReadFile(path)

	if _, err := kratosConfigRehostHandler(context.Background(), json.RawMessage(`{"hostname":"bad host!!"}`)); err == nil {
		t.Fatal("expected error for invalid hostname")
	}
	if *restarts != 0 {
		t.Fatalf("restarts = %d, want 0", *restarts)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file mutated on invalid hostname")
	}
}

func TestKratosConfigRehostHandler_MissingFile(t *testing.T) {
	_, restarts := withKratosRehostSeams(t, "", nil) // no file written
	if _, err := callRehost(t, "new.host.com"); err == nil {
		t.Fatal("expected error for missing kratos.yml")
	}
	if *restarts != 0 {
		t.Fatalf("restarts = %d, want 0", *restarts)
	}
}

func TestKratosConfigRehostHandler_RestartFailurePropagates(t *testing.T) {
	_, restarts := withKratosRehostSeams(t, kratosFixture(kratosFixtureHost), context.DeadlineExceeded)
	if _, err := callRehost(t, "new.host.com"); err == nil {
		t.Fatal("expected restart failure to propagate")
	}
	if *restarts != 1 {
		t.Fatalf("restarts = %d, want 1 (attempted)", *restarts)
	}
}
