package mailspam

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		s    Scores
		ok   bool
	}{
		{"defaults", Default, true},
		{"reject and discard off", Scores{Junk: 6, Reject: 0, Discard: 0}, true},
		{"only discard", Scores{Junk: 6, Reject: 0, Discard: 30}, true},
		{"discard at or above reject is allowed", Scores{Junk: 5, Reject: 15, Discard: 15}, true},
		{"top of the range", Scores{Junk: 100, Reject: 0, Discard: 0}, true},
		{"fractional", Scores{Junk: 7.5, Reject: 12.5, Discard: 0}, true},
		{"junk zero", Scores{Junk: 0, Reject: 15, Discard: 20}, false},
		{"junk negative", Scores{Junk: -1, Reject: 15, Discard: 20}, false},
		{"junk above 100", Scores{Junk: 100.5, Reject: 0, Discard: 0}, false},
		{"junk NaN", Scores{Junk: math.NaN(), Reject: 15, Discard: 20}, false},
		{"junk infinite", Scores{Junk: math.Inf(1), Reject: 0, Discard: 0}, false},
		{"reject equal to junk", Scores{Junk: 10, Reject: 10, Discard: 0}, false},
		{"reject below junk", Scores{Junk: 10, Reject: 8, Discard: 0}, false},
		{"reject negative", Scores{Junk: 5, Reject: -3, Discard: 0}, false},
		{"reject above 100", Scores{Junk: 5, Reject: 101, Discard: 0}, false},
		{"reject NaN", Scores{Junk: 5, Reject: math.NaN(), Discard: 0}, false},
		{"discard equal to junk", Scores{Junk: 10, Reject: 0, Discard: 10}, false},
		{"discard below junk", Scores{Junk: 10, Reject: 0, Discard: 4}, false},
		{"discard negative", Scores{Junk: 5, Reject: 0, Discard: -1}, false},
		{"discard above 100", Scores{Junk: 5, Reject: 0, Discard: 150}, false},
		{"discard infinite", Scores{Junk: 5, Reject: 0, Discard: math.Inf(1)}, false},
	}
	for _, c := range cases {
		err := c.s.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: Validate(%+v) = %v, want ok", c.name, c.s, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: Validate(%+v) = nil, want an error", c.name, c.s)
		}
	}
}

// The error names the field the admin has to change.
func TestValidate_NamesTheField(t *testing.T) {
	for field, s := range map[string]Scores{
		"spam_junk_score":    {Junk: 0, Reject: 15, Discard: 20},
		"spam_reject_score":  {Junk: 5, Reject: 3, Discard: 20},
		"spam_discard_score": {Junk: 5, Reject: 15, Discard: 3},
	} {
		err := s.Validate()
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("Validate(%+v) = %v, want an error naming %s", s, err, field)
		}
	}
}

// Payload writes the three thresholds and nothing else: enable, the trust
// switches and the rules URL stay install.sh's.
func TestPayload(t *testing.T) {
	got := Scores{Junk: 7.5, Reject: 0, Discard: 30}.Payload()
	want := map[string]any{"scoreSpam": 7.5, "scoreReject": 0.0, "scoreDiscard": 30.0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Payload = %#v, want %#v", got, want)
	}
}

func TestFromStalwart(t *testing.T) {
	raw := json.RawMessage(`{"id":"singleton","enable":true,"scoreSpam":5.5,"scoreReject":15.0,"scoreDiscard":0,"trustContacts":true}`)
	got, err := FromStalwart(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Scores{Junk: 5.5, Reject: 15, Discard: 0}) {
		t.Fatalf("FromStalwart = %+v", got)
	}
	// A singleton without the score fields is not read as all-zero.
	for _, bad := range []string{`{"id":"singleton","enable":true}`, `{"scoreSpam":5,"scoreReject":15}`, `not json`} {
		if _, err := FromStalwart(json.RawMessage(bad)); err == nil {
			t.Errorf("FromStalwart(%s) = nil error", bad)
		}
	}
}

func TestEqual(t *testing.T) {
	a := Scores{Junk: 5.1, Reject: 15, Discard: 20}
	if !a.Equal(Scores{Junk: 5.1 + 1e-9, Reject: 15, Discard: 20}) {
		t.Error("a float round trip must compare equal")
	}
	for _, b := range []Scores{
		{Junk: 5.2, Reject: 15, Discard: 20},
		{Junk: 5.1, Reject: 14, Discard: 20},
		{Junk: 5.1, Reject: 15, Discard: 0},
	} {
		if a.Equal(b) {
			t.Errorf("%+v Equal %+v", a, b)
		}
	}
}

