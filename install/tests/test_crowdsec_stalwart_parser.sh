#!/usr/bin/env bash
# install/tests/test_crowdsec_stalwart_parser.sh
#
# The CrowdSec parser jabali/stalwart-logs must read Stalwart's log lines
# whatever the order of the fields after `(event)`. Stalwart 0.16 writes an
# auth failure as `listenerId, localPort, remoteIp, remotePort, details,
# accountName, ...`; 1.0 writes `listenerId, localPort, remotePort, reason,
# accountName, causedBy, remoteIp, id`. The old single pattern wanted
# `localPort, remoteIp, remotePort` in that order, so no 1.0 auth failure
# parsed, and it never captured accountName (the user-enumeration scenario
# counts distinct account names).
#
# The client address (what every scenario bans) must come only from where
# Stalwart puts it, never from a quoted value a client chose (login name, EHLO
# domain, sender address): with such values shaped like extra keys, the line
# keeps the real address or is dropped.
#
# This runs the parser's node tree over real line shapes with a small grok
# evaluator (Python regexes for the grok macros the parser uses). The tree was
# also checked with `cscli explain` on a test box, on 0.16.27 and 1.0.2 lines.
#
# Run from repo root:
#     bash install/tests/test_crowdsec_stalwart_parser.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

python3 - install/crowdsec/stalwart/parsers/s01-parse/jabali-stalwart-logs.yaml <<'PY'
import re, sys
import yaml

doc = yaml.safe_load(open(sys.argv[1]))

MACROS = {
    "TIMESTAMP_ISO8601": r"\d{4}-\d{2}-\d{2}[T ]\d{2}:?\d{2}(?::?\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?",
    "LOGLEVEL": r"(?:TRACE|DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL)",
    "DATA": r".*?",
    "GREEDYDATA": r".*",
    "INT": r"[+-]?\d+",
    "IP": r"(?:\d{1,3}(?:\.\d{1,3}){3}|[0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){2,7})",
}


def collect_syntax(node):
    for k, v in (node.get("pattern_syntax") or {}).items():
        MACROS[k] = v
    for c in node.get("nodes") or []:
        collect_syntax(c)


def expand(p, depth=0):
    # Macros may use other macros (STALWART_FRONT_IP holds %{IP:ip_front}).
    if depth > 10:
        raise ValueError("grok macro nesting too deep")

    def sub(m):
        name, field = m.group(1), m.group(2)
        body = expand(MACROS[name], depth + 1)
        return "(?P<%s>%s)" % (field, body) if field else "(?:%s)" % body
    return re.sub(r"%\{(\w+)(?::(\w+))?\}", sub, p)


def compile_grok(p):
    return re.compile(expand(p))


def run(node, line, parsed):
    """CrowdSec node semantics, reduced to what this parser uses: a node fails
    when its grok does not match; a node without a grok succeeds when one of its
    children does; `onsuccess: next_stage` stops at the first successful child."""
    own = False
    g = node.get("grok")
    if g:
        text = line if g["apply_on"] == "Line.Raw" else parsed.get(g["apply_on"], "")
        m = compile_grok(g["pattern"]).search(text)
        if not m:
            return False
        parsed.update({k: v for k, v in m.groupdict().items() if v is not None})
        own = True
    kids = node.get("nodes") or []
    if not kids:
        return True
    any_ok = False
    for k in kids:
        if run(k, line, parsed):
            any_ok = True
            if node.get("onsuccess") == "next_stage":
                break
    return own or any_ok


collect_syntax(doc)

V016 = ('2026-11-03T00:45:45Z DEBUG Authentication failed (auth.failed) listenerId = "imaps", localPort = 993, '
        'remoteIp = 203.0.113.5, remotePort = 47620, details = "SQL login query did not return an account", '
        'accountName = "nobody-drill@mx.example.com", causedBy = "crates/directory/src/core/dispatch.rs:19", '
        'remoteIp = 203.0.113.5, id = "OEBJ1"')
