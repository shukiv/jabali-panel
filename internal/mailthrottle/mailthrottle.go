// Package mailthrottle is the one definition of an outbound mail throttle as
// it crosses the panel↔agent boundary and lands in Stalwart.
//
// The panel keeps throttle policy in mail_outbound_policy and decides which
// Stalwart MtaOutboundThrottle objects should exist. It cannot reach
// Stalwart's admin API itself: the admin credential in
// /etc/jabali-panel/stalwart.env is not readable by the panel user
// (JAB-357). So the panel sends an ApplyRequest or DeleteRequest to the
// agent's mail.throttle.apply / mail.throttle.delete verbs, and the agent
// builds the Stalwart object with Payload.
//
// Both sides import this package, so the wire shape cannot drift. The agent
// runs Validate again because ScopeRef ends up inside a Stalwart expression,
// and the agent is the privilege boundary.
package mailthrottle

import (
	"errors"
	"fmt"
	"regexp"
)

// Agent verbs.
const (
	VerbApply  = "mail.throttle.apply"
	VerbDelete = "mail.throttle.delete"
	VerbList   = "mail.throttle.list"
)

// OwnedPrefix starts the description of every throttle the panel creates.
// The reconciler removes a Stalwart throttle with this prefix that no
// mail_outbound_policy row references, so an operator's own throttles must
// not start with it.
const OwnedPrefix = "jabali "

// StalwartType is the only Stalwart object type the verbs touch.
const StalwartType = "MtaOutboundThrottle"

// Scopes. Same values as models.OutboundScope*.
const (
	ScopeGlobal = "global"
	ScopeUser   = "user"
	ScopeDomain = "domain"
)

// Windows. One mail_outbound_policy row owns up to one Stalwart throttle
// per window.
const (
	WindowHour = "hour"
	WindowDay  = "day"
)

// ApplyRequest asks the agent to make one throttle window exist in Stalwart.
type ApplyRequest struct {
	// StalwartID is the object this window already owns, if any. Empty
	// means create. If Stalwart no longer has the object, the agent creates
	// a new one and returns its id.
	StalwartID string `json:"stalwart_id,omitempty"`
	Scope      string `json:"scope"`
	// ScopeRef is the sender address (user scope) or sender domain (domain
	// scope) the cap applies to. Empty on a user or domain scope gives every
	// sender, or every sender domain, its own bucket. Must be empty on the
	// global scope.
	ScopeRef string `json:"scope_ref,omitempty"`
	Window   string `json:"window"`
	// Limit is the number of messages allowed per window. Must be > 0: a
	// window with no cap has no Stalwart object, so the panel deletes it.
	Limit uint64 `json:"limit"`
}

// ApplyResult is the agent's answer to ApplyRequest.
type ApplyResult struct {
	StalwartID string `json:"stalwart_id"`
	// Changed is false when Stalwart already held exactly this throttle.
	Changed bool `json:"changed"`
}

// DeleteRequest asks the agent to remove one throttle from Stalwart.
type DeleteRequest struct {
	StalwartID string `json:"stalwart_id"`
}

// DeleteResult is the agent's answer to DeleteRequest.
type DeleteResult struct {
	// Deleted is false when Stalwart had no such throttle (already gone).
	Deleted bool `json:"deleted"`
}

// ListResult is the agent's answer to VerbList (which takes no params):
// every MtaOutboundThrottle in Stalwart, the panel's and anyone else's.
type ListResult struct {
	Throttles []ListItem `json:"throttles"`
}

// ListItem is one Stalwart throttle.
type ListItem struct {
	StalwartID  string `json:"stalwart_id"`
	Description string `json:"description"`
}

