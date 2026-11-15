package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// GH #2501: EuroOffice's log volume holds its logs one directory down
// (docservice/out.log, converter/out.log, ...), so the scan must report
// HasLogs for those too — otherwise the reconciler never warns about an
// unmanaged, growing log dir. The handler's DataRoot is a const, so the
// per-subdirectory check is a helper taking the directory.
func TestDockerAppSubdirHasLogs(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"nested log only", []string{"docservice/out.log"}, true},
		{"top-level log", []string{"out.log"}, true},
		{"both depths", []string{"out.log", "converter/out.log"}, true},
		{"no logs at any depth", []string{"data.bin", "docservice/state.json"}, false},
		{"deeper than one level", []string{"a/b/out.log"}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := t.TempDir()
			for _, f := range tc.files {
				p := filepath.Join(sub, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatalf("write %s: %v", f, err)
				}
			}
			if got := dockerAppSubdirHasLogs(sub); got != tc.want {
				t.Errorf("dockerAppSubdirHasLogs with files %v = %v, want %v", tc.files, got, tc.want)
			}
		})
	}
}
