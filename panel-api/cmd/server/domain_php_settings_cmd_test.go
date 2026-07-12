package main

import "testing"

func TestCLIValidatePHPSize(t *testing.T) {
	ok := []string{"256M", "64m", "128K", "1G", "512", "99999999"}
	bad := []string{"", "256MB", "-1", "M", "256 M", "999999999", "12.5M"}
	for _, v := range ok {
		if err := cliValidatePHPSize("memory-limit", v); err != nil {
			t.Errorf("size %q should be valid: %v", v, err)
		}
	}
	for _, v := range bad {
		if err := cliValidatePHPSize("memory-limit", v); err == nil {
			t.Errorf("size %q should be invalid", v)
		}
	}
}

func TestCLIValidatePHPInt(t *testing.T) {
	for _, v := range []int{1, 1000, 86400} {
		if err := cliValidatePHPInt("max-input-vars", v); err != nil {
			t.Errorf("int %d should be valid: %v", v, err)
		}
	}
	for _, v := range []int{0, -5, 86401, 1000000} {
		if err := cliValidatePHPInt("max-input-vars", v); err == nil {
			t.Errorf("int %d should be invalid", v)
		}
	}
}
