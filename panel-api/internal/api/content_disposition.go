package api

import (
	"fmt"
	"path/filepath"
	"strings"
)

// contentDisposition builds an RFC 6266-safe Content-Disposition header value
// for a user-controlled filename (JAB-108). Linux filenames legally contain
// `"` `;` and control characters; concatenating one straight into
// `filename="..."` lets it break out of the quoted value and spoof the saved
// name. We defend on two fronts:
//
//   - an ASCII-only `filename=` legacy fallback with `"` `\` control chars and
//     non-ASCII bytes replaced by `_`, so it can never break out of the quotes;
//   - an RFC 5987 extended `filename*` parameter (UTF-8, percent-encoded)
//     carrying the true name, which modern browsers prefer and round-trips.
//
// dispType is "attachment" or "inline".
func contentDisposition(dispType, filename string) string {
	// Defense in depth: never let a path (separators / traversal) through.
	filename = filepath.Base(filename)
	if filename == "." || filename == string(filepath.Separator) || filename == "" {
		filename = "download"
	}

	// ASCII fallback: keep printable ASCII; replace the quoting-relevant and
	// non-representable runes with '_' so the quoted value is unambiguous.
	var ascii strings.Builder
	for _, r := range filename {
		switch {
		case r < 0x20 || r == 0x7f: // C0 controls + DEL
			ascii.WriteByte('_')
		case r == '"' || r == '\\':
			ascii.WriteByte('_')
		case r > 0x7f: // non-ASCII: not representable in the legacy param
			ascii.WriteByte('_')
		default:
			ascii.WriteRune(r)
		}
	}
	fallback := ascii.String()
	if fallback == "" {
		fallback = "download"
	}

	return fmt.Sprintf(
		`%s; filename="%s"; filename*=UTF-8''%s`,
		dispType, fallback, rfc5987Encode(filename),
	)
}

// rfc5987Encode percent-encodes a string per RFC 5987 (ext-value), encoding
// every byte that is not an attr-char. It operates on raw UTF-8 bytes so
// multibyte runes are encoded byte-by-byte, as the RFC requires.
func rfc5987Encode(s string) string {
	const attrChars = "!#$&+-.^_`|~" // plus ALPHA / DIGIT, handled below
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || strings.IndexByte(attrChars, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
