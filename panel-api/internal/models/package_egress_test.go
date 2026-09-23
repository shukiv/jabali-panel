package models

import (
	"reflect"
	"testing"
)

// GH #1798: the CIDR-scope helpers for the per-package outbound-SSH allowance.

func TestNormalizeEgressSSHOutCIDRs(t *testing.T) {
	t.Run("empty stays empty (means anywhere at apply time)", func(t *testing.T) {
		got, err := NormalizeEgressSSHOutCIDRs("   ")
		if err != nil || got != "" {
			t.Fatalf("got %q, err %v; want \"\", nil", got, err)
		}
	})
	t.Run("valid array is canonicalised", func(t *testing.T) {
		got, err := NormalizeEgressSSHOutCIDRs(`[" 140.82.112.0/20 ","2606:50c0::/32"]`)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got != `["140.82.112.0/20","2606:50c0::/32"]` {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("malformed CIDR is rejected", func(t *testing.T) {
		if _, err := NormalizeEgressSSHOutCIDRs(`["not-a-cidr"]`); err == nil {
			t.Fatal("expected error for invalid CIDR, got nil")
		}
	})
	t.Run("a bare host without a mask is rejected", func(t *testing.T) {
		// net.ParseCIDR requires a mask — "1.2.3.4" (no /32) must fail so a
		// malformed value never reaches the agent's nft renderer.
		if _, err := NormalizeEgressSSHOutCIDRs(`["1.2.3.4"]`); err == nil {
			t.Fatal("expected error for bare host, got nil")
		}
	})
	t.Run("non-array JSON is rejected", func(t *testing.T) {
		if _, err := NormalizeEgressSSHOutCIDRs(`{"cidr":"0.0.0.0/0"}`); err == nil {
			t.Fatal("expected error for non-array JSON, got nil")
		}
	})
}

func TestParseEgressSSHOutCIDRs(t *testing.T) {
	t.Run("empty falls back to anywhere v4+v6", func(t *testing.T) {
		got, err := ParseEgressSSHOutCIDRs("")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !reflect.DeepEqual(got, []string{"0.0.0.0/0", "::/0"}) {
			t.Fatalf("got %v, want the anywhere fallback", got)
		}
	})
	t.Run("empty array also falls back to anywhere", func(t *testing.T) {
		got, err := ParseEgressSSHOutCIDRs("[]")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !reflect.DeepEqual(got, []string{"0.0.0.0/0", "::/0"}) {
			t.Fatalf("got %v, want the anywhere fallback", got)
		}
	})
	t.Run("explicit scope is returned verbatim", func(t *testing.T) {
		got, err := ParseEgressSSHOutCIDRs(`["140.82.112.0/20"]`)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !reflect.DeepEqual(got, []string{"140.82.112.0/20"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("corrupt JSON returns an error (caller fails closed)", func(t *testing.T) {
		if _, err := ParseEgressSSHOutCIDRs(`["a",`); err == nil {
			t.Fatal("expected error for corrupt JSON, got nil")
		}
	})
	t.Run("a malformed CIDR that skipped write-time validation fails closed", func(t *testing.T) {
		// A value written by a path that bypasses NormalizeEgressSSHOutCIDRs
		// (backup restore / direct DB edit) must not reach the nft renderer.
		if _, err := ParseEgressSSHOutCIDRs(`["0.0.0.0/0","garbage"]`); err == nil {
			t.Fatal("expected error for a malformed stored CIDR, got nil")
		}
	})
}
