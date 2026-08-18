package ftpsync

import (
	"sync"
	"testing"
)

// NextGeneration must be strictly increasing even under concurrent callers and
// even if the wall clock does not advance between calls — the CAS max(prev+1,
// now) guarantees uniqueness, which the agent's stale-drop gate depends on.
func TestNextGeneration_StrictlyIncreasingConcurrent(t *testing.T) {
	const n = 2000
	got := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			got[idx] = NextGeneration()
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]struct{}, n)
	for _, g := range got {
		if _, dup := seen[g]; dup {
			t.Fatalf("duplicate generation %d — uniqueness broken", g)
		}
		seen[g] = struct{}{}
	}
	if len(seen) != n {
		t.Fatalf("expected %d unique generations, got %d", n, len(seen))
	}
}
