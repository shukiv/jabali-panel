package mailaddr

import (
	"errors"
	"strings"
	"testing"
)

func TestCanonicalise(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantLocal  string
		wantDomain string
		wantErr    error // nil = no error expected; sentinel = errors.Is target
	}{
		// --- happy paths -----------------------------------------------
		{
			name:       "simple ASCII",
			input:      "alice@example.com",
			wantLocal:  "alice",
			wantDomain: "example.com",
		},
		{
			name:       "uppercase local and domain both lowered",
			input:      "Alice@EXAMPLE.COM",
			wantLocal:  "alice",
			wantDomain: "example.com",
		},
		{
			name:       "plus-tag stripped (RFC 5233)",
			input:      "alice+newsletter@example.com",
			wantLocal:  "alice",
			wantDomain: "example.com",
		},
		{
			name:       "plus-tag with dots in tag",
			input:      "alice+a.b.c@example.com",
			wantLocal:  "alice",
			wantDomain: "example.com",
		},
		{
			name:       "local with dots, underscore, hyphen",
			input:      "first.last_name-2@sub.example.com",
			wantLocal:  "first.last_name-2",
			wantDomain: "sub.example.com",
		},
		{
			name:       "IDN domain (Chinese) encoded to punycode",
			input:      "alice@中国.cn",
			wantLocal:  "alice",
			wantDomain: "xn--fiqs8s.cn",
		},
		{
			name:       "IDN domain (Cyrillic)",
			input:      "alice@россия.рф",
			wantLocal:  "alice",
			wantDomain: "xn--h1alffa9f.xn--p1ai",
		},

		// --- structural failures ---------------------------------------
		{name: "empty", input: "", wantErr: ErrEmpty},
		{name: "no at-sign", input: "aliceexample.com", wantErr: ErrNoAtSign},
		{name: "multiple at-signs", input: "alice@example@com", wantErr: ErrMultipleAtSigns},
		{name: "empty local", input: "@example.com", wantErr: ErrLocalEmpty},
		{name: "empty local after strip", input: "+tag@example.com", wantErr: ErrLocalEmpty},
		{name: "empty domain", input: "alice@", wantErr: ErrDomainEmpty},

		// --- length bounds ---------------------------------------------
		{
			name:    "local over 64 octets",
			input:   strings.Repeat("a", 65) + "@example.com",
			wantErr: ErrLocalTooLong,
		},
		{
			name:    "domain over 253 octets",
			input:   "alice@" + strings.Repeat("a.", 130) + "com",
			wantErr: ErrDomainTooLong,
		},
		{
			name:       "local exactly 64 ok",
			input:      strings.Repeat("a", 64) + "@example.com",
			wantLocal:  strings.Repeat("a", 64),
			wantDomain: "example.com",
		},

		// --- charset / injection guards --------------------------------
		{name: "non-ASCII local", input: "álice@example.com", wantErr: ErrLocalNonASCII},
		{name: "semicolon in local", input: "alice;@example.com", wantErr: ErrLocalShellMeta},
		{name: "backtick in local", input: "al`ice@example.com", wantErr: ErrLocalShellMeta},
		{name: "newline in local", input: "alice\n@example.com", wantErr: ErrLocalShellMeta},
		{name: "dollar in local", input: "alice$x@example.com", wantErr: ErrLocalShellMeta},
		{name: "pipe in domain", input: "alice@example.com|id", wantErr: ErrDomainShellMeta},
		{name: "space in domain", input: "alice@exa mple.com", wantErr: ErrDomainShellMeta},

		// --- IDN failure path ------------------------------------------
		{name: "leading hyphen in IDN label", input: "alice@-bad.example.com", wantErr: ErrIDNA},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			local, domain, err := Canonicalise(tt.input)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err: got %v, want Is(%v)", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if local != tt.wantLocal {
				t.Errorf("local: got %q, want %q", local, tt.wantLocal)
			}
			if domain != tt.wantDomain {
				t.Errorf("domain: got %q, want %q", domain, tt.wantDomain)
			}
		})
	}
}

// TestCanonicalise_Idempotent asserts that canonicalising an already-canonical
// address returns the same value. Guards against silent re-encoding drift.
func TestCanonicalise_Idempotent(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"alice@example.com",
		"first.last_name-2@sub.example.com",
	}
	for _, in := range inputs {
		local1, domain1, err := Canonicalise(in)
		if err != nil {
			t.Fatalf("round 1 err on %q: %v", in, err)
		}
		local2, domain2, err := Canonicalise(local1 + "@" + domain1)
		if err != nil {
			t.Fatalf("round 2 err on %q: %v", in, err)
		}
		if local1 != local2 || domain1 != domain2 {
			t.Errorf("not idempotent: %q -> (%q, %q) -> (%q, %q)",
				in, local1, domain1, local2, domain2)
		}
	}
}

// GH #1637: the directory host's local part is refused in every spelling
// Canonicalise folds onto it; other local parts pass.
func TestCheckNotReserved(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"jabali-directory@example.com", "Jabali-Directory@example.com", "jabali-directory+tag@example.com"} {
		local, _, err := Canonicalise(in)
		if err != nil {
			t.Fatalf("Canonicalise(%q): %v", in, err)
		}
		if err := CheckNotReserved(local); !errors.Is(err, ErrLocalReserved) {
			t.Errorf("CheckNotReserved(%q) = %v, want ErrLocalReserved", local, err)
		}
	}
	for _, local := range []string{"alice", "jabali", "directory", "jabali-directory2"} {
		if err := CheckNotReserved(local); err != nil {
			t.Errorf("CheckNotReserved(%q) = %v, want nil", local, err)
		}
	}
}

// postmaster@ on a tenant domain belongs to the server admin (ADR-0110); on the
// panel hostname's domain it is the admin's own mailbox.
func TestCheckNotReservedOn_PostmasterIsTheAdmins(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"postmaster@example.com", "PostMaster@example.com", "postmaster+reports@example.com"} {
		local, _, err := Canonicalise(in)
		if err != nil {
			t.Fatalf("Canonicalise(%q): %v", in, err)
		}
		err = CheckNotReservedOn(local, false)
		if !errors.Is(err, ErrPostmasterReserved) || !errors.Is(err, ErrLocalReserved) {
			t.Errorf("CheckNotReservedOn(%q, tenant domain) = %v, want ErrPostmasterReserved wrapping ErrLocalReserved", local, err)
		}
		if err := CheckNotReservedOn(local, true); err != nil {
			t.Errorf("CheckNotReservedOn(%q, panel domain) = %v, want nil", local, err)
		}
	}
	if err := CheckNotReservedOn(DirectoryLocalPart, true); !errors.Is(err, ErrLocalReserved) {
		t.Errorf("CheckNotReservedOn(directory, panel domain) = %v, want ErrLocalReserved", err)
	}
	for _, local := range []string{"alice", "postmasters", "post-master", "abuse"} {
		if err := CheckNotReservedOn(local, false); err != nil {
			t.Errorf("CheckNotReservedOn(%q, tenant domain) = %v, want nil", local, err)
		}
	}
	// Alias local parts are stored as typed, so the alias check ignores case
	// and surrounding space.
	for _, local := range []string{"postmaster", "Postmaster", " POSTMASTER "} {
		if err := CheckPostmasterOn(local, false); !errors.Is(err, ErrPostmasterReserved) {
			t.Errorf("CheckPostmasterOn(%q, tenant domain) = %v, want ErrPostmasterReserved", local, err)
		}
	}
}