V10 = ('2026-11-03T00:24:07Z DEBUG Authentication failed (auth.failed) listenerId = "imaps", localPort = 993, '
       'remotePort = 37166, reason = "SQL login query did not return an account", '
       'accountName = "nobody-drill@mx.example.com", causedBy = "crates/directory/src/core/dispatch.rs:27", '
       'remoteIp = 203.0.113.6, id = "KCFB1"')
AUTH = {"event": "auth.failed", "log_level": "DEBUG", "listenerId": "imaps", "localPort": "993",
        "accountName": "nobody-drill@mx.example.com"}
CASES = [
    ("0.16 auth failure", V016, dict(AUTH, remoteIp="203.0.113.5", remotePort="47620",
                                     details="SQL login query did not return an account")),
    ("1.0 auth failure", V10, dict(AUTH, remoteIp="203.0.113.6", remotePort="37166")),
    ("journal-prefixed 0.16 auth failure", "Nov 03 00:45:45 mx.example.com stalwart[2649425]: " + V016,
     dict(AUTH, remoteIp="203.0.113.5", remotePort="47620")),
    ("SMTP invalid EHLO", '2026-11-03T00:30:00Z DEBUG Invalid EHLO (smtp.invalid-ehlo) listenerId = "smtp-25", '
     'localPort = 25, remoteIp = 203.0.113.7, remotePort = 5555, domain = "x"',
     {"event": "smtp.invalid-ehlo", "listenerId": "smtp-25", "remoteIp": "203.0.113.7", "domain": "x"}),
    # A line without a client address is dropped: every scenario groups by it.
    ("delivery line without a client address",
     '2026-11-03T00:26:26Z DEBUG Delivery completed (delivery.completed) queueId = 1, queueName = "local"', None),
    # Client-chosen values shaped like Stalwart's own keys.
    ("0.16 auth failure, login name carrying a second address",
     V016.replace('accountName = "nobody-drill@mx.example.com"',
                  'accountName = "q", remoteIp = 192.0.2.99, z = "w@mx.example.com"'),
     {"event": "auth.failed", "remoteIp": "203.0.113.5"}),
    ("1.0 auth failure, login name carrying an address and an id",
     V10.replace('accountName = "nobody-drill@mx.example.com"',
                 'accountName = "q", remoteIp = 192.0.2.99, id = "ZZ1"'),
     {"event": "auth.failed", "remoteIp": "203.0.113.6"}),
    ("EHLO domain carrying an address and an id at the end of the line",
     '2026-11-03T00:30:00Z DEBUG Invalid EHLO (smtp.invalid-ehlo) listenerId = "smtp-25", localPort = 25, '
     'remoteIp = 203.0.113.7, remotePort = 5555, domain = "x", remoteIp = 192.0.2.66, id = "A"',
     {"event": "smtp.invalid-ehlo", "remoteIp": "203.0.113.7"}),
    # As 1.0.2 logs them (it escapes quotes and newlines inside values).
    ("1.0 auth failure, login name with escaped quotes and newlines",
     r'2026-11-03T01:23:00Z DEBUG Authentication failed (auth.failed) listenerId = "imaps", localPort = 993, '
     r'remotePort = 43166, reason = "SQL login query did not return an account", '
     r'accountName = "nl\nx, remoteIp = 192.0.2.98, id = \"ZZ1\"\nw@mx.example.com", '
     r'causedBy = "crates/directory/src/core/dispatch.rs:27", remoteIp = 203.0.113.6, id = "IDFH1"',
     {"event": "auth.failed", "remoteIp": "203.0.113.6"}),
    ("1.0 too many attempts",
     '2026-11-03T01:23:01Z WARN Too many authentication attempts (auth.too-many-attempts) listenerId = "imaps", '
     'localPort = 993, remoteIp = 203.0.113.8, remotePort = 43186, causedBy = Authentication failed (auth.failed) '
     '{ reason = "SQL login query did not return an account", accountName = "tm3@mx.example.com", '
     'causedBy = "crates/directory/src/core/dispatch.rs:27", remoteIp = 203.0.113.8 }, id = "FMDB4"',
     {"event": "auth.too-many-attempts", "remoteIp": "203.0.113.8", "log_level": "WARN"}),
    ("MAIL FROM carrying a whole auth-failure header",
     '2026-11-03T01:10:00Z DEBUG Delivery completed (delivery.completed) queueId = 338450106294468608, '
     'queueName = "local", from = "x (auth.failed) listenerId = "imaps", localPort = 993, remoteIp = 192.0.2.66, '
     'remotePort = 1, y"@evil.example", to = ["postmaster@mx.example.com"], size = 112, total = 1, elapsed = 0ms', None),
    ("line ending in client text shaped like the auth-failure tail",
     '2026-11-03T00:26:26Z DEBUG Delivery completed (delivery.completed) queueId = 1, from = "a", '
     'remoteIp = 192.0.2.66, id = "B"', None),
]

