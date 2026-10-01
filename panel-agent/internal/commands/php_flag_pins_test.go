package commands

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func boolp(b bool) *bool { return &b }

// stubPHPIniBaseline replaces the php.ini reader and clears the cache.
func stubPHPIniBaseline(t *testing.T, vals map[string]string, err error) *int {
	t.Helper()
	calls := new(int)
	prev := phpIniBaselineRead
	phpIniBaselineRead = func(context.Context, string) (map[string]string, error) {
		*calls++
		return vals, err
	}
	phpIniBaselineMu.Lock()
	phpIniBaselineCache = map[string]phpIniBaselineEntry{}
	phpIniBaselineMu.Unlock()
	t.Cleanup(func() {
		phpIniBaselineRead = prev
		phpIniBaselineMu.Lock()
		phpIniBaselineCache = map[string]phpIniBaselineEntry{}
		phpIniBaselineMu.Unlock()
	})
	return calls
}

func TestWithPHPFlagPins(t *testing.T) {
	all := phpFlagPins{LogErrors: boolp(true), FileUploads: boolp(false), ShortOpenTag: boolp(false)}
	if got := withPHPFlagPins("display_errors=Off", all); got != "display_errors=Off\nlog_errors=On\nfile_uploads=Off\nshort_open_tag=Off" {
		t.Fatalf("got %q", got)
	}
	// A non-PHP vhost has no PHP_VALUE and gets no pins.
	if got := withPHPFlagPins("", all); got != "" {
		t.Fatalf("non-PHP vhost got %q, want empty", got)
	}
	// Unset flags emit nothing.
	if got := withPHPFlagPins("display_errors=Off", phpFlagPins{ShortOpenTag: boolp(true)}); got != "display_errors=Off\nshort_open_tag=On" {
		t.Fatalf("got %q", got)
	}
}

// A domain that sets no value inherits: the box php.ini baseline is pinned, so
// a sibling's value on the same reused FPM worker cannot bleed into it.
func TestResolvePHPFlagPins_FillsUnsetFromPHPIni(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{"log_errors": "1", "file_uploads": "1", "short_open_tag": ""}, nil)
	got := resolvePHPFlagPins(context.Background(), true, "8.4", phpFlagPins{ShortOpenTag: boolp(true)})
	if got.LogErrors == nil || !*got.LogErrors || got.FileUploads == nil || !*got.FileUploads {
		t.Fatalf("unset flags must take the php.ini baseline (On), got %+v", got)
	}
	if got.ShortOpenTag == nil || !*got.ShortOpenTag {
		t.Fatalf("the domain's own short_open_tag=On must win over the baseline, got %+v", got)
	}
}

// A php.ini that turns short_open_tag on (a legacy box) is pinned as On, not
// replaced by a guessed Off.
func TestResolvePHPFlagPins_KeepsALegacyPHPIniValue(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{"log_errors": "", "file_uploads": "1", "short_open_tag": "1"}, nil)
	got := resolvePHPFlagPins(context.Background(), true, "8.4", phpFlagPins{})
	if got.ShortOpenTag == nil || !*got.ShortOpenTag || got.LogErrors == nil || *got.LogErrors {
		t.Fatalf("got %+v, want short_open_tag On and log_errors Off from php.ini", got)
	}
}

// If the baseline cannot be read, inherited flags stay unpinned (today's
// behaviour) rather than pinned to a guess; the domain's own value still pins.
func TestResolvePHPFlagPins_ReadFailureLeavesInheritedUnpinned(t *testing.T) {
	stubPHPIniBaseline(t, nil, errors.New("php8.4: not found"))
	got := resolvePHPFlagPins(context.Background(), true, "8.4", phpFlagPins{FileUploads: boolp(false)})
	if got.LogErrors != nil || got.ShortOpenTag != nil {
		t.Fatalf("a failed read must not invent values, got %+v", got)
	}
	if got.FileUploads == nil || *got.FileUploads {
		t.Fatalf("the domain's own file_uploads=Off must still pin, got %+v", got)
	}
}

