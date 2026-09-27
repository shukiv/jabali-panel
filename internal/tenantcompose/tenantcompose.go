// Package tenantcompose is the panel-api ↔ agent contract that pins a tenant
// Docker app's compose to what panel-api rendered (GH #1903).
//
// panel-api derives the expected service set from a render that carries no
// tenant input (no env values, no domain, no ports), so an injection into the
// real render cannot reach it. The agent resolves the real compose with
// `docker compose config` and requires every service to match: same names,
// same images, same limits.
package tenantcompose

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Services maps each compose service name to what it must resolve to.
type Services map[string]Service

// Service is the pinned part of one service. A zero value means "not set":
// the resolved service must not set it either.
type Service struct {
	Image        string            `json:"image"`
	CPUs         float64           `json:"cpus,omitempty"`
	MemoryBytes  int64             `json:"memory_bytes,omitempty"`
	PIDs         int64             `json:"pids,omitempty"`
	ShmSizeBytes int64             `json:"shm_size_bytes,omitempty"`
	Ulimits      map[string]Ulimit `json:"ulimits,omitempty"`
}

// Ulimit is one ulimit. A single-value ulimit in compose sets both halves.
type Ulimit struct {
	Soft int64 `json:"soft"`
	Hard int64 `json:"hard"`
}

// Check reports the first way got differs from want, or nil when they match.
// Services are compared in name order so the message is deterministic.
func (want Services) Check(got Services) error {
	for _, name := range sortedNames(got) {
		if _, ok := want[name]; !ok {
			return fmt.Errorf("service %q is not in the catalog template", name)
		}
	}
	for _, name := range sortedNames(want) {
		g, ok := got[name]
		if !ok {
			return fmt.Errorf("service %q from the catalog template is missing", name)
		}
		if err := want[name].check(g); err != nil {
			return fmt.Errorf("service %q %v", name, err)
		}
	}
	return nil
}

func (w Service) check(g Service) error {
	switch {
	case g.Image != w.Image:
		return fmt.Errorf("runs image %q, not the catalog image %q", g.Image, w.Image)
	case math.Abs(g.CPUs-w.CPUs) > 1e-6:
		return fmt.Errorf("has a cpus limit of %v, not the rendered %v", g.CPUs, w.CPUs)
	case g.MemoryBytes != w.MemoryBytes:
		return fmt.Errorf("has a memory limit of %d bytes, not the rendered %d", g.MemoryBytes, w.MemoryBytes)
	case g.PIDs != w.PIDs:
		return fmt.Errorf("has a pids limit of %d, not the rendered %d", g.PIDs, w.PIDs)
	case g.ShmSizeBytes != w.ShmSizeBytes:
		return fmt.Errorf("has a shm_size of %d bytes, not the rendered %d", g.ShmSizeBytes, w.ShmSizeBytes)
	}
	for _, n := range sortedUlimitNames(g.Ulimits, w.Ulimits) {
		gu, gok := g.Ulimits[n]
		wu, wok := w.Ulimits[n]
		if gok != wok || gu != wu {
			return fmt.Errorf("sets ulimit %s to %+v, not the rendered %+v", n, gu, wu)
		}
	}
	return nil
}

func sortedNames(s Services) []string {
	names := make([]string, 0, len(s))
	for n := range s {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func sortedUlimitNames(a, b map[string]Ulimit) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for n := range a {
		seen[n] = true
	}
	for n := range b {
		seen[n] = true
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ramSizeRE is go-units' size syntax: a decimal number, an optional space, an
// optional binary unit prefix, then an optional "i" and "b".
var ramSizeRE = regexp.MustCompile(`^(\d+(\.\d+)*) ?([kKmMgGtTpP])?[iI]?[bB]?$`)

var ramUnits = map[string]float64{
	"k": 1 << 10,
	"m": 1 << 20,
	"g": 1 << 30,
	"t": 1 << 40,
	"p": 1 << 50,
}

// RAMInBytes parses a compose byte size ("512m", "1gb", "1.5g", "1073741824")
// the way docker compose does for memory and shm_size (go-units RAMInBytes:
// binary units, case-insensitive, optional "i" and "b").
func RAMInBytes(s string) (int64, error) {
	m := ramSizeRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	size, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	if mul, ok := ramUnits[strings.ToLower(m[3])]; ok {
		size *= mul
	}
	return int64(size), nil
}
