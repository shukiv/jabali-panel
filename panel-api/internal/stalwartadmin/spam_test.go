package stalwartadmin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailspam"
)

// fakeSpam holds Stalwart's SpamSettings singleton and answers the JMAP calls
// SpamScores makes the way Stalwart 0.16.24 did on the .60 test box.
type fakeSpam struct {
	t          *testing.T
	settings   map[string]any // the singleton as x:SpamSettings/get returns it
	updates    []map[string]any
	reloads    int
	failUpdate bool
	failReload bool
	gets       int
}

func newFakeSpam(t *testing.T, junk, reject, discard float64) (SpamScores, *fakeSpam) {
	t.Helper()
	f := &fakeSpam{t: t, settings: map[string]any{
		"id": "singleton", "enable": true, "trustContacts": true, "trustReplies": true,
		"scoreSpam": junk, "scoreReject": reject, "scoreDiscard": discard,
		"spamFilterRulesUrl": "file:///opt/stalwart/share/spam-filter-rules.json.gz",
	}}
	c, _ := newTestClient(t, f.answer)
	return SpamScores{Client: c}, f
}

func (f *fakeSpam) answer(method string, raw json.RawMessage) (int, string, any) {
	var args struct {
		IDs    []string                  `json:"ids"`
		Update map[string]map[string]any `json:"update"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		f.t.Errorf("%s args: %v", method, err)
	}
	switch method {
	case "x:SpamSettings/get":
		f.gets++
		if !reflect.DeepEqual(args.IDs, []string{"singleton"}) {
			f.t.Errorf("get ids = %v, want [singleton]", args.IDs)
		}
		return 200, method, map[string]any{"list": []any{f.settings}, "notFound": []string{}}
	case "x:SpamSettings/set":
		p, ok := args.Update["singleton"]
		if !ok || len(args.Update) != 1 {
			f.t.Errorf("set update = %v, want the singleton only", args.Update)
		}
		f.updates = append(f.updates, p)
		if f.failUpdate {
			return 200, method, map[string]any{"notUpdated": map[string]any{"singleton": map[string]any{
				"type": "validationFailed", "validationErrors": []any{map[string]any{"type": "MaxValue", "property": "scoreReject", "required": 100}},
			}}}
		}
		for k, v := range p {
			f.settings[k] = v
		}
		return 200, method, map[string]any{"updated": map[string]any{"singleton": nil}}
	case "x:Action/set":
		f.reloads++
		if f.failReload {
			return 200, method, map[string]any{"notCreated": map[string]any{"reload": map[string]any{"type": "validationFailed"}}}
		}
		return 200, method, map[string]any{"created": map[string]any{"reload": map[string]any{"id": "r1"}}}
	}
	f.t.Errorf("unexpected call %s", method)
	return 500, method, nil
}

// A changed threshold is written (the three scores only) and then made live:
// Stalwart keeps scoring with the old thresholds until a settings reload.
func TestSpamScores_WritesThenReloads(t *testing.T) {
	s, f := newFakeSpam(t, 5, 15, 20)
	want := mailspam.Scores{Junk: 8, Reject: 0, Discard: 25}
	changed, err := s.Apply(context.Background(), want, false)
	if err != nil || !changed {
		t.Fatalf("Apply = %v, %v; want changed", changed, err)
	}
	if len(f.updates) != 1 || !reflect.DeepEqual(f.updates[0], map[string]any{"scoreSpam": 8.0, "scoreReject": 0.0, "scoreDiscard": 25.0}) {
		t.Fatalf("updates = %v", f.updates)
	}
	if f.reloads != 1 {
		t.Fatalf("reloads = %d, want 1", f.reloads)
	}
	if f.settings["enable"] != true || f.settings["trustContacts"] != true {
		t.Fatalf("other fields changed: %v", f.settings)
	}
}

// Steady state: same thresholds, nothing written, no reload.
func TestSpamScores_NoChangeNoWrite(t *testing.T) {
	s, f := newFakeSpam(t, 5, 15, 20)
	changed, err := s.Apply(context.Background(), mailspam.Default, false)
	if err != nil || changed {
		t.Fatalf("Apply = %v, %v; want unchanged", changed, err)
	}
	if len(f.updates) != 0 || f.reloads != 0 || f.gets != 1 {
		t.Fatalf("updates = %v, reloads = %d, gets = %d", f.updates, f.reloads, f.gets)
	}
}

// reload asks for a reload with nothing to write: the retry after a write
// whose reload failed.
func TestSpamScores_ReloadWithoutWrite(t *testing.T) {
	s, f := newFakeSpam(t, 5, 15, 20)
	changed, err := s.Apply(context.Background(), mailspam.Default, true)
	if err != nil || changed {
		t.Fatalf("Apply = %v, %v", changed, err)
	}
	if len(f.updates) != 0 || f.reloads != 1 {
		t.Fatalf("updates = %v, reloads = %d; want a reload only", f.updates, f.reloads)
	}
}

// A failed reload after a write reports the write and the error, so the
// caller knows the stored thresholds are not live yet.
func TestSpamScores_ReloadFailureAfterWrite(t *testing.T) {
	s, f := newFakeSpam(t, 5, 15, 20)
	f.failReload = true
	changed, err := s.Apply(context.Background(), mailspam.Scores{Junk: 9, Reject: 15, Discard: 20}, false)
	if err == nil || !changed {
		t.Fatalf("Apply = %v, %v; want changed and an error", changed, err)
	}
}

func TestSpamScores_UpdateRefused(t *testing.T) {
	s, f := newFakeSpam(t, 5, 15, 20)
	f.failUpdate = true
	changed, err := s.Apply(context.Background(), mailspam.Scores{Junk: 9, Reject: 15, Discard: 20}, false)
	if err == nil || changed {
		t.Fatalf("Apply = %v, %v; want an error and nothing changed", changed, err)
	}
	if f.reloads != 0 {
		t.Fatalf("reloads = %d after a refused update", f.reloads)
	}
}

// Thresholds the panel would refuse are never sent to the mail server.
func TestSpamScores_RefusesInvalid(t *testing.T) {
	s, f := newFakeSpam(t, 5, 15, 20)
	if _, err := s.Apply(context.Background(), mailspam.Scores{Junk: 10, Reject: 5, Discard: 0}, true); err == nil {
		t.Fatal("Apply took scores with reject below junk")
	}
	if f.gets != 0 || len(f.updates) != 0 || f.reloads != 0 {
		t.Fatalf("calls made: gets %d, updates %v, reloads %d", f.gets, f.updates, f.reloads)
	}
}

// A singleton without readable scores is not taken as "all zero" and
// overwritten blind; the pass fails and retries.
func TestSpamScores_UnreadableSingleton(t *testing.T) {
	s, f := newFakeSpam(t, 5, 15, 20)
	delete(f.settings, "scoreSpam")
	if _, err := s.Apply(context.Background(), mailspam.Default, false); err == nil {
		t.Fatal("Apply took a singleton without scoreSpam")
	}
	if len(f.updates) != 0 {
		t.Fatalf("updates = %v", f.updates)
	}
}
