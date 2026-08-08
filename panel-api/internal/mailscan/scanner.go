package mailscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ScanResult is the verdict for a single attachment. RuleName is empty
// on a clean scan; populated with the matching YARA rule name on a hit.
// EngineErr is set when the scanner subprocess exited non-zero for a
// reason other than a hit (e.g. yr binary missing, timeout, parse fail).
type ScanResult struct {
	RuleName  string
	EngineErr error
}

// yrPath returns the absolute path to the yara-x CLI. Pinned via env so
// tests can swap a fake. We deliberately do NOT fall back to legacy
// `yara`: it has known parser divergence vs yara-x on the rfxn pack
// (no hash module). If yr is missing, the scanner reports EngineErr —
// the orchestrator records a DLQ row + emits an info event so the
// operator notices.
func yrPath() string {
	if v := os.Getenv("JABALI_YR_PATH"); v != "" {
		return v
	}
	return "/usr/local/bin/yr"
}

// rulePaths returns the YARA rule sources mailscan points yr at:
//   - rfxn.yara  (shipped + refreshed by maldet)
//   - /etc/jabali/yara/  (admin-uploaded custom rules)
//
// signature-base (Neo23x0) is INTENTIONALLY EXCLUDED here — many of
// its host-detection rules reference yara identifiers (filename, magic)
// that yara-x doesn't expose, and yr aborts the whole compile if ANY
// rule fails to parse. The filesystem maldet scanner runs signature-base
// via the libclamav-YARA engine which is more permissive. Mail
// attachments are predominantly script/Office payloads that rfxn covers
// well — full host-rule depth isn't load-bearing for the mail path.
//
// Missing dirs are silently skipped — rfxn.yara always exists
// post-install and /etc/jabali/yara is a growth surface.
func rulePaths() []string {
	candidates := []string{
		"/usr/local/maldetect/sigs/rfxn.yara",
		"/etc/jabali/yara",
	}
	out := make([]string, 0, len(candidates))
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// scanBytes runs yr against `data` written to a temp file. Returns the
// first matching rule name, empty on clean. Caller passes a context
// with the per-attachment timeout already applied.
//
// yr CLI shape (verified upstream): `yr scan [rule-or-dir]... <target>`
// with `--negate` to flip semantics. Default output is one match per
// line: `<rule_name> <file>`. We grab the first token of the first
// line.
func scanBytes(ctx context.Context, data []byte, attachmentName string) ScanResult {
	if len(data) == 0 {
		return ScanResult{}
	}
	rules := rulePaths()
	if len(rules) == 0 {
		return ScanResult{EngineErr: errors.New("no YARA rule sources installed")}
	}
	tmp, err := os.CreateTemp("", "mailscan-*-"+sanitiseFilename(attachmentName))
	if err != nil {
		return ScanResult{EngineErr: fmt.Errorf("tempfile: %w", err)}
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return ScanResult{EngineErr: fmt.Errorf("write attachment: %w", err)}
	}
	tmp.Close()

	// yr flags: -w silences rule warnings (rfxn pack triggers tons of
	// "deprecated" notices); --disable-console-logs hides scan progress.
	args := []string{"scan", "-w", "--disable-console-logs"}
	// Prefer a precompiled rule blob: yara-x compiles its sources on EVERY
	// `yr scan`, and the rfxn pack is thousands of rules, so that compile —
	// not the scan — dominates the cost of every attachment. compiledRules
	// returns "" whenever the cache is unavailable for any reason, in which
	// case we fall back to scanning from source exactly as before.
	if blob := compiledRules(ctx, rules); blob != "" {
		args = append(args, "--compiled-rules", blob)
	} else {
		args = append(args, rules...)
	}
	args = append(args, tmp.Name())
	cmd := exec.CommandContext(ctx, yrPath(), args...) //nolint:gosec // argv form, no shell, paths controlled
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// yr exits 0 when nothing matched; non-zero on hit OR on engine
		// failure. Disambiguate via stdout: if we got at least one line
		// of <rule_name> <path> output, it's a hit.
		if rule := firstRuleName(stdout.String(), tmp.Name()); rule != "" {
			return ScanResult{RuleName: rule}
		}
		// Engine failure (parser error, OOM, missing module). Capture
		// stderr tail for the DLQ.
		tail := strings.TrimSpace(stderr.String())
		if len(tail) > 256 {
			tail = tail[:256] + "…"
		}
		if tail == "" {
			tail = err.Error()
		}
		return ScanResult{EngineErr: fmt.Errorf("yr: %s", tail)}
	}
	if rule := firstRuleName(stdout.String(), tmp.Name()); rule != "" {
		return ScanResult{RuleName: rule}
	}
	// Exit-0 with "error:" on stderr means yr loaded zero rules (e.g.
	// permission denied on rule file) and reported "clean" — silently.
	// Surface as engine error so the DLQ catches it instead of a false
	// negative.
	if errLine := stderrErrLine(stderr.String()); errLine != "" {
		return ScanResult{EngineErr: fmt.Errorf("yr: %s", errLine)}
	}
	return ScanResult{}
}

// stderrErrLine returns the first stderr line that begins with "error:"
// or contains "Permission denied" (case-insensitive) — the patterns yr
// uses for non-fatal-but-load-bearing errors that don't change the exit
// code. Empty when stderr is benign (warnings, progress, blank lines).
func stderrErrLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		ll := strings.ToLower(l)
		if strings.HasPrefix(ll, "error:") || strings.Contains(ll, "permission denied") {
			if len(l) > 256 {
				l = l[:256] + "…"
			}
			return l
		}
	}
	return ""
}

// firstRuleName parses the first token of the first non-blank line in
// yr scan output. Filters lines that don't reference our temp filename
// (defence against future yr verbose modes that prefix headers).
func firstRuleName(out, target string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[len(fields)-1] != target && filepath.Base(fields[len(fields)-1]) != filepath.Base(target) {
			continue
		}
		return fields[0]
	}
	return ""
}

// sanitiseFilename strips path separators + control bytes from an
// attachment name so it's safe to embed in a tempfile name. Non-ASCII
// names get a "unsafe" placeholder — yr only needs the file path, the
// real attachment name is preserved in the ingest event metadata.
func sanitiseFilename(name string) string {
	if name == "" {
		return "attach.bin"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		return "attach.bin"
	}
	return out
}

// MimeIsTextish returns true for MIME types where yr-scanning the body
// makes sense without download (already small, low FP). Used by the
// orchestrator to skip oversize text-only parts when budget is tight.
// Currently a no-op stub kept for future use.
func MimeIsTextish(mt string) bool {
	mt = strings.ToLower(mt)
	return strings.HasPrefix(mt, "text/") || mt == "application/json"
}

// drainAndClose ensures the body of an HTTP response is fully drained
// even when we read only the first N bytes — avoids keep-alive leaks
// against Stalwart's connection pool.
func drainAndClose(r io.ReadCloser) {
	_, _ = io.Copy(io.Discard, r)
	_ = r.Close()
}

// timeoutCtx returns a derived context that cancels after dur or when
// parent does. Tiny helper used by scanBytes callers to centralise the
// per-attachment scan-timeout policy.
func timeoutCtx(parent context.Context, dur time.Duration) (context.Context, context.CancelFunc) {
	if dur <= 0 {
		dur = 10 * time.Second
	}
	return context.WithTimeout(parent, dur)
}