var (
	domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	emailRe  = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]{1,64}@[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	idRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ValidScopeRef reports whether ref is a sender address (user scope) or a
// sender domain (domain scope) that is safe to embed in the Stalwart match
// expression. The expression is `sender == '<ref>'`, so neither pattern
// admits a quote, a backslash or a space.
func ValidScopeRef(scope, ref string) bool {
	switch scope {
	case ScopeUser:
		return emailRe.MatchString(ref)
	case ScopeDomain:
		return domainRe.MatchString(ref)
	}
	return false
}

// ValidStalwartID reports whether id looks like a Stalwart object id (a
// short alphanumeric token, e.g. "jg1nyykmahqa"). It is passed as an argv
// word, so nothing that could read as a flag or a path gets through.
func ValidStalwartID(id string) bool {
	return idRe.MatchString(id) && id[0] != '-'
}

// Validate checks every field before the agent builds anything from it.
func (r ApplyRequest) Validate() error {
	if r.StalwartID != "" && !ValidStalwartID(r.StalwartID) {
		return fmt.Errorf("invalid stalwart_id %q", r.StalwartID)
	}
	switch r.Scope {
	case ScopeGlobal:
		if r.ScopeRef != "" {
			return errors.New("scope_ref must be empty on the global scope")
		}
	case ScopeUser, ScopeDomain:
		if r.ScopeRef != "" && !ValidScopeRef(r.Scope, r.ScopeRef) {
			return fmt.Errorf("scope_ref %q is not a valid %s (email for user, domain for domain)", r.ScopeRef, r.Scope)
		}
	default:
		return fmt.Errorf("invalid scope %q (want global, user or domain)", r.Scope)
	}
	switch r.Window {
	case WindowHour, WindowDay:
	default:
		return fmt.Errorf("invalid window %q (want hour or day)", r.Window)
	}
	if r.Limit == 0 {
		return errors.New("limit must be greater than 0")
	}
	return nil
}

// Validate checks the id before the agent passes it to stalwart-cli.
func (r DeleteRequest) Validate() error {
	if !ValidStalwartID(r.StalwartID) {
		return fmt.Errorf("invalid stalwart_id %q", r.StalwartID)
	}
	return nil
}

// Throttle is Stalwart's MtaOutboundThrottle object, as `stalwart-cli
// create|update --json` takes it and `stalwart-cli get --json` returns it
// (verified on Stalwart with stalwart-cli 1.0.12).
type Throttle struct {
	Description string `json:"description"`
	Enable      bool   `json:"enable"`
	// Key is a set on the wire: {"sender": true}. Empty means one bucket
	// for all mail.
	Key   map[string]bool `json:"key"`
	Rate  Rate            `json:"rate"`
	Match Match           `json:"match"`
}

// Rate is the bucket. Period is in milliseconds.
type Rate struct {
	Count  uint64 `json:"count"`
	Period uint64 `json:"period"`
}

// Match is a Stalwart expression: the first rule whose If holds gives the
// value, otherwise Else. "true" applies the throttle, "false" skips it.
type Match struct {
	Match map[string]MatchRule `json:"match"`
	Else  string               `json:"else"`
}

// MatchRule is one entry of Match.Match.
type MatchRule struct {
	If   string `json:"if"`
	Then string `json:"then"`
}

// Stalwart MtaOutboundThrottleKey values.
const (
	keySender       = "sender"
	keySenderDomain = "senderDomain"
)

const (
	hourMillis = 3600 * 1000
	dayMillis  = 86400 * 1000
)

// Payload builds the Stalwart object for a request that passed Validate.
//
// A user scope keys the bucket by sender; with a ScopeRef it only fires for
// that sender. A domain scope does the same with the sender domain. The
// global scope has no key and always fires, so all mail shares one bucket.
func Payload(r ApplyRequest) Throttle {
	key := map[string]bool{}
	match := Match{Match: map[string]MatchRule{}, Else: "true"}
	switch r.Scope {
	case ScopeUser:
		key[keySender] = true
		if r.ScopeRef != "" {
			match = onlyWhen("sender == '" + r.ScopeRef + "'")
		}
	case ScopeDomain:
		key[keySenderDomain] = true
		if r.ScopeRef != "" {
			match = onlyWhen("sender_domain == '" + r.ScopeRef + "'")
		}
	}
	period := uint64(hourMillis)
	if r.Window == WindowDay {
		period = dayMillis
	}
	return Throttle{
		Description: Description(r),
		Enable:      true,
		Key:         key,
		Rate:        Rate{Count: r.Limit, Period: period},
		Match:       match,
	}
}

func onlyWhen(cond string) Match {
	return Match{
		Match: map[string]MatchRule{"0": {If: cond, Then: "true"}},
		Else:  "false",
	}
}

// Description labels the object in Stalwart's WebAdmin so an operator can
// tell which panel row made it.
func Description(r ApplyRequest) string {
	ref := r.ScopeRef
	if ref == "" {
		ref = "*"
	}
	return fmt.Sprintf("%s%s %s: %d per %s", OwnedPrefix, r.Scope, ref, r.Limit, r.Window)
}

// Equal reports whether two throttles are the same object content. A nil
// and an empty map are equal, because Stalwart may leave an empty set out.
func (t Throttle) Equal(o Throttle) bool {
	if t.Description != o.Description || t.Enable != o.Enable || t.Rate != o.Rate || t.Match.Else != o.Match.Else {
		return false
	}
	if len(t.Key) != len(o.Key) || len(t.Match.Match) != len(o.Match.Match) {
		return false
	}
	for k, v := range t.Key {
		if o.Key[k] != v {
			return false
		}
	}
	for k, v := range t.Match.Match {
		if ov, ok := o.Match.Match[k]; !ok || ov != v {
			return false
		}
	}
	return true
}
