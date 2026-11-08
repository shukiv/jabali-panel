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

	"github.com/gin-gonic/gin"
)

// kratosAccountField is the body field that names the account a login or
// recovery POST is for. The second-factor login steps and the recovery code
// step carry none.
var kratosAccountField = map[string]string{"login": "identifier", "recovery": "email"}

// kratosAccountKey returns the bucket key of the account a login or recovery
// POST names, "" when it names none. The body is put back for the proxy.
//
// A body it can't read for the account is refused (status != 0, with the
// error code) rather than passed on without the account budget: Kratos must
// not act on a name this limiter didn't see. The panel UI posts JSON only.
// Two different names in one body are refused for the same reason, since
// which one Kratos takes is up to its decoder.
func kratosAccountKey(c *gin.Context, flow string) (key string, status int, code string) {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return "", 0, ""
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, DefaultBodyLimitBytes+1))
	if err != nil {
		return "", http.StatusBadRequest, "invalid_request"
	}
	if int64(len(body)) > DefaultBodyLimitBytes {
		return "", http.StatusRequestEntityTooLarge, "request_body_too_large"
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) == 0 {
		return "", 0, ""
	}
	if mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type")); err != nil || mt != "application/json" {
		return "", http.StatusUnsupportedMediaType, "unsupported_content_type"
	}
	values, err := topLevelStrings(body, kratosAccountField[flow])
	if err != nil {
		return "", http.StatusBadRequest, "invalid_request"
	}
	name := ""
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if name != "" && v != name {
			return "", http.StatusBadRequest, "invalid_request"
		}
		name = v
	}
	if name == "" {
		return "", 0, ""
	}
	// Hashed: the key is held in memory and logged, and the name is
	// whatever the client sent.
	sum := sha256.Sum256([]byte(name))
	return flow + ":" + hex.EncodeToString(sum[:16]), 0, ""
}

var errNotJSONObject = errors.New("not a single JSON object")

// topLevelStrings returns the string values of every top-level key of the
// JSON object body that matches field, ignoring case as encoding/json does
// when it decodes into a struct.
func topLevelStrings(body []byte, field string) ([]string, error) {
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
		var s string
		if strings.EqualFold(key, field) && json.Unmarshal(v, &s) == nil {
			out = append(out, s)
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
