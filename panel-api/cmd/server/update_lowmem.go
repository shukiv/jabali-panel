package main

import (
	"fmt"
	"os"
	"strings"
)

// update_lowmem.go — GH #760. The from-source update path (`jabali update`
// falling back to a local go build when no release tarball is available or
// --from-source is set) runs the same `go build ./panel-api/...` that OOM-kills
// a 2 GB VPS under Go's default build parallelism (=nproc). Mirror
// install.sh:build_backend: on a low-RAM host, serialize compilation and
// soft-cap the Go heap so the build survives (slower, not killed).

// lowRAMGoBuildEnvForMB returns the extra env vars to prepend to an on-box
// `go build` on a low-RAM host. -p=1 is the big lever (one compile action at a
// time); GOMEMLIMIT + a lower GOGC make each go tool's GC more aggressive.
// Empty on hosts >4 GB (and on unknown RAM) so CI and real hosts build exactly
// as before. The brackets match install.sh:build_backend.
func lowRAMGoBuildEnvForMB(memMB int) []string {
	if memMB <= 0 || memMB > 4096 {
		return nil
	}
	if memMB <= 2560 {
		return []string{"GOFLAGS=-p=1", "GOMEMLIMIT=900MiB", "GOGC=40"}
	}
	return []string{"GOFLAGS=-p=1", "GOMEMLIMIT=1500MiB", "GOGC=50"}
}

// hostMemMB reads MemTotal from /proc/meminfo in MB; 0 on any error (which
// lowRAMGoBuildEnvForMB treats as "don't tune").
func hostMemMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			var kb int
			if _, err := fmt.Sscanf(line, "MemTotal: %d kB", &kb); err == nil && kb > 0 {
				return kb / 1024
			}
			return 0
		}
	}
	return 0
}

// lowRAMGoBuildEnv is the /proc-backed wrapper used by the updater.
func lowRAMGoBuildEnv() []string { return lowRAMGoBuildEnvForMB(hostMemMB()) }

// isLikelyOOM reports whether a build error looks like the process was killed
// for memory — the common low-RAM `jabali update` failure, where the go/vite
// build gets OOM-killed. Used to add a "free RAM / add swap" hint to the
// failure message (GH #760 follow-up) so the operator knows why the binary
// didn't update.
func isLikelyOOM(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "signal: killed") ||
		strings.Contains(s, "cannot allocate memory") ||
		strings.Contains(s, "out of memory") ||
		strings.Contains(s, "Killed")
}

// shortSHA7 truncates a git SHA to 7 chars for display; passes through anything
// shorter.
func shortSHA7(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// serializeGoBuildsForMB reports whether the updater must build its Go binaries
// one at a time instead of concurrently.
//
// The updater builds THREE binaries in parallel — panel-api, panel-agent and
// jabali-ssh-shell. lowRAMGoBuildEnvForMB caps each one at GOMEMLIMIT=900MiB on
// a small box, but that is a per-process ceiling: three of them at once is a
// 2700MiB ceiling on a host with 1973MB of RAM, and GOFLAGS=-p=1 does not help
// because it limits parallelism WITHIN a build, not across builds.
//
// Observed on a 2 GB / 2-core box, where `jabali update` died with
//
//	go build ... ./panel-api/cmd/server: exit status 1
//
// and the identical build run on its own immediately afterwards succeeded.
// Serial builds cost wall-clock (~30s → ~60s) and buy an update that finishes,
// which is the better trade on exactly the hosts that cannot afford to fail.
//
// Uses the same 2560MB threshold as the tight GOMEMLIMIT tier: if a host needs
// the aggressive per-process cap, it cannot afford three of them at once.
func serializeGoBuildsForMB(memMB int) bool {
	return memMB > 0 && memMB <= 2560
}

// serializeGoBuilds is the /proc-backed wrapper used by the updater.
func serializeGoBuilds() bool { return serializeGoBuildsForMB(hostMemMB()) }
