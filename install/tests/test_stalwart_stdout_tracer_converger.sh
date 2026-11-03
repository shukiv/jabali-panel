#!/usr/bin/env bash
# install/tests/test_stalwart_stdout_tracer_converger.sh
#
# converge_stalwart_stdout_tracer makes sure Stalwart has an enabled Stdout
# tracer at debug level, which feeds the journal that CrowdSec's Stalwart
# scenarios read. The apply plan only creates it on a fresh install, and boxes
# installed before 2026-06-03 never got it.
#
# Behavioural: extracts the function from install.sh and runs it against a
# fake stalwart-cli that keeps the tracers in a file.
#
# Run from repo root:
#     bash install/tests/test_stalwart_stdout_tracer_converger.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
fn_src=$(awk '/^converge_stalwart_stdout_tracer\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: converge_stalwart_stdout_tracer() not found in install.sh"
  exit 1
fi
eval "$fn_src"

_log() { :; }
_ok() { :; }
_warn() { printf 'WARN %s\n' "$*" >>"$FAKE_DIR/warnings"; }

if ! grep -q '^  converge_stalwart_stdout_tracer "\$jmap_port" "\$admin_token" || true$' install.sh; then
  echo "FAIL: install.sh never calls converge_stalwart_stdout_tracer"
  fail=1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export FAKE_DIR="$tmp"

# Fake stalwart-cli. State: tracers.json (a list). Every call's argv goes to
# calls.log, one JSON array per line.
cat >"$tmp/stalwart-cli" <<'EOF'
#!/usr/bin/env python3
import json, os, sys
d = os.environ["FAKE_DIR"]
args = sys.argv[1:]
with open(os.path.join(d, "calls.log"), "a") as f:
    f.write(json.dumps(args) + "\n")
if os.environ.get("FAKE_FAIL") and args[0] in ("create", "update"):
    sys.stderr.write("refused\n"); sys.exit(1)
p = os.path.join(d, "tracers.json")
t = json.load(open(p))
def body():
    assert "--stdin" in args, "payload not on stdin"
    return json.loads(sys.stdin.read())
op, obj = args[0], args[1]
if obj != "Tracer":
    sys.stderr.write("fake: unexpected %r\n" % args); sys.exit(2)
if op == "query":
    fields = args[args.index("--fields") + 1].split(",") if "--fields" in args else ["@type", "enable", "level"]
    for x in t:
        print(json.dumps({**{k: x[k] for k in fields if k in x}, "id": x["id"]}))
elif op == "create":
    b = body(); b["id"] = "t%d" % len(t); t.append(b)
elif op == "update":
    b = body()
    hit = [x for x in t if x["id"] == args[2]]
    if not hit:
        sys.stderr.write("not found\n"); sys.exit(1)
    hit[0].update(b)
else:
    sys.stderr.write("fake: unexpected %r\n" % args); sys.exit(2)
json.dump(t, open(p, "w"))
EOF
chmod +x "$tmp/stalwart-cli"
export JABALI_STALWART_CLI="$tmp/stalwart-cli"

LOGS='{"@type":"Log","id":"log1","enable":true,"level":"info","ansi":false,"path":"/var/log/stalwart","prefix":"delivery"},{"@type":"Log","id":"log2","enable":true,"level":"info","ansi":false}'
box() { # <extra tracer JSON objects, comma-led, or empty>
  printf '[%s%s]\n' "$LOGS" "$1" >"$tmp/tracers.json"
  rm -f "$tmp/calls.log" "$tmp/warnings"
}
q() { python3 -c "$1" "$tmp"; }
writes() { grep -c '"create"\|"update"' "$tmp/calls.log" || true; }

# 1. A box installed before the plan had the tracer: one is created, with the
#    plan's values, and the Log tracers are left alone.
box ''
if ! converge_stalwart_stdout_tracer 8446 token; then
  echo "FAIL: no Stdout tracer: converger returned failure"
  fail=1
fi
check=$(q '
import json, sys
t = json.load(open(sys.argv[1] + "/tracers.json"))
s = [x for x in t if x["@type"] == "Stdout"]
bad = []
if len(s) != 1: bad.append("want one Stdout tracer, have %d" % len(s))
elif {k: s[0].get(k) for k in ("enable", "level", "ansi", "lossy", "buffered")} != {"enable": True, "level": "debug", "ansi": False, "lossy": False, "buffered": False}:
    bad.append("created tracer has %r" % s[0])
logs = [x for x in t if x["@type"] == "Log"]
if [x["level"] for x in logs] != ["info", "info"] or logs[0].get("prefix") != "delivery": bad.append("a Log tracer was touched")
print("\n".join(bad))
')
if [[ -n "$check" ]]; then
  while IFS= read -r l; do echo "FAIL: no Stdout tracer: $l"; done <<<"$check"
  fail=1
fi
# No payload on argv (ps shows argv to every user).
if grep -q 'debug' "$tmp/calls.log"; then
  echo "FAIL: a payload was passed on the command line"
  fail=1
fi

# 2. A second run writes nothing.
rm -f "$tmp/calls.log"
converge_stalwart_stdout_tracer 8446 token || { echo "FAIL: second run returned failure"; fail=1; }
if [[ "$(writes)" != 0 ]]; then
  echo "FAIL: second run wrote again"
  fail=1
fi

# 3. A Stdout tracer at info (auth.failed is a debug event): raised to debug in
#    place, no second tracer.
box ',{"@type":"Stdout","id":"so1","enable":true,"level":"info","ansi":false}'
converge_stalwart_stdout_tracer 8446 token || { echo "FAIL: info tracer: converger returned failure"; fail=1; }
if ! q 'import json,sys; t=json.load(open(sys.argv[1]+"/tracers.json")); s=[x for x in t if x["@type"]=="Stdout"]; sys.exit(0 if len(s)==1 and s[0]["id"]=="so1" and s[0]["level"]=="debug" and s[0]["enable"] is True else 1)'; then
  echo "FAIL: info tracer not raised to debug in place"
  fail=1
fi

# 4. A disabled Stdout tracer, or one with colour codes on: put right.
box ',{"@type":"Stdout","id":"so1","enable":false,"level":"debug","ansi":true}'
converge_stalwart_stdout_tracer 8446 token || { echo "FAIL: disabled tracer: converger returned failure"; fail=1; }
if ! q 'import json,sys; t=json.load(open(sys.argv[1]+"/tracers.json")); s=[x for x in t if x["@type"]=="Stdout"]; sys.exit(0 if len(s)==1 and s[0]["enable"] is True and s[0]["ansi"] is False else 1)'; then
  echo "FAIL: disabled or ansi tracer not put right"
  fail=1
fi

# 5. A Stdout tracer at trace already carries the debug events: left alone.
box ',{"@type":"Stdout","id":"so1","enable":true,"level":"trace","ansi":false}'
converge_stalwart_stdout_tracer 8446 token || { echo "FAIL: trace tracer: converger returned failure"; fail=1; }
if [[ "$(writes)" != 0 ]]; then
  echo "FAIL: trace tracer was rewritten"
  fail=1
fi

# 6. A refused write is reported, with a warning.
box ''
if FAKE_FAIL=1 converge_stalwart_stdout_tracer 8446 token; then
  echo "FAIL: a refused write returned success"
  fail=1
fi
if ! grep -q 'not converged' "$tmp/warnings" 2>/dev/null; then
  echo "FAIL: a refused write gave no warning"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: test_stalwart_stdout_tracer_converger"
fi
exit "$fail"