func TestResolvePHPFlagPins_NonPHPAndBadVersion(t *testing.T) {
	calls := stubPHPIniBaseline(t, map[string]string{"log_errors": "1", "file_uploads": "1", "short_open_tag": ""}, nil)
	if got := resolvePHPFlagPins(context.Background(), false, "8.4", phpFlagPins{LogErrors: boolp(true)}); !reflect.DeepEqual(got, phpFlagPins{}) {
		t.Fatalf("non-PHP vhost got %+v, want no pins", got)
	}
	if got := resolvePHPFlagPins(context.Background(), true, "8.4; rm -rf /", phpFlagPins{}); !reflect.DeepEqual(got, phpFlagPins{}) {
		t.Fatalf("a malformed version must not be read, got %+v", got)
	}
	if *calls != 0 {
		t.Fatalf("php.ini read %d times, want 0", *calls)
	}
}

// The baseline is cached per version, so a reconcile pass over many domains
// runs PHP once.
func TestPHPIniBaseline_CachedPerVersion(t *testing.T) {
	calls := stubPHPIniBaseline(t, map[string]string{"log_errors": "1", "file_uploads": "1", "short_open_tag": ""}, nil)
	for i := 0; i < 5; i++ {
		resolvePHPFlagPins(context.Background(), true, "8.4", phpFlagPins{})
	}
	resolvePHPFlagPins(context.Background(), true, "8.3", phpFlagPins{})
	if *calls != 2 {
		t.Fatalf("php.ini read %d times, want 2 (one per version)", *calls)
	}
}

// End to end through the vhost template: a PHP domain's PHP_VALUE line always
// carries all three flags.
func TestPHPValueLineCarriesThePinnedFlags(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{"log_errors": "1", "file_uploads": "1", "short_open_tag": ""}, nil)
	pins := resolvePHPFlagPins(context.Background(), true, "8.4", phpFlagPins{FileUploads: boolp(false)})
	line := withPHPFlagPins(buildPHPValueParam(true, "", "", "", 0, 0, 0, false, nil, ""), pins)
	for _, want := range []string{"log_errors=On", "file_uploads=Off", "short_open_tag=Off", "display_errors=Off"} {
		if !strings.Contains(line, want) {
			t.Fatalf("PHP_VALUE %q lacks %q", line, want)
		}
	}
}

// domain.create: when the panel could not read the pool's flag overrides, the
// inherited value is unknown and must not be pinned from php.ini (it could
// undo a pool's php_admin_flag); the values the panel did send still pin.
func TestPHPFlagPinsForParams_InheritUnknownPinsOnlySentValues(t *testing.T) {
	calls := stubPHPIniBaseline(t, map[string]string{"log_errors": "1", "file_uploads": "1", "short_open_tag": ""}, nil)
	p := &domainCreateParams{HasPHP: true, PHPVersion: "8.4", PHPShortOpenTag: boolp(true), PHPFlagsInheritUnknown: true}
	got := phpFlagPinsForParams(context.Background(), p)
	if got.LogErrors != nil || got.FileUploads != nil || got.ShortOpenTag == nil || !*got.ShortOpenTag {
		t.Fatalf("got %+v, want only short_open_tag pinned", got)
	}
	if *calls != 0 {
		t.Fatalf("php.ini read %d times with inherit unknown, want 0", *calls)
	}

	p.PHPFlagsInheritUnknown = false
	got = phpFlagPinsForParams(context.Background(), p)
	if got.LogErrors == nil || got.FileUploads == nil {
		t.Fatalf("with the pool's flags known the inherited values must pin, got %+v", got)
	}

	p.HasPHP = false
	if got := phpFlagPinsForParams(context.Background(), p); !reflect.DeepEqual(got, phpFlagPins{}) {
		t.Fatalf("non-PHP vhost got %+v", got)
	}
}

// The JSON the panel sends decodes onto the params the pin decision reads.
func TestDomainCreateParams_DecodePHPFlagFields(t *testing.T) {
	var p domainCreateParams
	raw := `{"has_php":true,"php_version":"8.4","php_log_errors":false,"php_file_uploads":true,"php_short_open_tag":true,"php_flags_inherit_unknown":true,"php_pool_values":{"memory_limit":"1G","date.timezone":"Europe/Berlin"}}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.PHPLogErrors == nil || *p.PHPLogErrors || p.PHPFileUploads == nil || !*p.PHPFileUploads ||
		p.PHPShortOpenTag == nil || !*p.PHPShortOpenTag || !p.PHPFlagsInheritUnknown ||
		p.PHPPoolValues["memory_limit"] != "1G" || p.PHPPoolValues["date.timezone"] != "Europe/Berlin" {
		t.Fatalf("decoded %+v", p)
	}
}
