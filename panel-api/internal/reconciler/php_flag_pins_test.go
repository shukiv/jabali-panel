package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func flagPinReconciler(overrides *fakeIniOverrideRepo) *Reconciler {
	r := &Reconciler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if overrides != nil {
		r.phpPoolIniOverrides = overrides
	}
	return r
}

func bp(b bool) *bool { return &b }

// The domain's own value wins; a flag it leaves unset takes the pool's flag
// override; one set by neither is left for the agent's php.ini baseline.
func TestPHPFlagPinParams_DomainThenPool(t *testing.T) {
	ovs := &fakeIniOverrideRepo{byPool: map[string][]models.PHPPoolIniOverride{
		"p1": {
			{Directive: "file_uploads", Value: "off", Kind: "flag"},
			{Directive: "log_errors", Value: "off", Kind: "flag"},
			{Directive: "memory_limit", Value: "1G", Kind: "value"},
		},
	}}
	dom := &models.Domain{ID: "d1", PHPLogErrors: bp(true)}
	got := flagPinReconciler(ovs).phpFlagPinParams(context.Background(), dom, "p1")
	want := map[string]any{"php_log_errors": true, "php_file_uploads": false}
	if len(got) != len(want) || got["php_log_errors"] != true || got["php_file_uploads"] != false {
		t.Fatalf("got %v, want %v (domain log_errors wins, pool file_uploads off, short_open_tag left to the agent)", got, want)
	}
}

// If the pool's overrides cannot be read the agent is told the inherited value
// is unknown, so it does not pin a php.ini baseline over a pool admin flag.
func TestPHPFlagPinParams_UnreadablePoolMarksInheritUnknown(t *testing.T) {
	ovs := &fakeIniOverrideRepo{err: errors.New("db down")}
	dom := &models.Domain{ID: "d1", PHPShortOpenTag: bp(true)}
	got := flagPinReconciler(ovs).phpFlagPinParams(context.Background(), dom, "p1")
	if got["php_flags_inherit_unknown"] != true || got["php_short_open_tag"] != true {
		t.Fatalf("got %v, want inherit_unknown plus the domain's own short_open_tag", got)
	}
	if _, ok := got["php_file_uploads"]; ok {
		t.Fatalf("got %v, want no guessed file_uploads", got)
	}
}

// A pool override counts whether it was saved as a flag or a value, and is
// read the way PHP reads a boolean: "1" is on, "0" is off.
func TestPHPFlagPinParams_ReadsPoolOverridesAsPHPDoes(t *testing.T) {
	ovs := &fakeIniOverrideRepo{byPool: map[string][]models.PHPPoolIniOverride{
		"p1": {
			{Directive: "file_uploads", Value: "0", Kind: "value"},
			{Directive: "short_open_tag", Value: "1", Kind: "flag"},
			{Directive: "log_errors", Value: "yes", Kind: "flag"},
			// Not one of the three flags: never read as a boolean.
			{Directive: "memory_limit", Value: "1G", Kind: "value"},
			{Directive: "memory_limit", Value: "0", Kind: "value"},
		},
	}}
	got := flagPinReconciler(ovs).phpFlagPinParams(context.Background(), &models.Domain{ID: "d1"}, "p1")
	if got["php_file_uploads"] != false || got["php_short_open_tag"] != true || got["php_log_errors"] != true {
		t.Fatalf("got %v, want file_uploads off (value 0), short_open_tag on (flag 1), log_errors on (flag yes)", got)
	}
	if _, ok := got["php_flags_inherit_unknown"]; ok {
		t.Fatalf("got %v, want no inherit_unknown (other directives are not flags)", got)
	}
}

// Two pool overrides that disagree on one flag leave its inherited value
// unknown: that flag is not sent and the agent pins no php.ini guess; the
// other flags still pin.
func TestPHPFlagPinParams_ConflictingPoolOverridesAreUnknown(t *testing.T) {
	ovs := &fakeIniOverrideRepo{byPool: map[string][]models.PHPPoolIniOverride{
		"p1": {
			{Directive: "file_uploads", Value: "off", Kind: "flag"},
			{Directive: "file_uploads", Value: "On", Kind: "value"},
			{Directive: "log_errors", Value: "off", Kind: "flag"},
		},
	}}
	got := flagPinReconciler(ovs).phpFlagPinParams(context.Background(), &models.Domain{ID: "d1"}, "p1")
	if got["php_flags_inherit_unknown"] != true || got["php_log_errors"] != false {
		t.Fatalf("got %v, want inherit_unknown and log_errors off", got)
	}
	if _, ok := got["php_file_uploads"]; ok {
		t.Fatalf("got %v, want no guessed file_uploads", got)
	}
}

// Without a pool or the override repository the pool's flags are unknown, not
// absent: the agent must not pin a php.ini baseline over them.
func TestPHPFlagPinParams_NoPoolLookupMarksInheritUnknown(t *testing.T) {
	dom := &models.Domain{ID: "d1", PHPFileUploads: bp(false)}
	for name, r := range map[string]*Reconciler{
		"no repo": flagPinReconciler(nil),
		"no pool": flagPinReconciler(&fakeIniOverrideRepo{}),
	} {
		poolID := "p1"
		if name == "no pool" {
			poolID = ""
		}
		got := r.phpFlagPinParams(context.Background(), dom, poolID)
		if got["php_flags_inherit_unknown"] != true || got["php_file_uploads"] != false {
			t.Fatalf("%s: got %v, want inherit_unknown plus the domain's own file_uploads", name, got)
		}
	}
}

// createDomainOnAgent sends the flag params for a PHP domain.
func TestCreateDomainOnAgent_SendsPHPFlagPins(t *testing.T) {
	r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	pool := &models.PHPPool{ID: "p1", UserID: dom.UserID, PHPVersion: "8.4"}
	r.WithPHPPools(&fakePHPPoolRepo{pools: map[string]*models.PHPPool{"p1": pool}})
	r.WithPHPPoolIniOverrides(&fakeIniOverrideRepo{byPool: map[string][]models.PHPPoolIniOverride{
		"p1": {{Directive: "file_uploads", Value: "off", Kind: "flag"}},
	}})
	dom.PHPPoolID = &pool.ID
	dom.PHPShortOpenTag = bp(true)

	r.createDomainOnAgent(context.Background(), dom, true)
	call, ok := findAgentCall(ag, "domain.create")
	if !ok {
		t.Fatal("domain.create was not dispatched")
	}
	params := call.params.(map[string]any)
	if params["php_short_open_tag"] != true || params["php_file_uploads"] != false {
		t.Fatalf("domain.create params = %v, want php_short_open_tag=true (domain) and php_file_uploads=false (pool)", params)
	}
	if _, ok := params["php_log_errors"]; ok {
		t.Fatalf("log_errors is set by neither the domain nor the pool; the agent pins php.ini, got %v", params["php_log_errors"])
	}
}
