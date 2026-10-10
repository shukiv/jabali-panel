// Package mailspam is the one definition of the mail server's spam score
// thresholds (GH #2017): what the panel stores (Scores, kept in
// server_settings), how they are checked (Validate), and the fields of
// Stalwart's SpamSettings object they become (Payload).
//
// Stalwart scores every message it receives. At or above the junk threshold
// the message goes to the Junk folder; at or above the reject threshold it is
// refused at SMTP time; at or above the discard threshold it is dropped.
// Stalwart checks reject before discard, and treats a reject or discard
// threshold of 0 as off (spam-filter/src/analysis/score.rs, 0.16.24).
//
// The settings PATCH handler and stalwartadmin.SpamScores run the same
// checks. Stalwart itself only bounds each value to -100..100; it accepted a
// junk threshold above the reject threshold on the .60 test box.
package mailspam

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// StalwartType and StalwartID name Stalwart's SpamSettings singleton.
const (
	StalwartType = "SpamSettings"
	StalwartID   = "singleton"
)

// MaxScore is the largest threshold Stalwart accepts (validationFailed,
// MaxValue 100, on the .60 test box).
const MaxScore = 100.0

// Scores are the three thresholds. Reject and Discard are 0 when off.
type Scores struct {
	Junk    float64 `json:"spam_junk_score"`
	Reject  float64 `json:"spam_reject_score"`
	Discard float64 `json:"spam_discard_score"`
}

// Default is what install.sh applied before the panel owned the thresholds,
// and what the apply plan, the migration and the model still start from.
var Default = Scores{Junk: 5, Reject: 15, Discard: 20}

// Validate checks the three together: Junk above 0 and at most MaxScore;
// Reject and Discard each 0 (off) or above Junk and at most MaxScore. A
// Discard at or above Reject is allowed, though Stalwart rejects such a
// message before it could be discarded.
func (s Scores) Validate() error {
	if !finite(s.Junk) || s.Junk <= 0 || s.Junk > MaxScore {
		return fmt.Errorf("spam_junk_score must be above 0 and at most %g", MaxScore)
	}
	if err := checkOptional("spam_reject_score", s.Reject, s.Junk); err != nil {
		return err
	}
	return checkOptional("spam_discard_score", s.Discard, s.Junk)
}

func checkOptional(field string, v, junk float64) error {
	if !finite(v) {
		return fmt.Errorf("%s must be a number", field)
	}
	if v == 0 {
		return nil
	}
	if v <= junk || v > MaxScore {
		return fmt.Errorf("%s must be 0 (off), or above spam_junk_score (%g) and at most %g", field, junk, MaxScore)
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Payload is the SpamSettings update that sets the three thresholds and
// nothing else.
func (s Scores) Payload() map[string]any {
	return map[string]any{
		"scoreSpam":    s.Junk,
		"scoreReject":  s.Reject,
		"scoreDiscard": s.Discard,
	}
}

// equalTolerance absorbs a float round trip through JSON and Stalwart.
const equalTolerance = 1e-6

// Equal reports whether two sets of thresholds are the same.
func (s Scores) Equal(o Scores) bool {
	return math.Abs(s.Junk-o.Junk) < equalTolerance &&
		math.Abs(s.Reject-o.Reject) < equalTolerance &&
		math.Abs(s.Discard-o.Discard) < equalTolerance
}

// FromStalwart reads the thresholds from a SpamSettings object as
// x:SpamSettings/get returns it. A missing field is an error, not a zero: a
// zero would read as "reject off".
func FromStalwart(raw json.RawMessage) (Scores, error) {
	var o struct {
		ScoreSpam    *float64 `json:"scoreSpam"`
		ScoreReject  *float64 `json:"scoreReject"`
		ScoreDiscard *float64 `json:"scoreDiscard"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return Scores{}, fmt.Errorf("mailspam: parse SpamSettings: %w", err)
	}
	if o.ScoreSpam == nil || o.ScoreReject == nil || o.ScoreDiscard == nil {
		return Scores{}, errors.New("mailspam: SpamSettings has no scoreSpam, scoreReject or scoreDiscard")
	}
	return Scores{Junk: *o.ScoreSpam, Reject: *o.ScoreReject, Discard: *o.ScoreDiscard}, nil
}
