#!/usr/bin/env bash
# install/tests/test_stalwart_cache_sizes_converger.sh
#
# Stalwart 1.0 raised its cache defaults (messages 320 MB, messagesLarge and
# groupwareLarge 512 MB each) past the memory bound Jabali puts on small hosts.
# converge_stalwart_cache_sizes sizes those three to 35% of the host's
# MemoryHigh (stalwart_cache_sizes_mb), never above the defaults, and leaves a
# 0.16 server (no messagesLarge) alone.
#
# Behavioural: extracts the functions from install.sh and runs the converger
# against a fake stalwart-cli that keeps the Cache singleton in a file.
#
# Run from repo root:
#     bash install/tests/test_stalwart_cache_sizes_converger.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }
for f in stalwart_mem_bounds_mb stalwart_cache_sizes_mb converge_stalwart_cache_sizes; do
  src=$(fn "$f")
  if [[ -z "$src" ]]; then
    echo "FAIL: $f() not found in install.sh"
    exit 1
  fi
  eval "$src"
done

_log() { :; }
_ok() { :; }
_warn() { printf 'WARN %s\n' "$*" >>"$FAKE_DIR/warnings"; }

if ! grep -q '^  converge_stalwart_cache_sizes "\$jmap_port" "\$admin_token" || true$' <<<"$(fn _install_stalwart_apply_plan)"; then
  echo "FAIL: _install_stalwart_apply_plan never calls converge_stalwart_cache_sizes"
  fail=1
fi

# --- The sizes ---------------------------------------------------------------
# <MemoryHigh MB> <want>
while read -r high want; do
  got=$(stalwart_cache_sizes_mb "$high")
  [[ "$got" == "$want" ]] || { echo "FAIL: sizes for MemoryHigh $high MB: got '$got', want '$want'"; fail=1; }
done <<'EOF'
512 42 68 68
1024 85 136 136
2478 206 330 330
3839 319 511 511
3840 320 512 512
4096 320 512 512
EOF

# Never above the defaults, never shrinking as RAM grows, never 0.
prev="0 0 0"
for high in $(seq 512 64 8192); do
  read -r m ml gl <<<"$(stalwart_cache_sizes_mb "$high")"
  read -r pm pml pgl <<<"$prev"
  if (( m > 320 || ml > 512 || gl > 512 )); then echo "FAIL: MemoryHigh $high MB: above the defaults ($m $ml $gl)"; fail=1; fi
  if (( m < pm || ml < pml || gl < pgl )); then echo "FAIL: MemoryHigh $high MB: smaller than at less RAM"; fail=1; fi
  if (( m == 0 || ml == 0 || gl == 0 )); then echo "FAIL: MemoryHigh $high MB: a cache of 0"; fail=1; fi
  prev="$m $ml $gl"
done

# --- The converger -------------------------------------------------------------
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export FAKE_DIR="$tmp"

# Fake stalwart-cli. State: cache.json (the singleton). Every call's argv goes
# to calls.log, one JSON array per line.
cat >"$tmp/stalwart-cli" <<'EOF'
#!/usr/bin/env python3
import json, os, sys
d = os.environ["FAKE_DIR"]
args = sys.argv[1:]
with open(os.path.join(d, "calls.log"), "a") as f:
    f.write(json.dumps(args) + "\n")
p = os.path.join(d, "cache.json")
c = json.load(open(p))
op, obj = args[0], args[1]
if obj not in ("Cache", "x:Cache"):
    sys.stderr.write("fake: unexpected %r\n" % args); sys.exit(2)
if op == "get":
    print(json.dumps(c))
elif op == "update":
    if os.environ.get("FAKE_FAIL"):
        sys.stderr.write("error: invalidPatch\n"); sys.exit(1)
    assert args[2] == "singleton", "not the singleton"
    assert "--stdin" in args, "payload not on stdin"
    b = json.loads(sys.stdin.read())
    for k, v in b.items():
        if k not in c or not isinstance(v, int):
            sys.stderr.write("error: invalidPatch %s\n" % k); sys.exit(1)
    c.update(b)
    json.dump(c, open(p, "w"))
