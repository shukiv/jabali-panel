#!/usr/bin/env bash
# install/tests/test_stalwart_mail_elsewhere_converger.sh
#
# converge_stalwart_mail_elsewhere sets Stalwart's outbound route, queue
# schedule and RCPT script so a registered domain listed in the in-memory
# lookup "jabali-mail-elsewhere" (email off, or mail hosted by another
# provider) gets its mail sent to its MX, and unauthenticated mail for it is
# refused by the system Sieve script of the same name.
#
# Behavioural: extracts the function from install.sh and runs it against a
# fake stalwart-cli that keeps the objects in files.
#
# Run from repo root:
#     bash install/tests/test_stalwart_mail_elsewhere_converger.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
fn_src=$(awk '/^converge_stalwart_mail_elsewhere\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: converge_stalwart_mail_elsewhere() not found in install.sh"
  exit 1
fi
eval "$fn_src"

_log() { :; }
_ok() { :; }
_warn() { printf 'WARN %s\n' "$*" >>"$FAKE_DIR/warnings"; }

if ! grep -q '^  converge_stalwart_mail_elsewhere "\$jmap_port" "\$admin_token" || true$' install.sh; then
  echo "FAIL: install.sh never calls converge_stalwart_mail_elsewhere"
  fail=1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export FAKE_DIR="$tmp"

# Fake stalwart-cli. State: outbound.json, rcpt.json, sieve.json (a list).
# Every call's argv goes to calls.log, one JSON array per line.
cat >"$tmp/stalwart-cli" <<'EOF'
#!/usr/bin/env python3
import json, os, sys
d = os.environ["FAKE_DIR"]
args = sys.argv[1:]
with open(os.path.join(d, "calls.log"), "a") as f:
    f.write(json.dumps(args) + "\n")
if os.environ.get("FAKE_FAIL") and args[0] in ("create", "update"):
    sys.stderr.write("refused\n"); sys.exit(1)
def load(n): return json.load(open(os.path.join(d, n)))
def save(n, v): json.dump(v, open(os.path.join(d, n), "w"))
def body():
    assert "--stdin" in args, "payload not on stdin"
    b = json.loads(sys.stdin.read())
    # Stalwart 0.16 stores a Sieve script without its trailing newline.
    if isinstance(b.get("contents"), str):
        b["contents"] = b["contents"].rstrip("\n")
    return b
op, obj = args[0], args[1]
if op == "get" and obj == "MtaOutboundStrategy": print(json.dumps(load("outbound.json")))
elif op == "get" and obj == "MtaStageRcpt": print(json.dumps(load("rcpt.json")))
elif op == "query" and obj == "SieveSystemScript":
    for s in load("sieve.json"): print(json.dumps(s))
elif op == "create" and obj == "SieveSystemScript":
    s = load("sieve.json"); b = body(); b["id"] = "s%d" % len(s); s.append(b); save("sieve.json", s)
elif op == "update" and obj == "SieveSystemScript":
    s = load("sieve.json"); b = body()
    for x in s:
        if x["id"] == args[2]: x.update(b)
    save("sieve.json", s)
elif op == "update" and obj == "MtaStageRcpt":
    r = load("rcpt.json"); r.update(body()); save("rcpt.json", r)
elif op == "update" and obj == "MtaOutboundStrategy":
    o = load("outbound.json"); o.update(body()); save("outbound.json", o)
else:
    sys.stderr.write("fake: unexpected %r\n" % args); sys.exit(2)
EOF
chmod +x "$tmp/stalwart-cli"
export JABALI_STALWART_CLI="$tmp/stalwart-cli"

# Stalwart 0.16's shipped values (read from a fresh box).
shipped() {
  cat >"$tmp/outbound.json" <<'EOF'
{"connection":{"match":{},"else":"'default'"},"route":{"match":{"0":{"if":"is_local_domain(rcpt_domain)","then":"'local'"}},"else":"'mx'"},"schedule":{"match":{"0":{"if":"is_local_domain(rcpt_domain)","then":"'local'"},"1":{"if":"source == 'dsn'","then":"'dsn'"},"2":{"if":"source == 'report'","then":"'report'"}},"else":"'remote'"},"tls":{"match":{"0":{"if":"retry_num > 0 && last_error == 'tls'","then":"'invalid-tls'"}},"else":"'default'"},"id":"singleton"}
EOF
  cat >"$tmp/rcpt.json" <<'EOF'
{"maxFailures":{"match":{},"else":"5"},"allowRelaying":{"match":{},"else":"!is_empty(authenticated_as)"},"rewrite":{"match":{},"else":"false"},"script":{"match":{},"else":"false"},"id":"singleton"}
EOF
  echo '[{"id":"other","name":"jabali-something-else","contents":"keep","isActive":true}]' >"$tmp/sieve.json"
  rm -f "$tmp/calls.log" "$tmp/warnings"
}

q() { python3 -c "$1" "$tmp"; }

# 1. A shipped box gets the lookup-driven route, schedule and RCPT refusal.
shipped
if ! converge_stalwart_mail_elsewhere 8446 token; then
  echo "FAIL: shipped box: converger returned failure"
  fail=1
fi
check=$(q '
import json, sys
d = sys.argv[1]
o = json.load(open(d + "/outbound.json")); r = json.load(open(d + "/rcpt.json")); s = json.load(open(d + "/sieve.json"))
T = "is_local_domain(rcpt_domain) && key_exists(\x27jabali-mail-elsewhere\x27, rcpt_domain)"
bad = []
rm = o["route"]["match"]
if rm["0"] != {"if": T, "then": "\x27mx\x27"}: bad.append("route arm 0 does not send listed domains to their MX")
if rm["1"] != {"if": "is_local_domain(rcpt_domain)", "then": "\x27local\x27"}: bad.append("route arm 1 no longer delivers hosted domains locally")
if o["route"]["else"] != "\x27mx\x27": bad.append("route else changed")
sm = o["schedule"]["match"]
if sm["0"]["if"] != T or sm["0"]["then"] != "\x27remote\x27": bad.append("schedule arm 0")
if [sm[k]["then"] for k in ("1", "2", "3")] != ["\x27local\x27", "\x27dsn\x27", "\x27report\x27"]: bad.append("schedule lost local/dsn/report")
if o["tls"]["match"]["0"]["then"] != "\x27invalid-tls\x27" or o["connection"]["else"] != "\x27default\x27": bad.append("touched tls/connection")
sc = r["script"]["match"]["0"]
if sc["if"] != "is_empty(authenticated_as) && " + T or sc["then"] != "\x27jabali-mail-elsewhere\x27": bad.append("RCPT script does not refuse unauthenticated mail for listed domains")
if r["allowRelaying"]["else"] != "!is_empty(authenticated_as)": bad.append("touched the RCPT allowRelaying expression")
mine = [x for x in s if x["name"] == "jabali-mail-elsewhere"]
if len(mine) != 1 or not mine[0]["isActive"] or "reject \"550 5.7.1" not in mine[0]["contents"]: bad.append("Sieve script missing or wrong")
if not any(x["name"] == "jabali-something-else" and x["contents"] == "keep" for x in s): bad.append("another Sieve script was touched")
print("\n".join(bad))
')
if [[ -n "$check" ]]; then
  while IFS= read -r l; do echo "FAIL: shipped box: $l"; done <<<"$check"
  fail=1
fi
# Order: Sieve script, then the RCPT refusal, then the route. The route must
# never send a domain's mail out while unauthenticated mail for it is accepted.
order=$(q '
import json, sys
calls = [json.loads(l) for l in open(sys.argv[1] + "/calls.log")]
w = [c[1] for c in calls if c[0] in ("create", "update")]
print(",".join(w))
')
if [[ "$order" != "SieveSystemScript,MtaStageRcpt,MtaOutboundStrategy" ]]; then
  echo "FAIL: write order $order, want SieveSystemScript,MtaStageRcpt,MtaOutboundStrategy"
  fail=1
fi
# No payload on argv (ps shows argv to every user).
if grep -q 'key_exists\|reject' "$tmp/calls.log"; then
  echo "FAIL: a payload was passed on the command line"
  fail=1
fi

# 2. A second run writes nothing.
rm -f "$tmp/calls.log"
converge_stalwart_mail_elsewhere 8446 token || { echo "FAIL: second run returned failure"; fail=1; }
if grep -q '"create"\|"update"' "$tmp/calls.log"; then
  echo "FAIL: second run wrote again: $(grep '"create"\|"update"' "$tmp/calls.log" | tr '\n' ' ')"
  fail=1
fi

# 3. A route an operator changed by hand: nothing is written, a warning says
# so, and the function reports failure.
shipped
q '
import json, sys
p = sys.argv[1] + "/outbound.json"; o = json.load(open(p))
o["route"] = {"match": {"0": {"if": "rcpt_domain == \x27x.example\x27", "then": "\x27relay\x27"}}, "else": "\x27mx\x27"}
json.dump(o, open(p, "w"))
' >/dev/null
if converge_stalwart_mail_elsewhere 8446 token; then
  echo "FAIL: hand-changed route: converger returned success"
  fail=1
fi
if grep -q '"create"\|"update"' "$tmp/calls.log"; then
  echo "FAIL: hand-changed route: converger wrote something"
  fail=1
fi
if ! grep -q 'route' "$tmp/warnings" 2>/dev/null; then
  echo "FAIL: hand-changed route: no warning naming it"
  fail=1
fi

# 4. A value an older version of this converger wrote (names the lookup) is
# replaced, not treated as an operator's.
shipped
q '
import json, sys
p = sys.argv[1] + "/rcpt.json"; r = json.load(open(p))
r["script"] = {"match": {"0": {"if": "key_exists(\x27jabali-mail-elsewhere\x27, rcpt_domain)", "then": "\x27jabali-mail-elsewhere\x27"}}, "else": "false"}
json.dump(r, open(p, "w"))
' >/dev/null
converge_stalwart_mail_elsewhere 8446 token || { echo "FAIL: older own value: converger returned failure"; fail=1; }
if ! q 'import json,sys; r=json.load(open(sys.argv[1]+"/rcpt.json")); sys.exit(0 if r["script"]["match"]["0"]["if"].startswith("is_empty(authenticated_as)") else 1)'; then
  echo "FAIL: older own RCPT script not replaced"
  fail=1
fi

# 5. A refused write is reported.
shipped
if FAKE_FAIL=1 converge_stalwart_mail_elsewhere 8446 token; then
  echo "FAIL: a refused write returned success"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: test_stalwart_mail_elsewhere_converger"
fi
exit "$fail"
