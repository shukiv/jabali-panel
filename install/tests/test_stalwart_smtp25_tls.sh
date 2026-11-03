#!/usr/bin/env bash
# install/tests/test_stalwart_smtp25_tls.sh
#
# Port 25 must offer STARTTLS whichever Stalwart listener serves it. Stalwart's
# own "smtp" listener ([::]:25) has TLS on; where IPv6 is off in the kernel it
# can't bind, and the apply plan's "smtp-25" (0.0.0.0:25) serves port 25
# instead. A listener without TLS offers no STARTTLS (checked on 1.0.2), so:
#   - every listener the plan puts on a public address has TLS on;
#   - converge_stalwart_smtp25_tls turns TLS on for an existing smtp-25 and
#     changes no other listener.
#
# Behavioural: reads the real template, extracts the converger from install.sh
# and runs it against a fake stalwart-cli that keeps the listeners in a file.
#
# Run from repo root:
#     bash install/tests/test_stalwart_smtp25_tls.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmpl=install/stalwart/apply-plan.json.tmpl

# --- The plan ------------------------------------------------------------------
plan_out=$(python3 - <(sed -e 's|{{\.MariaDBPassword}}|x|g' "$tmpl") <<'PY'
import json, sys
plan = json.load(open(sys.argv[1]))
found = 0
for op in plan:
    if op.get("object") != "x:NetworkListener":
        continue
    for key, l in op["value"].items():
        hosts = [b.rsplit(":", 1)[0] for b in l.get("bind", {})]
        public = [h for h in hosts if h not in ("127.0.0.1", "[::1]")]
        if l.get("name") == "smtp-25":
            found = 1
        if public and l.get("useTls") is not True:
            print("FAIL: plan listener %s binds %s without TLS" % (l.get("name"), ", ".join(l["bind"])))
if not found:
    print("FAIL: the plan has no smtp-25 listener")
PY
)
if [[ -n "$plan_out" ]]; then
  printf '%s\n' "$plan_out"
  fail=1
fi

# --- The converger ---------------------------------------------------------------
# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }
src=$(fn converge_stalwart_smtp25_tls)
if [[ -z "$src" ]]; then
  echo "FAIL: converge_stalwart_smtp25_tls() not found in install.sh"
  exit 1
fi
eval "$src"

_log() { :; }
_ok() { :; }
_warn() { printf 'WARN %s\n' "$*" >>"$FAKE_DIR/warnings"; }

if ! grep -q '^  converge_stalwart_smtp25_tls "\$jmap_port" "\$admin_token" || true$' <<<"$(fn _install_stalwart_apply_plan)"; then
  echo "FAIL: _install_stalwart_apply_plan never calls converge_stalwart_smtp25_tls"
  fail=1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export FAKE_DIR="$tmp"

# Fake stalwart-cli. State: listeners.json (a list of NetworkListener objects).
# Every call's argv goes to calls.log, one JSON array per line.
cat >"$tmp/stalwart-cli" <<'EOF'
#!/usr/bin/env python3
import json, os, sys
d = os.environ["FAKE_DIR"]
args = sys.argv[1:]
with open(os.path.join(d, "calls.log"), "a") as f:
    f.write(json.dumps(args) + "\n")
p = os.path.join(d, "listeners.json")
ls = json.load(open(p))
op, obj = args[0], args[1]
if obj not in ("NetworkListener", "x:NetworkListener"):
    sys.stderr.write("fake: unexpected %r\n" % args); sys.exit(2)
if op == "query":
    # Like stalwart-cli 1.0: NDJSON, only the asked-for fields plus id.
    fields = args[args.index("--fields") + 1].split(",") if "--fields" in args else ["name", "bind"]
    for l in ls:
        print(json.dumps({**{k: l.get(k) for k in fields}, "id": l["id"]}))
