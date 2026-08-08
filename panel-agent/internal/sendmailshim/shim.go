// Package sendmailshim implements the guts of the jabali-sendmail binary
// (JAB-230): a sendmail-compatible shim that PHP's mail() executes because
// install.sh purges every traditional MTA. It runs AS THE TENANT UID, picks a
// relay credential the uid can read, and submits the message to Stalwart on
// 127.0.0.1:587 with the envelope sender forced to the credential's identity
// (Stalwart rejects MAIL FROM ≠ authenticated identity with 501 5.5.4).
//
// Threat model: argv and stdin are fully attacker-controlled (any tenant PHP
// can exec the shim with arbitrary input). The From header is used ONLY to
// select among credential files the calling uid can already open — POSIX
// permissions enforce tenant isolation, never header contents. The shim
// writes no files and execs nothing.
package sendmailshim

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxMessageBytes caps the stdin read. PHP post_max_size defaults to 512M but
// a mail() body beyond 25 MiB is abuse, not email.
const MaxMessageBytes = 25 << 20

// Sysexits-style exit codes (sendmail convention; PHP only checks == 0).
const (
	ExitOK       = 0
	ExitUsage    = 64 // EX_USAGE — bad arguments
	ExitDataErr  = 65 // EX_DATAERR — unparsable/oversized message
	ExitNoPerm   = 77 // EX_NOPERM — authentication rejected
	ExitConfig   = 78 // EX_CONFIG — no credential available
	ExitTempFail = 75 // EX_TEMPFAIL — transient (connect/4xx) failure
)

// CodedError carries a sysexits code for main() to os.Exit with.
type CodedError struct {
	Code int
	Err  error
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

func coded(code int, format string, a ...any) error {
	return &CodedError{Code: code, Err: fmt.Errorf(format, a...)}
}

// ExitCode maps an error to its sysexits code (EX_TEMPFAIL for unknowns, so a
// transient bug retries rather than permanently bouncing tenant mail).
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ExitTempFail
}

// Options is the parsed argv.
type Options struct {
	ReadRecipientsFromHeaders bool     // -t
	EnvelopeFromHint          string   // -f / -r value (never trusted as the actual envelope)
	Recipients                []string // positional recipients
}

// ParseArgs parses a sendmail-style argv (excluding argv[0]). It is
// deliberately tolerant: unknown -o*/-O*/-B/-N/-V style options are ignored
// (real MTAs accept dozens; PHP and PHPMailer emit a few), because failing
// hard on a benign flag would break mail() for no security gain.
func ParseArgs(args []string) (Options, error) {
	var o Options
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break // first recipient
		}
		switch {
		case a == "-t":
			o.ReadRecipientsFromHeaders = true
		case a == "-i" || a == "-oi":
			// Read-to-EOF is our only mode; accepted for compatibility.
		case a == "-f" || a == "-r":
			if i+1 >= len(args) {
				return o, coded(ExitUsage, "%s requires an address", a)
			}
			i++
			o.EnvelopeFromHint = args[i]
		case strings.HasPrefix(a, "-f"):
			o.EnvelopeFromHint = a[2:]
		case strings.HasPrefix(a, "-r") && len(a) > 2:
			o.EnvelopeFromHint = a[2:]
		case a == "-F" || a == "-N" || a == "-V" || a == "-B" || a == "-X":
			// Option with a value we don't use — skip the value.
			if i+1 < len(args) {
				i++
			}
		default:
			// -o*, -O*, -q*, -v, ... — ignore.
		}
	}
	for ; i < len(args); i++ {
		if args[i] != "" {
			o.Recipients = append(o.Recipients, args[i])
		}
	}
	return o, nil
}

// Message is the parsed + sanitised message ready for submission.
type Message struct {
	// Raw is the message with every Bcc field stripped (Bcc recipients still
	// get the mail via the envelope; leaking the header would expose them).
	Raw []byte
	// FromDomain is the lowercased domain of the first From address, "" if
	// absent/unparsable. Used only to SELECT a credential file.
	FromDomain string
	// HasFrom reports whether a From header was present at all.
	HasFrom bool
	// FromAddr is the first From address (for Sender-header comparison).
	FromAddr string
	// HeaderRecipients are the To/Cc/Bcc addresses (only populated with -t).
	HeaderRecipients []string
}

// ParseMessage reads the whole message (capped), extracts what the shim
// needs, and strips Bcc. The remaining bytes are forwarded unmodified.
func ParseMessage(r io.Reader, wantHeaderRecipients bool) (*Message, error) {
	lim := io.LimitReader(r, MaxMessageBytes+1)
	raw, err := io.ReadAll(lim)
	if err != nil {
		return nil, coded(ExitDataErr, "read message: %v", err)
	}
	if len(raw) > MaxMessageBytes {
		return nil, coded(ExitDataErr, "message exceeds %d bytes", MaxMessageBytes)
	}

	headerBlock, body := splitHeaderBody(raw)
	fields := parseHeaderFields(headerBlock)

	msg := &Message{}
	var kept []headerField
	for _, f := range fields {
		name := strings.ToLower(f.name)
		switch name {
		case "bcc":
			if wantHeaderRecipients {
				msg.HeaderRecipients = append(msg.HeaderRecipients, parseAddrList(f.value)...)
			}
			continue // strip
		case "to", "cc":
			if wantHeaderRecipients {
				msg.HeaderRecipients = append(msg.HeaderRecipients, parseAddrList(f.value)...)
			}
		case "from":
			if !msg.HasFrom {
				if addr, perr := mail.ParseAddress(strings.TrimSpace(f.value)); perr == nil {
					msg.HasFrom = true
					msg.FromAddr = addr.Address
					if at := strings.LastIndexByte(addr.Address, '@'); at >= 0 {
						msg.FromDomain = strings.ToLower(addr.Address[at+1:])
					}
				}
			}
		}
		kept = append(kept, f)
	}

	var buf bytes.Buffer
	buf.Grow(len(raw))
	for _, f := range kept {
		buf.Write(f.raw)
	}
	buf.Write(body)
	msg.Raw = buf.Bytes()
	return msg, nil
}

