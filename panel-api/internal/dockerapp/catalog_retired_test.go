package dockerapp

import "testing"

// Bigcapital's MinIO images are gone, so it is retired: Listed (the install
// list) leaves it out while Get still finds it for the installs made before.
func TestCatalog_RetiredEntriesAreNotListed(t *testing.T) {
	cat, errs := LoadDir(repoCatalogDir(t))
	if len(errs) > 0 {
		t.Fatalf("catalog load errors: %v", errs)
	}
	if e, ok := cat.Get("bigcapital"); !ok || e.Retired == "" {
		t.Fatal("bigcapital must stay in the catalog, retired")
	}
	retired := 0
	for _, e := range cat.All() {
		if e.Retired != "" {
			retired++
		}
	}
	listed := cat.Listed()
	if len(listed) != cat.Len()-retired {
		t.Fatalf("Listed has %d entries, want %d (all %d less %d retired)", len(listed), cat.Len()-retired, cat.Len(), retired)
	}
	for _, e := range listed {
		if e.Retired != "" {
			t.Errorf("Listed holds retired %s", e.Slug)
		}
	}
}
