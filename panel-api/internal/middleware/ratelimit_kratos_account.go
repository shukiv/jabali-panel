package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// kratosAccountFields are the body fields that name the account a login or
// recovery POST is for, Kratos's deprecated and recovery-v2 ones included.
// The second-factor login steps and the recovery code step carry none.
var kratosAccountFields = map[string][]string{
	"login":    {"identifier", "password_identifier"},
	"recovery": {"email", "recovery_address", "recovery_confirm_address"},
}

// signedInFor is how long an address that signed in to an account keeps its
// way in past that account's budget: Kratos's session lifespan.
const signedInFor = 30 * 24 * time.Hour

// kratosAccount is the account a login or recovery POST names: its bucket key,
// and a short hash of the name for the log. byPassword is a password login,
// the one method with which Kratos signs in the account the body names; a
// session from any other (a passkey, a second factor) may be another
// account's.
type kratosAccount struct {
	key, logHash string
	byPassword   bool
}

// kratosAccountOf reads the account a login or recovery POST names; the zero
// value when it names none. The body is put back for the proxy.
//
// A request it can't read for the account is refused (status != 0, with the
// error code) rather than passed on without the account budget: Kratos must
// not act on a name this limiter didn't see. The panel UI posts JSON only,
// with the flow alone in the query string. Two different names in one body
// are refused for the same reason, since which one Kratos takes is up to its
// decoder.
func kratosAccountOf(c *gin.Context, flow string) (acct kratosAccount, status int, code string) {
	fields := kratosAccountFields[flow]
	for k := range c.Request.URL.Query() {
		if matchesField(k, fields) {
			return kratosAccount{}, http.StatusBadRequest, "invalid_request"
		}
	}
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return kratosAccount{}, 0, ""
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, DefaultBodyLimitBytes+1))
	if err != nil {
		return kratosAccount{}, http.StatusBadRequest, "invalid_request"
	}
	if int64(len(body)) > DefaultBodyLimitBytes {
		return kratosAccount{}, http.StatusRequestEntityTooLarge, "request_body_too_large"
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) == 0 {
		return kratosAccount{}, 0, ""
	}
	if mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type")); err != nil || mt != "application/json" {
		return kratosAccount{}, http.StatusUnsupportedMediaType, "unsupported_content_type"
	}
	values, err := topLevelStrings(body, fields)
	if err != nil {
		return kratosAccount{}, http.StatusBadRequest, "invalid_request"
	}
	var name []byte
	for _, v := range values {
		k := foldAccountName(v)
		if len(k) == 0 {
			continue
		}
		if name != nil && !bytes.Equal(k, name) {
			return kratosAccount{}, http.StatusBadRequest, "invalid_request"
		}
		name = k
	}
	if name == nil {
		return kratosAccount{}, 0, ""
	}
	methods, err := topLevelStrings(body, []string{"method"})
	if err != nil {
		return kratosAccount{}, http.StatusBadRequest, "invalid_request"
	}
	byPassword := len(methods) > 0
	for _, m := range methods {
		byPassword = byPassword && m == "password"
	}
	// Hashed: the key is held in memory and logged, and the name is
	// whatever the client sent.
	sum := sha256.Sum256(name)
	h := hex.EncodeToString(sum[:16])
	return kratosAccount{key: flow + ":" + h, logHash: h[:12], byPassword: byPassword}, 0, ""
}

func matchesField(key string, fields []string) bool {
	for _, f := range fields {
		if strings.EqualFold(key, f) {
			return true
		}
	}
	return false
}

// accountNameCollators fold names at primary strength. A Collator isn't
// safe for concurrent use.
var accountNameCollators = sync.Pool{New: func() any { return collate.New(language.Und, collate.Loose) }}

// foldAccountName is name as Kratos's tables compare it, or nil when it is
// blank. Recovery addresses are utf8mb4_unicode_ci: case, accents and width
// don't count, and a trailing space doesn't either. Login identifiers are
// utf8mb4_bin, which tells more names apart, so folding them the same way
// only joins budgets, never splits one.
func foldAccountName(name string) []byte {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	col := accountNameCollators.Get().(*collate.Collator)
	defer accountNameCollators.Put(col)
	var buf collate.Buffer
	return bytes.Clone(col.KeyFromString(&buf, name))
}

var errNotJSONObject = errors.New("not a single JSON object")

// topLevelStrings returns the string values of every top-level key of the
// JSON object body that matches one of fields, ignoring case as encoding/json
// does when it decodes into a struct. A matching key whose value is neither a
// string nor null is an error: what Kratos makes of it is up to its decoder.
func topLevelStrings(body []byte, fields []string) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errNotJSONObject
	}
	var out []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if !matchesField(key, fields) {
			continue
		}
		var s *string
		if err := json.Unmarshal(v, &s); err != nil {
			return nil, err
		}
		if s != nil {
			out = append(out, *s)
		}
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errNotJSONObject
	}
	return out, nil
}

// setsKratosSession reports whether a response sets a Kratos session cookie,
// which Kratos does only for a login that succeeded.
func setsKratosSession(h http.Header) bool {
	for _, ck := range (&http.Response{Header: h}).Cookies() {
		if ck.Name == "ory_kratos_session" && ck.Value != "" && ck.MaxAge >= 0 &&
			(ck.Expires.IsZero() || ck.Expires.After(time.Now())) {
			return true
		}
	}
	return false
}

type signInKey struct {
	account, ip string
}

// hasSignedIn reports whether ip signed in to account within signedInFor.
func (l *RateLimiter) hasSignedIn(account, ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.signedIn[signInKey{account, ip}]
	return ok && time.Since(at) < signedInFor
}

func (l *RateLimiter) noteSignIn(account, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.signedIn[signInKey{account, ip}] = time.Now()
}
