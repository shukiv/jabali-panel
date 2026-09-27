package egressops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSeedState(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		missing       bool
		want          string
		ok            bool
	}{
		{name: "learning", content: "learning\n", want: "learning", ok: true},
		{name: "enforced", content: "enforced\n", want: "enforced", ok: true},
		{name: "missing", missing: true, want: "enforced", ok: false},
		{name: "off is not a seed state", content: "off", want: "enforced", ok: false},
		{name: "garbage", content: "yes", want: "enforced", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "absent")
			if !tc.missing {
				p = writeFile(t, tc.content)
			}
			got, ok := SeedState(p)
			if got != tc.want || ok != tc.ok {
				t.Errorf("SeedState = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// install.sh writes "learning" into the mode file on every host that
// predated M34. That must not hold the nightly flip: the pin is its own
// file. Before, the two were the same path and the soak never ended.
func TestFlipMature_ModeFileLearningIsNotAPin(t *testing.T) {
	if DefaultPinPath == DefaultModePath {
		t.Fatalf("pin and mode share %s: the installer's default would pin every upgraded host", DefaultPinPath)
	}
	mode := writeFile(t, "learning\n")
	pols := &fakePolicyRepo{mature: []models.UserEgressPolicy{{UserID: "U1", State: models.UserEgressStateLearning}}}

	// The pin path is where an operator would pin; nothing is there.
	pin := filepath.Join(filepath.Dir(mode), "per-user-egress.pin")
	res, err := FlipMature(context.Background(), pols, 7, pin, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pinned || len(res.Flipped) != 1 {
		t.Fatalf("pinned=%v flipped=%v, want the mature row flipped", res.Pinned, res.Flipped)
	}

	// An operator pin still holds.
	pols = &fakePolicyRepo{mature: []models.UserEgressPolicy{{UserID: "U1", State: models.UserEgressStateLearning}}}
	res, err = FlipMature(context.Background(), pols, 7, writeFile(t, "learning\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pinned || len(res.Flipped) != 0 {
		t.Fatalf("pinned=%v flipped=%v, want the pin to hold", res.Pinned, res.Flipped)
	}
}
