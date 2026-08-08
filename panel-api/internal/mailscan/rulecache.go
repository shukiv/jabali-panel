package mailscan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// yara-x compiles its rule sources on every `yr scan`. The rfxn pack is
// thousands of rules, so that compile dominates the cost of scanning one small
// mail attachment: per-message cost was constant-and-large regardless of
// attachment size, and a busy tick pays it up to PerTickBudget (200) times.
//
// `yr compile` produces a single binary that `yr scan --compiled-rules`
// consumes, which turns the compile into a once-per-rule-change cost.
//
// Two things make this safe to land without a yara-x binary to test against:
//
//  1. The output flag is DISCOVERED, not assumed. yr's own `compile --help`
//     is parsed for a long output option; if none is found we do not guess.
//  2. Every failure degrades to the existing source-scanning path, which is
//     exactly today's behaviour. A wrong guess, an old yr, an unwritable cache
//     dir, or a corrupt binary all end in "scan from source" rather than in
//     malware scanning silently stopping.

// ruleCacheDir holds the compiled rule blob. Under /var/lib so it survives a
// reboot (unlike /run) and is writable by the panel service user.
const ruleCacheDir = "/var/lib/jabali-panel/mailscan"

// compiledRulesTTL bounds how long a compiled blob is reused without
// re-checking the sources. The fingerprint check below is the real
// invalidation; this only bounds the stat() work.
const compiledRulesTTL = 5 * time.Minute

type ruleCacheState struct {
	mu          sync.Mutex
	path        string // compiled blob, "" when unavailable
	fingerprint string
	checkedAt   time.Time
	// unsupported latches once we know this yr cannot compile, so we stop
	// re-probing on every attachment.
	unsupported bool
}

var ruleCache = &ruleCacheState{}

var compileWarnOnce sync.Once

func warnCompileUnavailable(reason string) {
	compileWarnOnce.Do(func() {
		slog.Info("mailscan: scanning from rule sources (compiled-rule cache unavailable); "+
			"each attachment recompiles the YARA pack", "reason", reason)
	})
}

// rulesFingerprint identifies the current rule sources by path, size and
// mtime. Cheap (stat only) and changes whenever maldet updates the pack.
func rulesFingerprint(paths []string) string {
	h := sha256.New()
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(h, "%s|missing\n", p)
			continue
		}
		// A directory's own mtime moves when entries are added or removed,
		// which is the case that matters for /etc/jabali/yara.
		fmt.Fprintf(h, "%s|%d|%d\n", p, fi.Size(), fi.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// compileOutputFlag asks yr which long flag `compile` uses to write its
// output. Returns "" when the subcommand or the flag is absent, which is the
// signal to keep scanning from source rather than guess.
func compileOutputFlag(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, yrPath(), "compile", "--help").CombinedOutput()
	if err != nil {
		return ""
	}
	help := string(out)
	for _, flag := range []string{"--output-path", "--output"} {
		if strings.Contains(help, flag) {
			return flag
		}
	}
	return ""
}

// compiledRules returns a path to a compiled rule blob for the given sources,
// or "" to indicate the caller should scan from source.
func compiledRules(ctx context.Context, rules []string) string {
	if len(rules) == 0 {
		return ""
	}
	ruleCache.mu.Lock()
	defer ruleCache.mu.Unlock()

	if ruleCache.unsupported {
		return ""
	}
	if ruleCache.path != "" && time.Since(ruleCache.checkedAt) < compiledRulesTTL {
		return ruleCache.path
	}

	fp := rulesFingerprint(rules)
	if ruleCache.path != "" && ruleCache.fingerprint == fp {
		if _, err := os.Stat(ruleCache.path); err == nil {
			ruleCache.checkedAt = time.Now()
			return ruleCache.path
		}
	}

	flag := compileOutputFlag(ctx)
	if flag == "" {
		ruleCache.unsupported = true
		warnCompileUnavailable("yr compile has no discoverable output flag")
		return ""
	}
	if err := os.MkdirAll(ruleCacheDir, 0o750); err != nil {
		ruleCache.unsupported = true
		warnCompileUnavailable("cache dir: " + err.Error())
		return ""
	}

	// Compile to a temp file and rename, so a concurrent scan never reads a
	// half-written blob.
	tmp, err := os.CreateTemp(ruleCacheDir, "rules-*.bin")
	if err != nil {
		warnCompileUnavailable("tempfile: " + err.Error())
		return ""
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	args := append([]string{"compile", flag, tmpName}, rules...)
	if out, cerr := exec.CommandContext(cctx, yrPath(), args...).CombinedOutput(); cerr != nil {
		// Do NOT latch unsupported here: a compile can fail for a transient
		// reason (a rule file mid-update by maldet). Retry on the next tick.
		warnCompileUnavailable(fmt.Sprintf("compile failed: %v: %s", cerr, strings.TrimSpace(string(out))))
		return ""
	}

	final := filepath.Join(ruleCacheDir, "rules.bin")
	if err := os.Rename(tmpName, final); err != nil {
		warnCompileUnavailable("install compiled rules: " + err.Error())
		return ""
	}
	ruleCache.path = final
	ruleCache.fingerprint = fp
	ruleCache.checkedAt = time.Now()
	return final
}
