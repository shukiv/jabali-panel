package api

import "testing"

// withDemoRedaction flips the process-global flag for one test and restores it,
// so a leaked "on" state can't poison other tests in the package.
func withDemoRedaction(t *testing.T, on bool) {
	t.Helper()
	prev := DemoRedactionEnabled()
	SetDemoRedaction(on)
	t.Cleanup(func() { SetDemoRedaction(prev) })
}

func TestDemoRedactionMaskIP(t *testing.T) {
	t.Run("off is a pass-through", func(t *testing.T) {
		withDemoRedaction(t, false)
		if got := demoMaskIP("83.151.202.27"); got != "83.151.202.27" {
			t.Errorf("off: got %q, want the real IP", got)
		}
	})
	t.Run("on masks by family", func(t *testing.T) {
		withDemoRedaction(t, true)
		if got := demoMaskIP("83.151.202.27"); got != demoPlaceholderIPv4 {
			t.Errorf("v4: got %q, want %q", got, demoPlaceholderIPv4)
		}
		if got := demoMaskIP("2601:6c1:4000::1"); got != demoPlaceholderIPv6 {
			t.Errorf("v6: got %q, want %q", got, demoPlaceholderIPv6)
		}
		if got := demoMaskIP(""); got != "" {
			t.Errorf("empty stays empty, got %q", got)
		}
		if got := demoMaskIP("not-an-ip"); got != demoPlaceholderIPv4 {
			t.Errorf("unparseable fails closed to v4 placeholder, got %q", got)
		}
	})
}

func TestDemoRedact(t *testing.T) {
	withDemoRedaction(t, true)
	if got := demoRedact("Mozilla/5.0 …"); got != demoRedactedText {
		t.Errorf("on: got %q, want %q", got, demoRedactedText)
	}
	if got := demoRedact(""); got != "" {
		t.Errorf("empty stays empty, got %q", got)
	}
	withDemoRedaction(t, false)
	if got := demoRedact("Mozilla/5.0 …"); got != "Mozilla/5.0 …" {
		t.Errorf("off is a pass-through, got %q", got)
	}
}