// headerField is one header field including continuation lines, with raw
// bytes preserved so untouched fields are forwarded byte-identical.
type headerField struct {
	name  string
	value string
	raw   []byte
}

// splitHeaderBody splits at the first blank line. The body keeps its leading
// blank-line separator so reassembly is exact.
func splitHeaderBody(raw []byte) (header, body []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i+2], raw[i+2:]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i+1], raw[i+1:]
	}
	return raw, nil
}

func parseHeaderFields(block []byte) []headerField {
	var fields []headerField
	sc := bufio.NewScanner(bytes.NewReader(block))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var cur *headerField
	flush := func() {
		if cur != nil {
			fields = append(fields, *cur)
			cur = nil
		}
	}
	for sc.Scan() {
		line := sc.Bytes()
		rawLine := append(append([]byte{}, line...), '\n')
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && cur != nil {
			cur.value += " " + strings.TrimSpace(string(line))
			cur.raw = append(cur.raw, rawLine...)
			continue
		}
		flush()
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			// Not a header field — keep verbatim as an unnamed blob.
			fields = append(fields, headerField{raw: rawLine})
			continue
		}
		cur = &headerField{
			name:  string(bytes.TrimSpace(line[:colon])),
			value: strings.TrimSpace(string(line[colon+1:])),
			raw:   rawLine,
		}
	}
	flush()
	return fields
}

func parseAddrList(v string) []string {
	list, err := mail.ParseAddressList(v)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out
}

// Cred is a relay credential provisioned by the panel (root-written,
// 0640 root:<usergroup>).
type Cred struct {
	Email    string
	Password string
	// Host is the mail server's certificate hostname, used as the TLS SNI /
	// verification name when dialing 127.0.0.1 (never InsecureSkipVerify).
	Host string
}

// domainRe validates a From-header domain before it becomes a path element.
// The header is hostile input; anything not matching falls back to default.
var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// LoadCred selects the credential for a message: <dir>/<domain>.cred when the
// (validated) From domain has one readable by this uid, else
// <dir>/default.cred. dir is the per-user subtree, e.g.
// /etc/jabali-panel/sendmail/<user>.
func LoadCred(dir, fromDomain string) (*Cred, error) {
	var tried []string
	if fromDomain != "" && domainRe.MatchString(fromDomain) && !strings.Contains(fromDomain, "..") {
		p := filepath.Join(dir, fromDomain+".cred")
		if c, err := readCredFile(p); err == nil {
			return c, nil
		}
		tried = append(tried, p)
	}
	p := filepath.Join(dir, "default.cred")
	if c, err := readCredFile(p); err == nil {
		return c, nil
	}
	tried = append(tried, p)
	return nil, coded(ExitConfig, "no relay credential (tried %s) — the panel provisions these; is the domain fully set up?", strings.Join(tried, ", "))
}

func readCredFile(path string) (*Cred, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := &Cred{}
	sc := bufio.NewScanner(io.LimitReader(f, 64*1024))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "email":
			c.Email = strings.TrimSpace(v)
		case "password":
			c.Password = strings.TrimSpace(v)
		case "host":
			c.Host = strings.TrimSpace(v)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if c.Email == "" || c.Password == "" || c.Host == "" {
		return nil, fmt.Errorf("cred file %s incomplete", path)
	}
	return c, nil
}

// EnsureSender guarantees the outgoing header block is honest about the real
// submitter: when the message's From differs from the authenticated identity,
// a Sender: field with the credential address is prepended (RFC 5322 §3.6.2),
// and any tenant-supplied Sender is dropped (it would be a spoof vector).
// When there is no From at all, one is added — Stalwart and most receivers
// reject From-less mail outright.
func EnsureSender(msg *Message, credEmail string) []byte {
	raw := msg.Raw
	if strings.EqualFold(msg.FromAddr, credEmail) && msg.HasFrom {
		return raw
	}
	header, body := splitHeaderBody(raw)
	var buf bytes.Buffer
	buf.Grow(len(raw) + 128)
	if !msg.HasFrom {
		fmt.Fprintf(&buf, "From: <%s>\n", credEmail)
	}
	fmt.Fprintf(&buf, "Sender: <%s>\n", credEmail)
	for _, f := range parseHeaderFields(header) {
		if strings.EqualFold(f.name, "sender") {
			continue
		}
		buf.Write(f.raw)
	}
	buf.Write(body)
	return buf.Bytes()
}
