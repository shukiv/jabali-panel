package commands

import (
	"reflect"
	"testing"
)

// JAB-93: path lists for the same (uid, host) group are merged + de-duplicated.
func TestMergeGroupPaths_MergeAndDedup(t *testing.T) {
	grp := []*wpPurgeItem{
		{paths: []string{"/a", "/b"}},
		{paths: []string{"/b", "/c"}}, // /b duplicated across files
		{paths: []string{"/a"}},       // /a duplicated
	}
	got, whole := mergeGroupPaths(grp)
	if whole {
		t.Fatal("no empty-paths request → must not be whole-domain")
	}
	want := []string{"/a", "/b", "/c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged=%v, want %v", got, want)
	}
}

// JAB-93: a whole-domain purge (empty paths) anywhere in the group collapses the
// whole group to one whole-domain purge.
func TestMergeGroupPaths_WholeDomainWins(t *testing.T) {
	grp := []*wpPurgeItem{
		{paths: []string{"/a", "/b"}},
		{paths: []string{}}, // whole-domain purge
		{paths: []string{"/c"}},
	}
	got, whole := mergeGroupPaths(grp)
	if !whole {
		t.Fatal("an empty-paths request must collapse the group to whole-domain")
	}
	if len(got) != 0 {
		t.Fatalf("whole-domain purge must carry no paths, got %v", got)
	}
}

// JAB-93: the merged list is capped at wpPurgeMaxPaths.
func TestMergeGroupPaths_CapsPaths(t *testing.T) {
	paths := make([]string, wpPurgeMaxPaths+50)
	for i := range paths {
		paths[i] = "/p" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	got, whole := mergeGroupPaths([]*wpPurgeItem{{paths: paths}})
	if whole {
		t.Fatal("non-empty paths → not whole-domain")
	}
	if len(got) != wpPurgeMaxPaths {
		t.Fatalf("merged len=%d, want cap %d", len(got), wpPurgeMaxPaths)
	}
}
