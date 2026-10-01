package main

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

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

// `domain php-settings set` changes only the directives whose flags are
// passed. UpdatePHPSettings writes every directive, so the command must start
// from the stored values: building the update from scratch cleared every other
// override, including an admin's open_basedir (GH #1701 Slice 3).
func TestCLIPHPSettingsSetKeepsUnpassedDirectives(t *testing.T) {
	stored, tz, off := "{DOCROOT}:{TMP}", "Asia/Jerusalem", false
	mem := "256M"
	dom := &models.Domain{
		PHPMemoryLimit:   &mem,
		PHPTimezone:      &tz,
		PHPOpenBasedir:   &stored,
		PHPAllowURLFopen: &off,
	}
	cmd := newDomainPHPSettingsSetCmd()
	if err := cmd.Flags().Set("max-input-vars", "5000"); err != nil {
		t.Fatal(err)
	}
	s, err := cliPHPSettingsFromFlags(cmd.Flags(), dom)
	if err != nil {
		t.Fatalf("cliPHPSettingsFromFlags: %v", err)
	}
	if s.MaxInputVars == nil || *s.MaxInputVars != 5000 {
		t.Fatalf("max_input_vars = %v, want 5000", s.MaxInputVars)
	}
	if s.MemoryLimit == nil || *s.MemoryLimit != mem || s.Timezone == nil || *s.Timezone != tz {
		t.Fatalf("memory_limit / date.timezone = %v / %v, want the stored values kept", s.MemoryLimit, s.Timezone)
	}
	if s.OpenBasedir == nil || *s.OpenBasedir != stored || s.AllowURLFopen == nil || *s.AllowURLFopen {
		t.Fatalf("open_basedir / allow_url_fopen = %v / %v, want the stored values kept", s.OpenBasedir, s.AllowURLFopen)
	}

	if _, err := cliPHPSettingsFromFlags(newDomainPHPSettingsSetCmd().Flags(), dom); err == nil {
		t.Fatal("no flags passed: want an error")
	}
	bad := newDomainPHPSettingsSetCmd()
	_ = bad.Flags().Set("memory-limit", "256MB")
	if _, err := cliPHPSettingsFromFlags(bad.Flags(), dom); err == nil {
		t.Fatal("an invalid size: want an error")
	}
}
