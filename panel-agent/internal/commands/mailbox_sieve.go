package commands

import (
	"fmt"
	"regexp"
	"strings"
)

// mailbox_sieve.go — builds the single active "jabali-managed" Sieve script
// for a mailbox (GH #1795).
//
// WHY this exists: Stalwart's delivery engine only executes a mailbox's active
// standard SieveScript (RFC 9661). jabali previously wrote external forwards to
// the `x:SieveUserScript` extension store, which Stalwart NEVER runs at
// delivery, so forwards silently no-oped on every box (proven on a test box:
// an active x:SieveUserScript redirect produced no outbound delivery, while the
// identical redirect in a standard SieveScript fired). The autoresponder used
// the native VacationResponse object, which Stalwart compiles into a standard
// SieveScript named "vacation". Stalwart allows exactly ONE active script per
// account, so a forward and an autoresponder cannot be two independent scripts —
// activating one deactivates the other. jabali therefore owns ONE composite
// script that carries both the forward `redirect`s and the `vacation` action.

// managedForward is one external forward target for the composite script.
// KeepCopy → `redirect :copy` (also deliver a copy to the mailbox); a plain
// `redirect` forwards without keeping a local copy.
type managedForward struct {
	Target   string
	KeepCopy bool
}

// managedAutoresponder is the mailbox's autoresponder (vacation) desired state.
// Dates are RFC 3339 strings (or nil for "no bound"); the bodies are optional.
type managedAutoresponder struct {
	Enabled  bool
	FromDate *string
	ToDate   *string
	Subject  *string
	TextBody *string
	HTMLBody *string
}

// managedScriptName is the fixed name of the single jabali-owned active script.
const managedScriptName = "jabali-managed"

// redirectTargetRe validates a forward target before it is embedded in a Sieve
// string literal. A bare addr-spec only: no whitespace, quotes, backslashes,
// or Sieve control characters that could break out of the quoted string (a
// target like `x@y";redirect "attacker@z` must never reach the script). This is
// deliberately stricter than RFC 5321 — jabali only ever stores plain
// local@domain forward targets.
var redirectTargetRe = regexp.MustCompile(`^[^\s"\\;{}@]+@[^\s"\\;{}@]+$`)

// sieveQuote renders s as a Sieve quoted string, escaping the only two
// characters that are special inside one: backslash and double-quote (RFC 5228
// §2.4.2). Applied to every operator-supplied value (subject, body) so a stray
// quote can neither corrupt the script nor inject a new command.
func sieveQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// buildManagedSieve renders the single active jabali-managed Sieve script from a
// mailbox's forwards and autoresponder. It returns "" when there is nothing to
// apply (no forwards and no enabled autoresponder) — the caller destroys the
// script rather than activating an empty one. An invalid forward target is a
// hard error (fail closed): a malformed target must never reach the ruleset.
//
// The rendered shape mirrors what Stalwart itself generates for a
// VacationResponse (`vacation :mime :subject ... "<mime entity>"`), so the
// autoresponder behaves identically to the native object it replaces.
func buildManagedSieve(fwds []managedForward, ar managedAutoresponder) (string, error) {
	arActive := ar.Enabled && (ar.TextBody != nil || ar.HTMLBody != nil)
	if len(fwds) == 0 && !arActive {
		return "", nil
	}

	// require line — only the extensions actually used.
	reqs := []string{}
	anyCopy := false
	for _, f := range fwds {
		if f.KeepCopy {
			anyCopy = true
			break
		}
	}
	if anyCopy {
		reqs = append(reqs, "copy")
	}
	if arActive {
		reqs = append(reqs, "vacation", "relational", "date")
	}

	var b strings.Builder
	if len(reqs) > 0 {
		quoted := make([]string, len(reqs))
		for i, r := range reqs {
			quoted[i] = sieveQuote(r)
		}
		fmt.Fprintf(&b, "require [%s];\n\n", strings.Join(quoted, ", "))
	}

	for _, f := range fwds {
		t := strings.TrimSpace(f.Target)
		if !redirectTargetRe.MatchString(t) {
			return "", fmt.Errorf("invalid forward target %q", f.Target)
		}
		if f.KeepCopy {
			fmt.Fprintf(&b, "redirect :copy %s;\n", sieveQuote(t))
		} else {
			fmt.Fprintf(&b, "redirect %s;\n", sieveQuote(t))
		}
	}

	if arActive {
		b.WriteString(buildVacationBlock(ar))
	}

	return b.String(), nil
}

// buildVacationBlock renders the `vacation` action, wrapped in a currentdate
// window guard when a from/to bound is set. Only the bounds actually present
// are emitted (RFC 5260 date + relational).
func buildVacationBlock(ar managedAutoresponder) string {
	action := vacationAction(ar)

	var guards []string
	if ar.FromDate != nil {
		guards = append(guards, fmt.Sprintf(`currentdate :value "ge" "iso8601" %s`, sieveQuote(*ar.FromDate)))
	}
	if ar.ToDate != nil {
		guards = append(guards, fmt.Sprintf(`currentdate :value "le" "iso8601" %s`, sieveQuote(*ar.ToDate)))
	}

	if len(guards) == 0 {
		return action + "\n"
	}
	var test string
	if len(guards) == 1 {
		test = guards[0]
	} else {
		test = "allof(" + strings.Join(guards, ", ") + ")"
	}
	return fmt.Sprintf("if %s {\n    %s\n}\n", test, action)
}

// vacationAction renders `vacation :mime :subject "..." "<mime entity>"`. The
// reason is a full MIME body (:mime), matching Stalwart's own VacationResponse
// output so a plain-text or HTML autoresponder renders correctly in clients.
// HTMLBody wins when both are present (mirrors the client precedence).
func vacationAction(ar managedAutoresponder) string {
	ctype, body := "text/plain", ""
	if ar.HTMLBody != nil && *ar.HTMLBody != "" {
		ctype = "text/html"
		body = *ar.HTMLBody
	} else if ar.TextBody != nil {
		body = *ar.TextBody
	}
	mime := fmt.Sprintf("Content-Type: %s; charset=\"utf-8\"\nContent-Transfer-Encoding: 8bit\n\n%s", ctype, body)

	var sb strings.Builder
	sb.WriteString("vacation :mime")
	if ar.Subject != nil && *ar.Subject != "" {
		fmt.Fprintf(&sb, " :subject %s", sieveQuote(*ar.Subject))
	}
	fmt.Fprintf(&sb, " %s;", sieveQuote(mime))
	return sb.String()
}
