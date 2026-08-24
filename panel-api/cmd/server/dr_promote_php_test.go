package main

import (
	"reflect"
	"testing"
)

func TestMissingPHPVersions(t *testing.T) {
	tests := []struct {
		name      string
		required  []string
		installed []string
		want      []string
	}{
		{"all present", []string{"8.3", "8.4"}, []string{"8.3", "8.4", "8.5"}, nil},
		{"one missing", []string{"8.3", "8.5"}, []string{"8.3", "8.4"}, []string{"8.5"}},
		{"dedup + sort", []string{"8.5", "8.3", "8.5"}, []string{"8.4"}, []string{"8.3", "8.5"}},
		{"empty version skipped", []string{"", "8.5"}, []string{}, []string{"8.5"}},
		{"nothing required", nil, []string{"8.4"}, nil},
		{"nothing installed", []string{"8.4"}, nil, []string{"8.4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := missingPHPVersions(tt.required, tt.installed)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missingPHPVersions(%v,%v) = %v, want %v", tt.required, tt.installed, got, tt.want)
			}
		})
	}
}
