package main

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestSrcSSHPort(t *testing.T) {
	if srcSSHPort(nil) != 22 {
		t.Error("nil job → 22")
	}
	cases := map[int]int{0: 22, -5: 22, 70000: 22, 22: 22, 2222: 2222}
	for in, want := range cases {
		if got := srcSSHPort(&models.MigrationJob{SourcePort: in}); got != want {
			t.Errorf("srcSSHPort(port=%d) = %d, want %d", in, got, want)
		}
	}
}