elif op == "update":
    if os.environ.get("FAKE_FAIL"):
        sys.stderr.write("error: invalidPatch\n"); sys.exit(1)
    assert "--stdin" in args, "payload not on stdin"
    b = json.loads(sys.stdin.read())
    hit = [l for l in ls if l["id"] == args[2]]
    if not hit:
        sys.stderr.write("error: notFound\n"); sys.exit(1)
    hit[0].update(b)
    json.dump(ls, open(p, "w"))
else:
    sys.stderr.write("fake: unexpected %r\n" % args); sys.exit(2)
EOF
chmod +x "$tmp/stalwart-cli"
export JABALI_STALWART_CLI="$tmp/stalwart-cli"

# box <listeners JSON>
box() {
  printf '%s\n' "$1" >"$tmp/listeners.json"
  rm -f "$tmp/calls.log" "$tmp/warnings"
}
tls() { python3 -c "import json,sys; print([l for l in json.load(open(sys.argv[1]+'/listeners.json')) if l['id']==sys.argv[2]][0]['useTls'])" "$tmp" "$1"; }
updates() { grep -c '"update"' "$tmp/calls.log" || true; }

# The listeners on the test box after the migration to 1.0.2 (2026-11-03).
BOX='[
 {"id":"f25","name":"smtp","bind":{"[::]:25":true},"protocol":"smtp","useTls":true,"implicitTls":false},
 {"id":"p25","name":"smtp-25","bind":{"0.0.0.0:25":true},"protocol":"smtp","useTls":false,"implicitTls":false},
 {"id":"p587","name":"smtp-submission-587","bind":{"0.0.0.0:587":true},"protocol":"smtp","useTls":true,"implicitTls":false},
 {"id":"jmap","name":"jmap-loopback","bind":{"127.0.0.1:8446":true},"protocol":"http","useTls":false,"implicitTls":false},
 {"id":"int","name":"internal-loopback","bind":{"127.0.0.1:18181":true},"protocol":"http","useTls":false,"implicitTls":false}
]'

# 1. smtp-25 without TLS gets TLS; nothing else is written.
box "$BOX"
converge_stalwart_smtp25_tls 8446 token || { echo "FAIL: converger returned failure"; fail=1; }
[[ "$(tls p25)" == True ]] || { echo "FAIL: smtp-25 still has useTls $(tls p25)"; fail=1; }
[[ "$(updates)" == 1 ]] || { echo "FAIL: $(updates) writes, want 1 (smtp-25 only)"; fail=1; }
[[ "$(tls jmap)" == False && "$(tls int)" == False ]] || { echo "FAIL: a loopback listener was changed"; fail=1; }
if grep -q '"useTls"' "$tmp/calls.log"; then
  echo "FAIL: a payload was passed on the command line"
  fail=1
fi

# 2. A second run writes nothing.
rm -f "$tmp/calls.log"
converge_stalwart_smtp25_tls 8446 token || { echo "FAIL: second run returned failure"; fail=1; }
[[ "$(updates)" == 0 ]] || { echo "FAIL: second run wrote again"; fail=1; }

# 3. No smtp-25 (a box set up some other way): nothing written, not a failure.
box '[{"id":"f25","name":"smtp","bind":{"[::]:25":true},"protocol":"smtp","useTls":true,"implicitTls":false}]'
converge_stalwart_smtp25_tls 8446 token || { echo "FAIL: no smtp-25: converger returned failure"; fail=1; }
[[ "$(updates)" == 0 ]] || { echo "FAIL: no smtp-25: something was written"; fail=1; }

# 4. A refused write is reported, with a warning.
box "$BOX"
if FAKE_FAIL=1 converge_stalwart_smtp25_tls 8446 token; then
  echo "FAIL: a refused write returned success"
  fail=1
fi
grep -q 'not converged' "$tmp/warnings" 2>/dev/null || { echo "FAIL: a refused write gave no warning"; fail=1; }

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: test_stalwart_smtp25_tls"
fi
exit "$fail"
