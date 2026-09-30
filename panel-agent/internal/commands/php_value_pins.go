package commands

import (
	"context"
	"log"
	"regexp"
)

// GH #1701: the per-domain value directives (memory_limit, upload sizes, input
// and execution limits, error_reporting, timezone) bleed across sibling
// domains on a reused FPM worker exactly like the flags in php_flag_pins.go:
// a value sent through PHP_VALUE sticks on the worker for the next request
// that sends none. So a PHP vhost pins every one its domain leaves unset to
// the value it inherits:
//
//  1. the pool's own ini override, sent by the panel (php_pool_values). It
//     renders as php_admin_value, above the template's php_value;
//  2. else the box baseline (phpIniBaseline): the pool template's literal
//     php_value lines over the php.ini read, without the CLI-hardcoded
//     max_execution_time / max_input_time (see php_ini_defaults.go);
//  3. else no pin.
//
// When the panel could not read the pool's overrides (php_flags_inherit_unknown)
// nothing is pinned from the baseline: it could defeat an admin override PHP
// would otherwise apply. Every value is checked before it reaches the vhost's
// PHP_VALUE line; one that fails is not pinned.

var (
	phpSizeValueRE = regexp.MustCompile(`^(-1|\d{1,8}[KMGkmg]?)$`)
	phpIntValueRE  = regexp.MustCompile(`^-?\d{1,10}$`)
)

// phpValuePinDirectives lists the directives in the order they are pinned.
var phpValuePinDirectives = []struct {
	name string
	re   *regexp.Regexp
}{
	{"memory_limit", phpSizeValueRE},
	{"upload_max_filesize", phpSizeValueRE},
	{"post_max_size", phpSizeValueRE},
	{"max_input_vars", phpIntValueRE},
	{"max_execution_time", phpIntValueRE},
	{"max_input_time", phpIntValueRE},
	{"error_reporting", phpIntValueRE},
	{"date.timezone", phpTimezoneRE},
}

// domainSetsPHPValue reports whether the domain carries its own value for a
// directive: the same gates buildPHPValueParam emits on.
func domainSetsPHPValue(p *domainCreateParams, directive string) bool {
	switch directive {
	case "memory_limit":
		return p.PHPMemoryLimit != ""
	case "upload_max_filesize":
		return p.PHPUploadMaxFilesize != ""
	case "post_max_size":
		return p.PHPPostMaxSize != ""
	case "max_input_vars":
		return p.PHPMaxInputVars > 0
	case "max_execution_time":
		return p.PHPMaxExecutionTime > 0
	case "max_input_time":
		return p.PHPMaxInputTime > 0
	case "error_reporting":
		return p.PHPErrorReporting != nil
	case "date.timezone":
		return p.PHPTimezone != "" && phpTimezoneRE.MatchString(p.PHPTimezone)
	}
	return false
}

// phpInheritedValuePins returns the "directive=value" lines to pin for the
// directives the domain leaves unset.
func phpInheritedValuePins(ctx context.Context, p *domainCreateParams) []string {
	if !p.HasPHP {
		return nil
	}
	var base map[string]string
	baseRead := false
	var out []string
	for _, d := range phpValuePinDirectives {
		if domainSetsPHPValue(p, d.name) {
			continue
		}
		v, ok := p.PHPPoolValues[d.name]
		if !ok {
			if p.PHPFlagsInheritUnknown {
				continue
			}
			if !baseRead {
				baseRead = true
				base = phpValueBaseline(ctx, p.PHPVersion)
			}
			if v, ok = base[d.name]; !ok {
				continue
			}
			switch d.name {
			case "date.timezone":
				// A php.ini with no date.timezone runs as UTC.
				if v == "" {
					v = "UTC"
				}
			case "error_reporting":
				if v == "" {
					continue
				}
			}
		}
		if !d.re.MatchString(v) {
			log.Printf("domain.create %s: not pinning %s: inherited value %q is not valid", p.Domain, d.name, v)
			continue
		}
		out = append(out, d.name+"="+v)
	}
	return out
}

// phpValueBaseline is the cached box baseline for a PHP version, or nil.
func phpValueBaseline(ctx context.Context, version string) map[string]string {
	if !phpVersionRE.MatchString(version) {
		return nil
	}
	base, err := phpIniBaseline(ctx, version)
	if err != nil {
		log.Printf("domain.create: php %s baseline unavailable, not pinning inherited PHP values: %v", version, err)
		return nil
	}
	return base
}
