package main

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
)

// JAB-318 AC5 — CLI door half of the domain-config validation parity matrix.
//
// The HTTP half (internal/api/domains_config_parity_test.go) drives the PATCH
// door; this half drives the CLI's validateDomainSetInput over the SAME case
// values and anchors each row to the SAME api.* validator both doors are meant
// to call. Together they pin one validation contract from both sides: if the CLI
// stops calling a shared validator, its row here diverges from the anchor and
// reddens (the slice-1 bug shape, caught from the CLI side).
//
// ssl_mode is not part of validateDomainSetInput — the CLI validates it inline
// against models.SSLModeProtectedRefusal, which is source-pinned in
// domain_advanced_cmd_ssl_mode_test.go (cmd/server runs on the global repo, no
// injection seam). cache_enabled has a CLI flag but no general HTTP PATCH key, so
// it is out of the shared matrix by construction (see the HTTP file header).
//
// The one deliberate asymmetry is redirect_all_type: the CLI accepts the friendly
// aliases "permanent"/"temporary" and normalises them to "301"/"302"; the HTTP
// door accepts only the numeric codes. TestDomainConfig_CLIRedirectTypeAliases
// proves this is a CLI-input superset, not a drift — the normalised value the CLI
// persists is a value the HTTP door accepts, so both doors end at the same stored
// redirect type.

func strptr(s string) *string { return &s }

func cliAcceptNginx(v string) bool { return api.ValidateNginxDirectivesAdmin(v) == "" }

func cliAcceptRedirectTo(v string) bool {
	t := strings.TrimSpace(v)
	return t == "" || api.ValidateRedirectURL(t) == nil
}

func cliAcceptRedirectType(v string) bool {
	t := strings.TrimSpace(v)
	return t == "" || api.IsValidRedirectType(t)
}

func cliAcceptIndexPriority(v string) bool { return api.IsValidIndexPriority(strings.TrimSpace(v)) }

// TestDomainConfig_CLIParityMatrix runs validateDomainSetInput per field over the
// shared matrix and asserts each accept/reject verdict matches the api.* anchor.
// The redirect_all_type aliases are excluded here and asserted separately, since
// they are the one intended CLI-superset case.
func TestDomainConfig_CLIParityMatrix(t *testing.T) {
	fields := []struct {
		name   string
		accept func(string) bool
		run    func(v string) error
		values []string
	}{
		{
			name:   "nginx_directives",
			accept: cliAcceptNginx,
			run:    func(v string) error { _, err := validateDomainSetInput(strptr(v), nil, nil, nil); return err },
			values: []string{
				`add_header X-Frame-Options "DENY";`,
				``,
				`root /tmp;`,
				`access_log off;`,
			},
		},
		{
			name:   "redirect_all_to",
			accept: cliAcceptRedirectTo,
			run:    func(v string) error { _, err := validateDomainSetInput(nil, strptr(v), nil, nil); return err },
			values: []string{
				`https://example.com/`,
				``,
				`javascript:alert(1)`,
				`https://`,
				`/relative`,
			},
		},
		{
			name:   "redirect_type_canonical",
			accept: cliAcceptRedirectType,
			run:    func(v string) error { _, err := validateDomainSetInput(nil, nil, strptr(v), nil); return err },
			values: []string{
				`301`,
				`308`,
				``,
				`303`,
				`bogus`,
			},
		},
		{
			name:   "index_priority",
			accept: cliAcceptIndexPriority,
			run:    func(v string) error { _, err := validateDomainSetInput(nil, nil, nil, strptr(v)); return err },
			values: []string{
				`php_first`,
				`full`,
				``,
				`bogus`,
			},
		},
	}

	for _, f := range fields {
		f := f
		t.Run(f.name, func(t *testing.T) {
			var accepts, rejects int
			for _, v := range f.values {
				want := f.accept(v)
				got := f.run(v) == nil
				if got != want {
					t.Errorf("%s=%q: CLI accept=%v, anchor accept=%v — CLI door diverged from its api.* validator",
						f.name, v, got, want)
				}
				if want {
					accepts++
				} else {
					rejects++
				}
			}
			if accepts == 0 || rejects == 0 {
				t.Fatalf("%s matrix is vacuous: %d accepts / %d rejects — need at least one of each", f.name, accepts, rejects)
			}
		})
	}
}

// TestDomainConfig_CLIRedirectTypeAliases pins the one deliberate HTTP/CLI
// asymmetry: the CLI accepts "permanent"/"temporary" (case-insensitively) and
// normalises them to "301"/"302". The assertion that closes the parity argument
// is the last one — the normalised value is a value the HTTP door ACCEPTS
// (api.IsValidRedirectType), so the CLI's leniency only widens accepted INPUT,
// never the persisted result.
func TestDomainConfig_CLIRedirectTypeAliases(t *testing.T) {
	aliases := []struct {
		in       string
		wantNorm string
	}{
		{"permanent", "301"},
		{"temporary", "302"},
		{"Permanent", "301"}, // case-insensitive
	}
	for _, a := range aliases {
		a := a
		t.Run(a.in, func(t *testing.T) {
			// HTTP door rejects the raw alias...
			if api.IsValidRedirectType(a.in) {
				t.Fatalf("%q must NOT be an HTTP-accepted redirect type — the asymmetry premise is that only the CLI accepts it", a.in)
			}
			// ...but the CLI accepts it and normalises it.
			norm, err := validateDomainSetInput(nil, nil, strptr(a.in), nil)
			if err != nil {
				t.Fatalf("CLI must accept alias %q, got error: %v", a.in, err)
			}
			if norm != a.wantNorm {
				t.Fatalf("CLI normalised %q to %q, want %q", a.in, norm, a.wantNorm)
			}
			// The parity-closing assertion: what the CLI persists is a value the
			// HTTP door accepts, so both doors end at the same stored type.
			if !api.IsValidRedirectType(norm) {
				t.Fatalf("CLI normalised %q to %q, which the HTTP door would REJECT — the doors would drift on the persisted value", a.in, norm)
			}
		})
	}
}
