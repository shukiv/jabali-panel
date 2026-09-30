package commands

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// GH #1701 Slice 2: per-domain log_errors, file_uploads and short_open_tag.
//
// A value sent through fastcgi_param PHP_VALUE sticks on the reused FPM worker:
// the next request on that worker that sends no value inherits it (drilled on
// PHP 8.4 and 8.5 — log_errors, file_uploads and short_open_tag all carried
// over). One pool serves all of a user's domains on a PHP version, so a value
// set on one domain would leak onto a sibling: short_open_tag=On breaks a
// sibling's `<?xml` templates, file_uploads=Off breaks its uploads at random.
// So these three are PINNED on every PHP vhost: the domain's own value when it
// has one, else the value it inherits. An explicit value resets a bled one.
//
// The inherited value must be the real one, never a hardcoded default. PHP_VALUE
// overrides even a pool's php_admin_flag (drilled: it replaced an admin
// sendmail_path), so pinning a guessed "On" would silently undo an admin's pool
// flag, or break a box whose php.ini turns short_open_tag on for legacy sites.
// The panel sends the domain's value, else the pool's flag override; for what
// is still unset the agent reads the box php.ini for the domain's PHP version.
// If that read fails the directive is not pinned (today's behaviour) rather
// than pinned to a guess.

// phpFlagPins holds the three flags. A nil field is unset (no pin emitted).
// Values carries the inherited value pins ("directive=value", see
// php_value_pins.go), appended the same way.
type phpFlagPins struct {
	LogErrors    *bool
	FileUploads  *bool
	ShortOpenTag *bool
	Values       []string
}

// withPHPFlagPins appends the pinned flags to a PHP_VALUE body. An empty body
// is a non-PHP vhost (buildPHPValueParam emits nothing for those) and stays
// empty.
func withPHPFlagPins(phpValue string, pins phpFlagPins) string {
	if phpValue == "" {
		return ""
	}
	parts := append([]string(nil), pins.Values...)
	for _, f := range []struct {
		name string
		v    *bool
	}{
		{"log_errors", pins.LogErrors},
		{"file_uploads", pins.FileUploads},
		{"short_open_tag", pins.ShortOpenTag},
	} {
		if f.v == nil {
			continue
		}
		if *f.v {
			parts = append(parts, f.name+"=On")
		} else {
			parts = append(parts, f.name+"=Off")
		}
	}
	if len(parts) == 0 {
		return phpValue
	}
	return phpValue + "\n" + strings.Join(parts, "\n")
}

// resolvePHPFlagPins fills each flag the panel left unset with the box php.ini
// baseline for phpVersion. A non-PHP vhost, an unknown version or a failed read
// leaves the unset flags nil.
func resolvePHPFlagPins(ctx context.Context, hasPHP bool, phpVersion string, sent phpFlagPins) phpFlagPins {
	out := sent
	if !hasPHP {
		return phpFlagPins{}
	}
	if out.LogErrors != nil && out.FileUploads != nil && out.ShortOpenTag != nil {
		return out
	}
	if !phpVersionRE.MatchString(phpVersion) {
		return out
	}
	base, err := phpIniBaseline(ctx, phpVersion)
	if err != nil {
		log.Printf("domain.create: php %s ini baseline unavailable, not pinning inherited PHP flags: %v", phpVersion, err)
		return out
	}
	fill := func(dst **bool, directive string) {
		if *dst != nil {
			return
		}
		raw, ok := base[directive]
		if !ok {
			return
		}
		v := phpIniBool(raw)
		*dst = &v
	}
	fill(&out.LogErrors, "log_errors")
	fill(&out.FileUploads, "file_uploads")
	fill(&out.ShortOpenTag, "short_open_tag")
	return out
}

// phpFlagPinsForParams is domain.create's pin decision: the values the panel
// sent, with the unset ones filled from php.ini — unless the panel says the
// pool's flags could not be read, when only the sent values are pinned.
func phpFlagPinsForParams(ctx context.Context, p *domainCreateParams) phpFlagPins {
	if !p.HasPHP {
		return phpFlagPins{}
	}
	pins := phpFlagPins{LogErrors: p.PHPLogErrors, FileUploads: p.PHPFileUploads, ShortOpenTag: p.PHPShortOpenTag}
	if !p.PHPFlagsInheritUnknown {
		pins = resolvePHPFlagPins(ctx, true, p.PHPVersion, pins)
	}
	pins.Values = phpInheritedValuePins(ctx, p)
	return pins
}

// phpIniBool reads an ini_get() flag: "1" (or on/yes/true) is on; "", "0" and
// anything else is off.
func phpIniBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "on", "yes", "true":
		return true
	}
	return false
}

// phpIniBaselineTTL bounds how long a version's php.ini read is reused. The
// baseline changes only when an operator edits php.ini; the cache keeps a
// reconcile pass over many domains from running PHP once per domain.
const phpIniBaselineTTL = 10 * time.Minute

var (
	phpIniBaselineMu    sync.Mutex
	phpIniBaselineCache = map[string]phpIniBaselineEntry{}
	// phpIniBaselineRead is the uncached reader; tests replace it.
	phpIniBaselineRead = readPHPIniDefaults
)

type phpIniBaselineEntry struct {
	at   time.Time
	vals map[string]string
}

func phpIniBaseline(ctx context.Context, version string) (map[string]string, error) {
	phpIniBaselineMu.Lock()
	if e, ok := phpIniBaselineCache[version]; ok && time.Since(e.at) < phpIniBaselineTTL {
		phpIniBaselineMu.Unlock()
		return e.vals, nil
	}
	phpIniBaselineMu.Unlock()

	vals, err := phpIniBaselineRead(ctx, version)
	if err != nil {
		return nil, err
	}
	phpIniBaselineMu.Lock()
	phpIniBaselineCache[version] = phpIniBaselineEntry{at: time.Now(), vals: vals}
	phpIniBaselineMu.Unlock()
	return vals, nil
}