PICK = re.compile(r'^evt\.Parsed\.(\w+) != "" \? evt\.Parsed\.\1 : evt\.Parsed\.(\w+)$')
COPY = re.compile(r'^evt\.Parsed\.(\w+)$')


def apply_parsed_statics(parsed):
    """The parser's `parsed:` statics, in order, for the two expression forms
    it uses: `evt.Parsed.a != "" ? evt.Parsed.a : evt.Parsed.b` and
    `evt.Parsed.a`. A missing field reads as "", as in CrowdSec."""
    for s in doc.get("statics") or []:
        if not s.get("parsed"):
            continue
        e = (s.get("expression") or "").strip()
        m = PICK.match(e)
        if m:
            parsed[s["parsed"]] = parsed.get(m.group(1)) or parsed.get(m.group(2), "")
        elif COPY.match(e):
            parsed[s["parsed"]] = parsed.get(COPY.match(e).group(1), "")


fail = []
for label, line, want in CASES:
    parsed = {}
    ok = run(doc, line, parsed)
    if ok:
        apply_parsed_statics(parsed)
    if want is None:
        if ok:
            fail.append("%s: parsed, want dropped (%r)" % (label, parsed))
        continue
    if not ok:
        fail.append("%s: not parsed" % label)
        continue
    for k, v in want.items():
        if parsed.get(k) != v:
            fail.append("%s: %s = %r, want %r" % (label, k, parsed.get(k), v))

# The root must stay next_stage: with `continue` CrowdSec never hands the event
# to the next stage. Below it, a next_stage node would stop at its first field.
if doc.get("onsuccess") != "next_stage":
    fail.append("root onsuccess is %r, want next_stage" % doc.get("onsuccess"))


def inner(node):
    for c in node.get("nodes") or []:
        if c.get("onsuccess") == "next_stage":
            fail.append("a child node has onsuccess: next_stage; the field nodes after it would never run")
        inner(c)


inner(doc)

# The scenarios key on these. The two merges come first: the statics after
# them read the merged values.
st = doc.get("statics") or []
merges = [(s.get("parsed"), s.get("expression")) for s in st[:2]]
want_merges = [("event", 'evt.Parsed.event != "" ? evt.Parsed.event : evt.Parsed.event_tail'),
               ("remoteIp", 'evt.Parsed.ip_front != "" ? evt.Parsed.ip_front : evt.Parsed.ip_tail')]
if merges != want_merges:
    fail.append("first two statics are %r, want %r" % (merges, want_merges))
statics = {s.get("meta") or s.get("parsed"): s.get("expression") for s in st}
for meta, expr in (("source_ip", "evt.Parsed.remoteIp"), ("account_name", "evt.Parsed.accountName"),
                   ("log_type", "evt.Parsed.event")):
    if statics.get(meta) != expr:
        fail.append("static %s is %r, want %r" % (meta, statics.get(meta), expr))
# Only the root's own pattern may set the address: a child grok capturing it
# could read it out of client text.
def no_child_ip(node):
    for c in node.get("nodes") or []:
        p = (c.get("grok") or {}).get("pattern", "")
        if re.search(r"%\{IP:", p) or "remoteIp" in re.findall(r"%\{\w+:(\w+)\}", p):
            fail.append("a child node captures the client address: %s" % p)
        no_child_ip(c)


no_child_ip(doc)

for f in fail:
    print("FAIL: " + f)
if fail:
    sys.exit(1)
print("PASS: test_crowdsec_stalwart_parser")
PY
