package cacheprobe

import (
	"net/http"
	"strings"
	"testing"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestClassify_HitOnSecondFetchIsCaching(t *testing.T) {
	r := Classify(hdr(CacheStatusHeader, "MISS"), hdr(CacheStatusHeader, "HIT"))
	if r.Verdict != VerdictCaching {
		t.Fatalf("verdict = %q, want %q (reason: %s)", r.Verdict, VerdictCaching, r.Reason)
	}
}

// The brown-online.co.il case: every configuration check passed, and the site
// had never cached a single response because maintenance mode emitted
// nocache_headers() on every request. The probe has to name THAT, not just
// report "no HIT" — the operator can act on the cause, not the symptom.
func TestClassify_MaintenanceModeNamesTheCause(t *testing.T) {
	second := hdr(
		CacheStatusHeader, "BYPASS",
		"Cache-Control", "no-cache, must-revalidate, max-age=0",
		"Expires", "Wed, 11 Jan 1984 05:00:00 GMT",
	)
	r := Classify(hdr(CacheStatusHeader, "BYPASS"), second)

	if r.Verdict != VerdictNotCaching {
		t.Fatalf("verdict = %q, want %q", r.Verdict, VerdictNotCaching)
	}
	if !strings.Contains(r.Reason, "tells caches not to store") {
		t.Errorf("reason does not name the cause: %s", r.Reason)
	}
	if !strings.Contains(r.Reason, "no-cache") {
		t.Errorf("reason does not quote the offending directive: %s", r.Reason)
	}
	// It must also say the cache is behaving correctly — otherwise the operator
	// goes looking for a cache bug that does not exist.
	if !strings.Contains(r.Reason, "working as designed") {
		t.Errorf("reason blames the cache instead of the site: %s", r.Reason)
	}
}

// The 1984 Expires is WordPress's nocache_headers() signature and appears even
// when Cache-Control has been stripped by a plugin.
func TestClassify_DetectsNocacheHeadersByExpires1984(t *testing.T) {
	second := hdr(CacheStatusHeader, "MISS", "Expires", "Wed, 11 Jan 1984 05:00:00 GMT")
	r := Classify(hdr(CacheStatusHeader, "MISS"), second)
	if !strings.Contains(r.Reason, "1984") {
		t.Errorf("did not recognise the nocache_headers() signature: %s", r.Reason)
	}
}

func TestClassify_BypassWithoutOptOutBlamesTheGate(t *testing.T) {
	r := Classify(hdr(CacheStatusHeader, "BYPASS"), hdr(CacheStatusHeader, "BYPASS"))
	if r.Verdict != VerdictNotCaching {
		t.Fatalf("verdict = %q", r.Verdict)
	}
	if !strings.Contains(r.Reason, "skip gate") {
		t.Errorf("reason = %s", r.Reason)
	}
}

// Vary: Accept-Encoding is normal — nginx keys on it. Flagging it would send
// operators chasing a non-problem on every gzip-enabled site.
func TestClassify_VaryAcceptEncodingIsNotAProblem(t *testing.T) {
	if varyDefeatsCache("Accept-Encoding") {
		t.Error("Accept-Encoding flagged as cache-defeating")
	}
	if varyDefeatsCache("accept-encoding, Accept-Encoding") {
		t.Error("case/duplicate handling wrong")
	}
	for _, v := range []string{"Cookie", "User-Agent", "*", "Accept-Encoding, Cookie"} {
		if !varyDefeatsCache(v) {
			t.Errorf("Vary: %s should defeat a shared cache entry", v)
		}
	}
}

// No header at all means this vhost is not running the jabali cache config, so
// the probe must say nothing rather than claim the cache is broken.
func TestClassify_NoHeaderIsUnknownNotFailure(t *testing.T) {
	r := Classify(hdr(), hdr())
	if r.Verdict != VerdictUnknown {
		t.Fatalf("verdict = %q, want %q", r.Verdict, VerdictUnknown)
	}
	if !strings.Contains(r.Reason, "not running the jabali cache config") {
		t.Errorf("reason = %s", r.Reason)
	}
}

// Two misses with no detectable cause still has to say something useful.
func TestClassify_RepeatedMissReportsBothStatuses(t *testing.T) {
	r := Classify(hdr(CacheStatusHeader, "MISS"), hdr(CacheStatusHeader, "MISS"))
	if r.Verdict != VerdictNotCaching {
		t.Fatalf("verdict = %q", r.Verdict)
	}
	if !strings.Contains(r.Reason, "MISS") {
		t.Errorf("reason should quote the observed statuses: %s", r.Reason)
	}
}

func TestClassify_CaseAndWhitespaceInsensitive(t *testing.T) {
	r := Classify(hdr(CacheStatusHeader, " miss "), hdr(CacheStatusHeader, " hit "))
	if r.Verdict != VerdictCaching {
		t.Fatalf("verdict = %q — header comparison must tolerate case and padding", r.Verdict)
	}
}