else:
    sys.stderr.write("fake: unexpected %r\n" % args); sys.exit(2)
EOF
chmod +x "$tmp/stalwart-cli"
export JABALI_STALWART_CLI="$tmp/stalwart-cli"
MB=1048576

# box <ram MB> <cache JSON>
box() {
  printf 'MemTotal:       %d kB\n' "$(( $1 * 1024 ))" >"$tmp/meminfo"
  printf '%s\n' "$2" >"$tmp/cache.json"
  rm -f "$tmp/calls.log" "$tmp/warnings"
}
export JABALI_MEMINFO="$tmp/meminfo"
val() { python3 -c "import json,sys; print(json.load(open(sys.argv[1]+'/cache.json'))['$1'])" "$tmp"; }
updates() { grep -c '"update"' "$tmp/calls.log" || true; }
V10="{\"id\":\"singleton\",\"messages\":$((320*MB)),\"messagesLarge\":$((512*MB)),\"groupwareLarge\":$((512*MB)),\"events\":$((48*MB))}"

# 1. A 4 GB host on 1.0 defaults: MemoryHigh 1024 MB -> 85 / 136 / 136 MB.
box 4096 "$V10"
converge_stalwart_cache_sizes 8446 token || { echo "FAIL: 4 GB host: converger returned failure"; fail=1; }
[[ "$(val messages)" == $((85*MB)) ]] || { echo "FAIL: messages is $(val messages), want $((85*MB))"; fail=1; }
[[ "$(val messagesLarge)" == $((136*MB)) ]] || { echo "FAIL: messagesLarge is $(val messagesLarge), want $((136*MB))"; fail=1; }
[[ "$(val groupwareLarge)" == $((136*MB)) ]] || { echo "FAIL: groupwareLarge is $(val groupwareLarge), want $((136*MB))"; fail=1; }
[[ "$(val events)" == $((48*MB)) ]] || { echo "FAIL: another cache (events) was changed"; fail=1; }
if grep -q "$((85*MB))" "$tmp/calls.log"; then
  echo "FAIL: a payload was passed on the command line"
  fail=1
fi

# 2. A second run writes nothing.
rm -f "$tmp/calls.log"
converge_stalwart_cache_sizes 8446 token || { echo "FAIL: second run returned failure"; fail=1; }
[[ "$(updates)" == 0 ]] || { echo "FAIL: second run wrote again"; fail=1; }

# 3. A 16 GB host keeps Stalwart's defaults: nothing written.
box 16384 "$V10"
converge_stalwart_cache_sizes 8446 token || { echo "FAIL: 16 GB host: converger returned failure"; fail=1; }
[[ "$(updates)" == 0 ]] || { echo "FAIL: 16 GB host: the defaults were rewritten"; fail=1; }

# 4. Stalwart 0.16 (no messagesLarge): left alone, not a failure.
box 2048 "{\"id\":\"singleton\",\"messages\":$((50*MB)),\"events\":$((10*MB))}"
converge_stalwart_cache_sizes 8446 token || { echo "FAIL: 0.16: converger returned failure"; fail=1; }
[[ "$(updates)" == 0 ]] || { echo "FAIL: 0.16: something was written"; fail=1; }
[[ "$(val messages)" == $((50*MB)) ]] || { echo "FAIL: 0.16: messages changed"; fail=1; }

# 5. A refused write is reported, with a warning.
box 4096 "$V10"
if FAKE_FAIL=1 converge_stalwart_cache_sizes 8446 token; then
  echo "FAIL: a refused write returned success"
  fail=1
fi
grep -q 'not converged' "$tmp/warnings" 2>/dev/null || { echo "FAIL: a refused write gave no warning"; fail=1; }

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: test_stalwart_cache_sizes_converger"
fi
exit "$fail"
