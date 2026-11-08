package mailaddr

import (
	"errors"
	"strings"
	"testing"
)

func TestCanonicaliseExternal_Accepts(t *testing.T) {
	cases := map[string]string{
		"Someone@Gmail.com":         "someone@gmail.com",
		"  someone@gmail.com  ":     "someone@gmail.com",
		"someone+lists@gmail.com":   "someone+lists@gmail.com", // the tag is part of the destination
		"first.last_x-y@mail.co.uk": "first.last_x-y@mail.co.uk",
		"user@bücher.example":       "user@xn--bcher-kva.example",
	}
	for in, want := range cases {
		got, err := CanonicaliseExternal(in)
		if err != nil {
			t.Errorf("CanonicaliseExternal(%q) error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("CanonicaliseExternal(%q) = %q, want %q", in, got, want)
		}
	}
}

// Everything the agent's requireEmail refuses must be refused here first, or a
// saved address would never reach the mail server and nobody would be told.
func TestCanonicaliseExternal_Rejects(t *testing.T) {
	cases := map[string]error{
		"":                                     ErrEmpty,
		"gmail.com":                            ErrNoAtSign,
		"a@b@gmail.com":                        ErrMultipleAtSigns,
		"@gmail.com":                           ErrLocalEmpty,
		"some one@gmail.com":                   ErrLocalShellMeta,
		`some"one@gmail.com`:                   ErrLocalShellMeta,
		`some\one@gmail.com`:                   ErrLocalShellMeta,
		"someone;rm@gmail.com":                 ErrLocalShellMeta,
		"o'brien@gmail.com":                    ErrLocalShellMeta,
		"${x}@gmail.com":                       ErrLocalShellMeta,
		"anyone!@gmail.com":                    ErrLocalShellMeta,
		"jösé@gmail.com":                       ErrLocalNonASCII,
		".someone@gmail.com":                   ErrLocalDots,
		"someone.@gmail.com":                   ErrLocalDots,
		"some..one@gmail.com":                  ErrLocalDots,
		"someone@localhost":                    ErrDomainSingleLabel,
		"someone@":                             ErrDomainEmpty,
		"someone@gmail com":                    ErrDomainShellMeta,
		`someone@gmail.com"`:                   ErrDomainShellMeta,
		"someone@[127.0.0.1]":                  ErrDomainShellMeta,
		strings.Repeat("a", 65) + "@gmail.com": ErrLocalTooLong,
	}
	for in, want := range cases {
		_, err := CanonicaliseExternal(in)
		if !errors.Is(err, want) {
			t.Errorf("CanonicaliseExternal(%q) error %v, want %v", in, err, want)
		}
	}
}

func TestCanonicaliseExternal_RejectsBadIDN(t *testing.T) {
	if _, err := CanonicaliseExternal("someone@-bad-.example"); !errors.Is(err, ErrIDNA) {
		t.Fatalf("want ErrIDNA, got %v", err)
	}
}
