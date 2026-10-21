package dockerapp

import (
	"strings"
	"testing"
)

// A generated secret that starts with "-" is read as an option by a
// container that passes it on a command line: owncloud's entrypoint runs
// `occ maintenance:install --admin-pass "$PW"`, which fails with
// `The "-j" option does not exist` for such a password. base64url gives one
// in 64 secrets that first character, so the generator must draw again.
func TestGenerateSecret_NeverStartsWithDash(t *testing.T) {
	prev := secretRandRead
	t.Cleanup(func() { secretRandRead = prev })
	calls := 0
	secretRandRead = func(b []byte) (int, error) {
		calls++
		for i := range b {
			b[i] = 0
		}
		if calls == 1 {
			b[0] = 0xF8 // first 6 bits 62 = "-" in base64url
		}
		return len(b), nil
	}
	for _, scheme := range []string{"password32", "password64"} {
		calls = 0
		s, err := generateSecret(scheme)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(s, "-") {
			t.Fatalf("%s: secret %q starts with '-'", scheme, s)
		}
		if want := map[string]int{"password32": 32, "password64": 64}[scheme]; len(s) != want {
			t.Fatalf("%s: secret is %d characters, want %d", scheme, len(s), want)
		}
	}
}
