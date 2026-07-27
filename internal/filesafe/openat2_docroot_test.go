package filesafe

import (
	"os"
	"testing"
)

// TestOpenDirInScope_AllowsDocroot guards GH #657.
//
// "Folder sizes" / du / list at the user's home ROOT must work. OpenInScope
// refuses the docroot itself (rel == ".", "refusing to open docroot itself as a
// file") — a guard meant for file opens. files.du used OpenInScope by mistake,
// so a du at the home root (what the "Folder sizes" button hits by default)
// 502'd instantly. OpenDirInScope MUST open the docroot; that is what
// ReadDirInScope and the fixed files.du rely on.
func TestOpenDirInScope_AllowsDocroot(t *testing.T) {
	scope, docroot := funcScope(t)

	// OpenDirInScope on the docroot itself must SUCCEED.
	df, err := scope.OpenDirInScope(docroot)
	if err != nil {
		t.Fatalf("OpenDirInScope(docroot) = %v, want success (GH #657 regression)", err)
	}
	_ = df.Close()

	// OpenInScope must STILL refuse the docroot — the file-open guard whose
	// misuse caused the bug. If this ever starts succeeding, the
	// OpenInScope-vs-OpenDirInScope distinction (and this fix's rationale) is
	// gone and files.du could regress back to the wrong method silently.
	if _, err := scope.OpenInScope(docroot, os.O_RDONLY, 0); err == nil {
		t.Error("OpenInScope(docroot) unexpectedly succeeded — docroot file-open guard is gone")
	}
}
