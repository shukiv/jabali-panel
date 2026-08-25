// Package limits contains the cross-boundary logic for M18 per-user
// resource limits — the effective-limits resolver, filesystem detection,
// and quota mount-point discovery.
//
// Everything here is pure / stdlib-only so it can be imported by both
// panel-api (for API surface + reconciler) and panel-agent (for
// defense-in-depth validation before writing drop-ins).
package limits

// EffectiveLimits is the fully-resolved limit bundle for one user:
// package values with any override applied. Every field is a flat
// uint32; zero means "unlimited for this resource" — the agent emits
// no systemd directive and makes no setquota call for a zero field.
//
// Consumers MUST NOT store nil or pointer semantics — the override
// cascade happens exclusively in Resolve, not downstream.
type EffectiveLimits struct {
	DiskQuotaMB     uint32
	CPUQuotaPercent uint32
	MemoryLimitMB   uint32
	IOReadMbps      uint32
	IOWriteMbps     uint32
	MaxTasks        uint32
}

// PackageLimits is the subset of hosting_packages fields the resolver
// cares about. Defined as its own struct (not a models import) so the
// package stays import-clean — callers construct this from whatever
// their local type is.
type PackageLimits struct {
	DiskQuotaMB     uint32
	CPUQuotaPercent uint32
	MemoryLimitMB   uint32
	IOReadMbps      uint32
	IOWriteMbps     uint32
	MaxTasks        uint32
}

// OverrideLimits is the per-user override row — every field is a
// pointer so we can distinguish NULL (inherit from package) from
// 0 (override to unlimited) from N (override to exactly N).
//
// Passing nil for the whole struct is legal and means "no override"
// (equivalent to every field being nil).
type OverrideLimits struct {
	DiskQuotaMB     *uint32
	CPUQuotaPercent *uint32
	MemoryLimitMB   *uint32
	IOReadMbps      *uint32
	IOWriteMbps     *uint32
	MaxTasks        *uint32
}

// Resolve computes the effective limits for one user.
//
// The semantics, per ADR-0032 §7:
//
//   override.X == nil  →  use pkg.X
//   override.X == &0   →  0 (unlimited, override beats any package value)
//   override.X == &N   →  N
//
// Resolve never returns an error: every combination is valid input,
// including a nil package pointer (treated as all-zero package).
// This matches how GORM hydrates an unfound HostingPackage row.
func Resolve(pkg *PackageLimits, override *OverrideLimits) EffectiveLimits {
	var base PackageLimits
	if pkg != nil {
		base = *pkg
	}
	return EffectiveLimits{
		DiskQuotaMB:     pick(base.DiskQuotaMB, overrideField(override, func(o *OverrideLimits) *uint32 { return o.DiskQuotaMB })),
		CPUQuotaPercent: pick(base.CPUQuotaPercent, overrideField(override, func(o *OverrideLimits) *uint32 { return o.CPUQuotaPercent })),
		MemoryLimitMB:   pick(base.MemoryLimitMB, overrideField(override, func(o *OverrideLimits) *uint32 { return o.MemoryLimitMB })),
		IOReadMbps:      pick(base.IOReadMbps, overrideField(override, func(o *OverrideLimits) *uint32 { return o.IOReadMbps })),
		IOWriteMbps:     pick(base.IOWriteMbps, overrideField(override, func(o *OverrideLimits) *uint32 { return o.IOWriteMbps })),
		MaxTasks:        pick(base.MaxTasks, overrideField(override, func(o *OverrideLimits) *uint32 { return o.MaxTasks })),
	}
}

// pick returns ov when non-nil (inclusive of 0), else the package value.
// Keeps the resolver's per-field logic a one-liner and makes the zero-vs-nil
// distinction explicit at every call site.
func pick(pkgValue uint32, ov *uint32) uint32 {
	if ov != nil {
		return *ov
	}
	return pkgValue
}

