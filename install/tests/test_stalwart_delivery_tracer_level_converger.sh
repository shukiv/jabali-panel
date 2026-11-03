#!/usr/bin/env bash
# install/tests/test_stalwart_delivery_tracer_level_converger.sh
#
# converge_stalwart_delivery_tracer_level keeps the delivery Log tracer
# (prefix "delivery", read by Mail > Logs) at debug: Stalwart 1.0 logs
# delivery.completed at debug, so a tracer at info drops every "delivered"
# row there. Only that tracer is touched.
#
# Behavioural: extracts the function from install.sh and runs it against a
# fake stalwart-cli that keeps the tracers in a file.
#
# Run from repo root:
#     bash install/tests/test_stalwart_delivery_tracer_level_converger.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
fn_src=$(awk '/^converge_stalwart_delivery_tracer_level\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: converge_stalwart_delivery_tracer_level() not found in install.sh"
  exit 1
fi
eval "$fn_src"

_log() { :; }
_ok() { :; }
_warn() { printf 'WARN %s\n' "$*" >>"$FAKE_DIR/warnings"; }

if ! grep -q '^  converge_stalwart_delivery_tracer_level "\$jmap_port" "\$admin_token" || true$' install.sh; then
  echo "FAIL: install.sh never calls converge_stalwart_delivery_tracer_level"
  fail=1
fi

# A fresh install gets the tracer from the plan, at debug.
if ! python3 - <<'PY'
import json, re, sys
src = open("install/stalwart/apply-plan.json.tmpl").read()
m = re.search(r'"#jabali-delivery-log":\s*\{[^}]*?"level":\s*"(\w+)"', src)
sys.exit(0 if m and m.group(1) == "debug" else 1)
PY
then
  echo "FAIL: the apply plan's #jabali-delivery-log tracer is not at debug"
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
op, obj = args[0], args[1]
if obj != "Tracer":
    sys.stderr.write("fake: unexpected %r\n" % args); sys.exit(2)
if op == "query":
    fields = args[args.index("--fields") + 1].split(",") if "--fields" in args else ["@type", "enable", "level"]
    for x in t:
        print(json.dumps({**{k: x[k] for k in fields if k in x}, "id": x["id"]}))
elif op == "update":
    assert "--stdin" in args, "payload not on stdin"
    b = json.loads(sys.stdin.read())
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

OTHERS='{"@type":"Stdout","id":"so1","enable":true,"level":"debug"},{"@type":"Log","id":"gen1","enable":true,"level":"info","prefix":"stalwart.log"}'
box() { # <delivery tracer JSON object, comma-led, or empty>
  printf '[%s%s]\n' "$OTHERS" "$1" >"$tmp/tracers.json"
  rm -f "$tmp/calls.log" "$tmp/warnings"
}
q() { python3 -c "$1" "$tmp"; }
writes() { grep -c '"update"' "$tmp/calls.log" || true; }
level_of() { q "import json,sys; print(next(x['level'] for x in json.load(open(sys.argv[1]+'/tracers.json')) if x['id']=='$1'))"; }

# 1. The delivery tracer at info is raised to debug; the others stay as they were.
box ',{"@type":"Log","id":"del1","enable":true,"level":"info","prefix":"delivery"}'
converge_stalwart_delivery_tracer_level 8446 token || { echo "FAIL: info tracer: converger returned failure"; fail=1; }
[[ "$(level_of del1)" == debug ]] || { echo "FAIL: delivery tracer at $(level_of del1), want debug"; fail=1; }
[[ "$(level_of gen1)" == info ]] || { echo "FAIL: the general Log tracer was changed"; fail=1; }
[[ "$(level_of so1)" == debug ]] || { echo "FAIL: the Stdout tracer was changed"; fail=1; }
if grep -q 'debug' "$tmp/calls.log"; then
  echo "FAIL: a payload was passed on the command line"
  fail=1
fi

# 2. A second run writes nothing.
rm -f "$tmp/calls.log"
converge_stalwart_delivery_tracer_level 8446 token || { echo "FAIL: second run returned failure"; fail=1; }
[[ "$(writes)" == 0 ]] || { echo "FAIL: second run wrote again"; fail=1; }

# 3. At trace it already logs the debug events: left alone.
box ',{"@type":"Log","id":"del1","enable":true,"level":"trace","prefix":"delivery"}'
converge_stalwart_delivery_tracer_level 8446 token || { echo "FAIL: trace tracer: converger returned failure"; fail=1; }
[[ "$(writes)" == 0 ]] || { echo "FAIL: trace tracer was rewritten"; fail=1; }

# 4. No delivery tracer: nothing written, not a failure (the agent creates it).
box ''
converge_stalwart_delivery_tracer_level 8446 token || { echo "FAIL: no delivery tracer: converger returned failure"; fail=1; }
[[ "$(writes)" == 0 ]] || { echo "FAIL: no delivery tracer: something was written"; fail=1; }

# 5. A refused write is reported, with a warning.
box ',{"@type":"Log","id":"del1","enable":true,"level":"info","prefix":"delivery"}'
if FAKE_FAIL=1 converge_stalwart_delivery_tracer_level 8446 token; then
  echo "FAIL: a refused write returned success"
  fail=1
fi
grep -q 'not converged' "$tmp/warnings" 2>/dev/null || { echo "FAIL: a refused write gave no warning"; fail=1; }

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: test_stalwart_delivery_tracer_level_converger"
fi
exit "$fail"