// The thresholds have three homes that must agree on a box nobody has
// touched: the apply plan (a fresh install), the migration (the panel's
// stored values, on a fresh install and on update) and the model's defaults.
// A drift would move mail between the Inbox and Junk on the first reconcile
// after an install or update, with no admin action.
func TestDefaultsAgree(t *testing.T) {
	root := repoRoot(t)

	plan := readFile(t, root, "install", "stalwart", "apply-plan.json.tmpl")
	block := regexp.MustCompile(`(?s)"object":\s*"x:SpamSettings".*?"value":\s*\{[^}]*\}`).Find(plan)
	if block == nil {
		t.Fatal("apply-plan.json.tmpl: no x:SpamSettings block")
	}
	planScores := Scores{
		Junk:    num(t, block, "scoreSpam"),
		Reject:  num(t, block, "scoreReject"),
		Discard: num(t, block, "scoreDiscard"),
	}
	if planScores != Default {
		t.Errorf("apply plan scores %+v, Default %+v", planScores, Default)
	}

	up := readFile(t, root, "panel-api", "internal", "db", "migrations", "000319_mail_spam_scores.up.sql")
	migScores := Scores{
		Junk:    colDefault(t, up, "spam_junk_score"),
		Reject:  colDefault(t, up, "spam_reject_score"),
		Discard: colDefault(t, up, "spam_discard_score"),
	}
	if migScores != Default {
		t.Errorf("migration defaults %+v, Default %+v", migScores, Default)
	}

	typ := reflect.TypeOf(models.ServerSettings{})
	modelScores := Scores{
		Junk:    tagDefault(t, typ, "SpamJunkScore"),
		Reject:  tagDefault(t, typ, "SpamRejectScore"),
		Discard: tagDefault(t, typ, "SpamDiscardScore"),
	}
	if modelScores != Default {
		t.Errorf("model defaults %+v, Default %+v", modelScores, Default)
	}
}

// install.sh re-applies its SpamSettings patch on every install and update.
// The thresholds belong to the panel now, so the patch must not carry them,
// or every update would put an admin's thresholds back to the defaults.
// The rest of the patch (filter on, trust contacts and replies, pinned
// rules) stays.
func TestInstallConvergerLeavesThresholdsToThePanel(t *testing.T) {
	sh := readFile(t, repoRoot(t), "install.sh")
	m := regexp.MustCompile(`spam_patch='([^']*)'`).FindSubmatch(sh)
	if m == nil {
		t.Fatal("install.sh: no spam_patch='{...}' assignment")
	}
	var patch map[string]any
	if err := json.Unmarshal(m[1], &patch); err != nil {
		t.Fatalf("spam_patch is not JSON: %v", err)
	}
	for _, k := range []string{"scoreSpam", "scoreReject", "scoreDiscard"} {
		if _, ok := patch[k]; ok {
			t.Errorf("install.sh spam_patch sets %s; the panel owns it", k)
		}
	}
	for k, want := range map[string]any{
		"enable":             true,
		"trustContacts":      true,
		"trustReplies":       true,
		"spamFilterRulesUrl": "file:///opt/stalwart/share/spam-filter-rules.json.gz",
	} {
		if patch[k] != want {
			t.Errorf("install.sh spam_patch %s = %v, want %v", k, patch[k], want)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "install.sh")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("install.sh not found above the package")
		}
		dir = parent
	}
}

func readFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func num(t *testing.T, in []byte, key string) float64 {
	t.Helper()
	m := regexp.MustCompile(`"` + key + `"\s*:\s*(-?[0-9.]+)`).FindSubmatch(in)
	if m == nil {
		t.Fatalf("no %s in %s", key, in)
	}
	return parse(t, string(m[1]))
}

func colDefault(t *testing.T, sql []byte, col string) float64 {
	t.Helper()
	m := regexp.MustCompile(`(?i)` + col + `\s+DOUBLE\s+NOT\s+NULL\s+DEFAULT\s+(-?[0-9.]+)`).FindSubmatch(sql)
	if m == nil {
		t.Fatalf("migration: no `%s DOUBLE NOT NULL DEFAULT <n>`", col)
	}
	return parse(t, string(m[1]))
}

func tagDefault(t *testing.T, typ reflect.Type, field string) float64 {
	t.Helper()
	f, ok := typ.FieldByName(field)
	if !ok {
		t.Fatalf("models.ServerSettings has no %s", field)
	}
	m := regexp.MustCompile(`default:(-?[0-9.]+)`).FindStringSubmatch(f.Tag.Get("gorm"))
	if m == nil {
		t.Fatalf("%s gorm tag has no default: %q", field, f.Tag.Get("gorm"))
	}
	return parse(t, m[1])
}

func parse(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
