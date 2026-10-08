package pdns

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Every test binary that links this package uses the same pdnsutil stub
// directory, so test runs don't leave a new temporary directory each.
func TestPdnsutilTestStub_LivesInTheUsersStubDirectory(t *testing.T) {
	want := filepath.Join(os.TempDir(), fmt.Sprintf("jabali-pdnsutil-stub-%d", os.Getuid()))
	if got := filepath.Dir(pdnsutilTestStub()); got != want {
		t.Errorf("pdnsutil stub is in %s, want %s", got, want)
	}
}