// overrideField safely dereferences the override struct for a single
// field — returns nil if the struct itself is nil, otherwise returns
// the pointer for the requested field (which may itself be nil).
func overrideField(o *OverrideLimits, f func(*OverrideLimits) *uint32) *uint32 {
	if o == nil {
		return nil
	}
	return f(o)
}

// MemoryHighMB returns the soft-limit value (MemoryHigh=) derived from
// a MemoryMax value. Fixed at 90% per ADR-0032 §8. Returns 0 when the
// input is 0 (unlimited → no high limit either).
func MemoryHighMB(memoryMaxMB uint32) uint32 {
	if memoryMaxMB == 0 {
		return 0
	}
	// Integer math: 90% floor, never overshoots MemoryMax.
	return memoryMaxMB * 9 / 10
}

// Bounds enforced at both API and agent layer — sane caps that still
// allow huge machines (100 cores, 1 TB RAM). Values above these likely
// indicate a units bug or a rogue admin.
const (
	MaxCPUQuotaPercent uint32 = 10000
	MaxMemoryLimitMB   uint32 = 1048576 // 1 TB
	MaxIOMbps          uint32 = 10000   // 10 GB/s, well above any NVMe budget
	MaxTasks           uint32 = 100000
	// DiskQuotaMB isn't bounded here; if an admin writes 1 EB we trust
	// they meant it, and setquota will fail at the filesystem layer
	// anyway on any real hardware.
)

// Validate rejects out-of-range values. Non-zero values above the cap
// fail; zero always passes (unlimited is always legal). Returns the
// first violation or nil.
// ValidateOverrideBounds runs each present (non-nil) per-user override value
// through the same bounds check EffectiveLimits.Validate applies, treating each
// as a field-of-one so any combination of NULLs is allowed. Shared by the REST
// handler and the operator CLI so a CLI-set override cannot bypass the bounds
// (JAB-309). DiskQuotaMB has no upper bound and is not checked, matching Validate.
func ValidateOverrideBounds(cpu, mem, ioRead, ioWrite, maxTasks *uint32) error {
	e := EffectiveLimits{}
	if cpu != nil {
		e.CPUQuotaPercent = *cpu
	}
	if mem != nil {
		e.MemoryLimitMB = *mem
	}
	if ioRead != nil {
		e.IOReadMbps = *ioRead
	}
	if ioWrite != nil {
		e.IOWriteMbps = *ioWrite
	}
	if maxTasks != nil {
		e.MaxTasks = *maxTasks
	}
	return e.Validate()
}

func (e EffectiveLimits) Validate() error {
	if e.CPUQuotaPercent > MaxCPUQuotaPercent {
		return &BoundError{Field: "cpu_quota_percent", Value: e.CPUQuotaPercent, Max: MaxCPUQuotaPercent}
	}
	if e.MemoryLimitMB > MaxMemoryLimitMB {
		return &BoundError{Field: "memory_limit_mb", Value: e.MemoryLimitMB, Max: MaxMemoryLimitMB}
	}
	if e.IOReadMbps > MaxIOMbps {
		return &BoundError{Field: "io_read_mbps", Value: e.IOReadMbps, Max: MaxIOMbps}
	}
	if e.IOWriteMbps > MaxIOMbps {
		return &BoundError{Field: "io_write_mbps", Value: e.IOWriteMbps, Max: MaxIOMbps}
	}
	if e.MaxTasks > MaxTasks {
		return &BoundError{Field: "max_tasks", Value: e.MaxTasks, Max: MaxTasks}
	}
	return nil
}

// BoundError is returned by Validate when a field exceeds its cap.
// Keeps the validator diagnostic without pulling in a heavyweight
// error package.
type BoundError struct {
	Field string
	Value uint32
	Max   uint32
}

func (b *BoundError) Error() string {
	return "limits: " + b.Field + " exceeds maximum"
}
