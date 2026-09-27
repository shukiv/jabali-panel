package tenantcompose

import (
	"strings"
	"testing"
)

// The expected byte counts are what `docker compose config --format json`
// (Compose v5.2.0) printed for these inputs on a test host.
func TestRAMInBytes_MatchesCompose(t *testing.T) {
	for in, want := range map[string]int64{
		"512m":       536870912,
		"1024m":      1073741824,
		"300m":       314572800,
		"1gb":        1073741824,
		"1g":         1073741824,
		"1.5g":       1610612736,
		"2GiB":       2147483648,
		"64k":        65536,
		"1073741824": 1073741824,
	} {
		got, err := RAMInBytes(in)
		if err != nil || got != want {
			t.Errorf("RAMInBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "m", "-1g", "1x", "1 g b"} {
		if _, err := RAMInBytes(bad); err == nil {
			t.Errorf("RAMInBytes(%q) accepted an invalid size", bad)
		}
	}
}

func pinned() Services {
	return Services{
		"app": {Image: "example/app:1@sha256:aa", CPUs: 0.5, MemoryBytes: 536870912, PIDs: 256},
		"db": {Image: "postgres:16", PIDs: 256, ShmSizeBytes: 1073741824,
			Ulimits: map[string]Ulimit{"nofile": {Soft: 1024, Hard: 2048}}},
	}
}

func TestCheck_Matches(t *testing.T) {
	if err := pinned().Check(pinned()); err != nil {
		t.Fatalf("identical sets must match: %v", err)
	}
}

func TestCheck_RejectsEveryDrift(t *testing.T) {
	cases := map[string]struct {
		mutate func(Services)
		want   string
	}{
		"extra service":   {func(s Services) { s["miner"] = Service{Image: "evil"} }, `"miner" is not in the catalog template`},
		"missing service": {func(s Services) { delete(s, "db") }, `"db" from the catalog template is missing`},
		"image swapped":   {func(s Services) { a := s["app"]; a.Image = "evil"; s["app"] = a }, "image"},
		"cpus raised":     {func(s Services) { a := s["app"]; a.CPUs = 4; s["app"] = a }, "cpus"},
		"cpus dropped":    {func(s Services) { a := s["app"]; a.CPUs = 0; s["app"] = a }, "cpus"},
		"memory raised":   {func(s Services) { a := s["app"]; a.MemoryBytes *= 8; s["app"] = a }, "memory"},
		"pids removed":    {func(s Services) { a := s["db"]; a.PIDs = 0; s["db"] = a }, "pids"},
		"shm raised":      {func(s Services) { a := s["db"]; a.ShmSizeBytes *= 4; s["db"] = a }, "shm_size"},
		"ulimit raised": {func(s Services) {
			a := s["db"]
			a.Ulimits = map[string]Ulimit{"nofile": {Soft: 1024, Hard: 1 << 20}}
			s["db"] = a
		}, "ulimit nofile"},
		"ulimit added": {func(s Services) {
			a := s["app"]
			a.Ulimits = map[string]Ulimit{"memlock": {Soft: -1, Hard: -1}}
			s["app"] = a
		}, "ulimit memlock"},
		"ulimit removed": {func(s Services) { a := s["db"]; a.Ulimits = nil; s["db"] = a }, "ulimit nofile"},
	}
	for name, tc := range cases {
		got := pinned()
		tc.mutate(got)
		err := pinned().Check(got)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
