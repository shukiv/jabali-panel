package limits

import "testing"

func u32(v uint32) *uint32 { return &v }

// ValidateOverrideBounds is the single bounds check both the REST handler and
// the operator CLI call (JAB-309): a present value over its ceiling is rejected;
// nil fields and in-range values pass.
func TestValidateOverrideBounds(t *testing.T) {
	// All nil → valid (a no-op override).
	if err := ValidateOverrideBounds(nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("all-nil override must be valid, got %v", err)
	}
	// In-range values → valid.
	if err := ValidateOverrideBounds(u32(200), u32(2048), u32(500), u32(500), u32(1000)); err != nil {
		t.Fatalf("in-range override must be valid, got %v", err)
	}

	cases := []struct {
		name                             string
		cpu, mem, ioRead, ioWrite, tasks *uint32
	}{
		{"cpu over", u32(MaxCPUQuotaPercent + 1), nil, nil, nil, nil},
		{"mem over", nil, u32(MaxMemoryLimitMB + 1), nil, nil, nil},
		{"io-read over", nil, nil, u32(MaxIOMbps + 1), nil, nil},
		{"io-write over", nil, nil, nil, u32(MaxIOMbps + 1), nil},
		{"tasks over", nil, nil, nil, nil, u32(MaxTasks + 1)},
		{"one bad among good", u32(100), u32(2048), u32(MaxIOMbps + 1), u32(100), u32(50)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateOverrideBounds(tc.cpu, tc.mem, tc.ioRead, tc.ioWrite, tc.tasks); err == nil {
				t.Errorf("%s: an out-of-range override must be rejected", tc.name)
			}
		})
	}
}
