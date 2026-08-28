package commands

import (
	"bufio"
	"io"
	"strings"
)

// sanitizePgPlainDump streams a plain-format pg_dump from r to w, dropping the
// top-level ownership/role/privilege statements that a default `pg_dump` emits
// but the restore's unprivileged shadow loader cannot execute (GH #1044).
//
// A dump made by the panel's own backup already carries `--no-owner
// --no-privileges`, so it loads clean; a dump the tenant made themselves with a
// stock `pg_dump` includes `ALTER ... OWNER TO <role>`, `GRANT`/`REVOKE`, and
// (with --use-set-session-authorization) `SET SESSION AUTHORIZATION` — all of
// which fail under ON_ERROR_STOP when replayed as the non-superuser shadow role
// ("must be able to SET ROLE ..."), aborting the whole restore. The existing
// superuser post-pass reassigns ownership to the tenant role and re-applies the
// panel-tracked grants, so dropping these directives here loses nothing the
// restore did not already re-establish.
//
// The filter is deliberately structural, not a blind line grep: it never drops a
// line inside a `COPY ... FROM stdin` data block or inside a dollar-quoted body
// (a function source can legitimately contain the words GRANT / OWNER TO), which
// a naive sed would corrupt. Only a complete single-statement line (ends in `;`)
// at top level is eligible to be dropped — pg_dump emits every one of these
// targets on its own line, so a multi-line statement is never half-cut.
func sanitizePgPlainDump(r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 1<<20)
	bw := bufio.NewWriterSize(w, 1<<20)

	inCopy := false   // between `COPY ... FROM stdin;` and its terminating `\.`
	dollarTag := ""   // non-empty while inside a $tag$ ... $tag$ quoted body

	for {
		line, readErr := br.ReadString('\n')
		if len(line) > 0 {
			keep := true
			switch {
			case inCopy:
				// Raw COPY data — pass verbatim; the block ends at a line that
				// is exactly `\.`.
				if strings.TrimRight(line, "\r\n") == `\.` {
					inCopy = false
				}
			case dollarTag != "":
				// Inside a dollar-quoted body — pass verbatim, tracking the close.
				dollarTag = scanDollarQuote(line, dollarTag)
			default:
				trimmed := strings.TrimSpace(line)
				if isCopyFromStdin(trimmed) {
					inCopy = true
				} else if tag := scanDollarQuote(line, ""); tag != "" {
					// This line opens a dollar-quoted body that stays open — it
					// is part of a CREATE FUNCTION/DO block, never a droppable
					// ownership statement.
					dollarTag = tag
				} else if isDroppableDumpStmt(trimmed) {
					keep = false
				}
			}
			if keep {
				if _, err := bw.WriteString(line); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}
	return bw.Flush()
}

// isCopyFromStdin reports whether a top-level line begins a `COPY ... FROM stdin`
// data block (the only COPY form pg_dump emits for table data).
func isCopyFromStdin(trimmed string) bool {
	up := strings.ToUpper(trimmed)
	return strings.HasPrefix(up, "COPY ") &&
		strings.Contains(up, " FROM STDIN") &&
		strings.HasSuffix(strings.TrimRight(trimmed, " \t\r\n"), ";")
}

// isDroppableDumpStmt reports whether a complete top-level statement line is one
// of the ownership/role/privilege directives the shadow loader cannot run. Only
// single-line, semicolon-terminated statements qualify, so a multi-line
// statement is never partially removed.
func isDroppableDumpStmt(trimmed string) bool {
	if !strings.HasSuffix(strings.TrimRight(trimmed, " \t\r\n"), ";") {
		return false
	}
	up := strings.ToUpper(trimmed)
	switch {
	case strings.HasPrefix(up, "ALTER ") && strings.Contains(up, " OWNER TO "):
		return true
	case strings.HasPrefix(up, "GRANT "):
		return true
	case strings.HasPrefix(up, "REVOKE "):
		return true
	case strings.HasPrefix(up, "SET SESSION AUTHORIZATION"):
		return true
	case strings.HasPrefix(up, "RESET SESSION AUTHORIZATION"):
		return true
	case strings.HasPrefix(up, "SET ROLE "):
		return true
	default:
		return false
	}
}

// scanDollarQuote walks one line and returns the dollar-quote tag left open at
// end of line. openTag is the tag already open at line start ("" if none). A
// tag is `$` + [A-Za-z0-9_]* + `$` (so `$$` and `$body$`); a lone `$` with no
// closing `$` on the same run is treated as a literal, never as an opener.
func scanDollarQuote(line, openTag string) string {
	i := 0
	n := len(line)
	for i < n {
		if line[i] != '$' {
			i++
			continue
		}
		if openTag == "" {
			tag, ok := dollarTagAt(line, i)
			if ok {
				openTag = tag
				i += len(tag)
				continue
			}
			i++
		} else {
			// Looking for the matching close tag.
			if strings.HasPrefix(line[i:], openTag) {
				i += len(openTag)
				openTag = ""
				continue
			}
			i++
		}
	}
	return openTag
}

// dollarTagAt parses a dollar-quote delimiter starting at line[i] (which is '$').
// Returns the full delimiter (e.g. "$$" or "$func$") and true, or false if the
// run is not a complete delimiter.
func dollarTagAt(line string, i int) (string, bool) {
	j := i + 1
	for j < len(line) {
		c := line[j]
		if c == '$' {
			return line[i : j+1], true
		}
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", false
		}
		j++
	}
	return "", false
}
