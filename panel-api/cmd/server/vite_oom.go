package main

import "strings"

// Heap ceilings for the vite build, as a percentage of MemTotal.
//
// The default deliberately stays low: V8 sizes its heap from /proc/meminfo and
// will happily grow past what a small VM can back, at which point the kernel
// OOM-killer takes the process (exit 137) mid-bundle. Capping at half of RAM
// forces harder GC and keeps node inside a budget the host can honour.
//
// The retry ceiling is what the default cannot be. Raising the steady-state cap
// to 90% would reintroduce the very OOM-kill the cap exists to prevent on a
// swapless host; using it only after a heap abort — and only once swap has been
// ensured — keeps the safe path safe and gives the build room exactly when the
// evidence says the ceiling, not the machine, was the limit.
const (
	viteHeapPctDefault = 50
	viteHeapPctRetry   = 90
)

// isHeapAbort reports whether the build died because V8 hit
// --max-old-space-size and aborted itself.
//
// Node raises SIGABRT for this, which surfaces as exit status 134, after
// printing "FATAL ERROR: ... JavaScript heap out of memory". It is a different
// failure from being OOM-killed and wants the opposite remedy: the machine had
// memory, the ceiling we set was too low.
//
// Matching the message as well as the exit code because a build wrapper that
// re-raises the failure can lose the signal number while keeping stderr.
func isHeapAbort(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "exit status 134") ||
		strings.Contains(msg, "javascript heap out of memory") ||
		strings.Contains(msg, "heap out of memory")
}

// isOOMKilled reports whether the kernel killed the build — genuinely not
// enough memory, which swap can fix.
func isOOMKilled(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "exit status 137") || strings.Contains(msg, "killed")
}
