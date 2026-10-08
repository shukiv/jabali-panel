package certbot

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Every test binary that links this package uses the same certbot stub
// directory, so test runs don't leave a new temporary directory each.
func TestCertbotTestStub_LivesInTheUsersStubDirectory(t *testing.T) {
	want := filepath.Join(os.TempDir(), fmt.Sprintf("jabali-certbot-stub-%d", os.Getuid()))
	if got := filepath.Dir(certbotTestStub()); got != want {
		t.Errorf("certbot stub is in %s, want %s", got, want)
	}
}
