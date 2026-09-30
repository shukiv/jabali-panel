#!/usr/bin/env bash
# install/tests/test_redis_maxmemory.sh — Redis maxmemory sized to host RAM.
#
# Redis ran with a fixed `maxmemory 128mb` (allkeys-lru) on every box. A fleet
# box sat at 99.7% of it: tenants' WordPress object caches evicted each other,
# and the notification stream in db 0 was as evictable as any cache key.
#
# Asserts install.sh ships:
#   1. The sizing brackets, executed at every interesting host size, never
#      below the old 128 MB and never above the 1024 MB ceiling.
#   2. The drop-in sorting after 10-jabali-socket.conf (so it overrides the
#      fixed 128mb) and before an operator's higher-numbered override.
#   3. size_redis_maxmemory writing the drop-in, restarting Redis only when it
#      changed, and leaving Redis alone when MemTotal is unreadable.
#   4. Both wiring points: fresh install, and `jabali update` for the fleet.
#   5. When redis-server is installed: Redis really applies the last
#      maxmemory across 10-, 15- and an operator's 50- drop-in.
#
# Run from repo root:
#     bash install/tests/test_redis_maxmemory.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. The pure arithmetic, pulled out of install.sh and run. ---
calc_src=$(awk '/^redis_maxmemory_mb\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$calc_src" ]]; then
  echo "FAIL: redis_maxmemory_mb not defined in install.sh"
  exit 1
fi
eval "$calc_src"

check() {
  local mem_mb="$1" want="$2" label="$3" got
  got=$(redis_maxmemory_mb "$mem_mb")
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: ${mem_mb}MB host ($label): got maxmemory ${got}MB, want ${want}MB"
    fail=1
  fi
}

#     host    want   why
check 1024    128    "floor: never below the old 128 MB"
check 2048    128    "RAM/16 is exactly the floor"
check 3900    192    "a 3.8 GB VPS rounds down to a 64 MB step"
check 4096    256    "RAM/16"
check 7884    448    "a 7.7 GB host rounds down to a 64 MB step"
check 8192    512    "RAM/16"
check 16384   1024   "RAM/16 reaches the ceiling"
check 65536   1024   "large host stays at the ceiling"

for mb in 256 512 1024 2048 3072 3900 4096 6144 7884 8192 12288 16384 32768 65536 131072; do
  got=$(redis_maxmemory_mb "$mb")
  if [[ "$got" -lt 128 || "$got" -gt 1024 ]]; then
    echo "FAIL: ${mb}MB host: maxmemory ${got}MB outside [128, 1024]"
    fail=1
  fi
  if (( got % 64 != 0 )); then
    echo "FAIL: ${mb}MB host: maxmemory ${got}MB is not a 64 MB step"
    fail=1
  fi
done

# --- 2. Drop-in order: after the socket drop-in, before an operator's. ---
fn_src=$(awk '/^size_redis_maxmemory\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: size_redis_maxmemory not defined in install.sh"
  exit 1
fi
dropin_name=$(grep -o '[0-9][0-9]-jabali-maxmemory\.conf' <<<"$fn_src" | head -1)
if [[ -z "$dropin_name" ]]; then
  echo "FAIL: size_redis_maxmemory does not name a NN-jabali-maxmemory.conf drop-in"
  fail=1
else
  order=$(printf '%s\n' 50-local.conf "$dropin_name" 10-jabali-socket.conf | LC_ALL=C sort | tr '\n' ' ')
  if [[ "$order" != "10-jabali-socket.conf ${dropin_name} 50-local.conf " ]]; then
    echo "FAIL: $dropin_name does not sort between 10-jabali-socket.conf and 50-local.conf (got: $order)"
    fail=1
  fi
fi

# --- 3. The function against a scratch directory, systemctl stubbed. ---
work=$(mktemp -d /tmp/jabali-redis-mem.XXXXXX)
pid=""
cleanup() {
  [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

mkdir -p "$work/conf.d"
_log()  { :; }
_warn() { echo "warn: $*" >>"$work/warn.log"; }
systemctl() { echo "$*" >>"$work/systemctl.log"; }
redis-server() { :; }
install() {
  # install -m MODE -o OWNER -g GROUP SRC DST: copy only (no chown in a test).
  local src="${*: -2:1}" dst="${*: -1}"
  cp "$src" "$dst"
}
set_ram_mb() { printf 'MemTotal:       %s kB\n' "$(( $1 * 1024 ))" >"$work/meminfo"; }
restarts() { grep -c 'try-restart redis-server' "$work/systemctl.log" 2>/dev/null || true; }

test_fn="${fn_src//\/etc\/redis\/redis.conf.d/$work/conf.d}"
test_fn="${test_fn//\/proc\/meminfo/$work/meminfo}"
eval "$test_fn"

dropin="$work/conf.d/${dropin_name:-15-jabali-maxmemory.conf}"

set_ram_mb 7884
size_redis_maxmemory
if ! grep -qx 'maxmemory 448mb' "$dropin" 2>/dev/null; then
  echo "FAIL: 7.7 GB host: drop-in does not set maxmemory 448mb"
  fail=1
fi
if [[ "$(restarts)" != 1 ]]; then
  echo "FAIL: writing the drop-in should restart Redis once (got $(restarts))"
  fail=1
fi

size_redis_maxmemory
if [[ "$(restarts)" != 1 ]]; then
  echo "FAIL: an unchanged drop-in restarted Redis again"
  fail=1
fi

# MemTotal drift from a kernel update must not rewrite the drop-in.
set_ram_mb 7870
size_redis_maxmemory
if [[ "$(restarts)" != 1 ]]; then
  echo "FAIL: a few MB of MemTotal drift restarted Redis"
  fail=1
fi

set_ram_mb 16384
size_redis_maxmemory
if ! grep -qx 'maxmemory 1024mb' "$dropin" || [[ "$(restarts)" != 2 ]]; then
  echo "FAIL: a RAM upgrade should rewrite the drop-in to 1024mb and restart once"
  fail=1
fi

: >"$work/meminfo"
before=$(cat "$dropin")
size_redis_maxmemory
if [[ "$(cat "$dropin")" != "$before" || "$(restarts)" != 2 ]]; then
  echo "FAIL: unreadable MemTotal changed the drop-in or restarted Redis"
  fail=1
fi

# --- 4. Wiring: fresh install and `jabali update`. ---
unset -f install redis-server systemctl
fresh=$(grep -nE '^  install_redis_acl$' install.sh | head -1 | cut -d: -f1)
if [[ -z "$fresh" ]] || ! sed -n "$((fresh + 1))p" install.sh | grep -q '^  size_redis_maxmemory'; then
  echo "FAIL: the fresh-install sequence does not run size_redis_maxmemory after install_redis_acl"
  fail=1
fi
pns=$(awk '/^provision_new_software\(\) \{$/,/^\}$/' install.sh)
if ! grep -q '^    size_redis_maxmemory$' <<<"$pns"; then
  echo "FAIL: provision_new_software (jabali update) does not run size_redis_maxmemory"
  fail=1
fi

# --- 5. Real Redis: the last maxmemory read wins. ---
if ! command -v redis-server >/dev/null 2>&1 || ! command -v redis-cli >/dev/null 2>&1; then
  echo "SKIP: redis-server not installed; drop-in precedence not run"
else
  mkdir -p "$work/r/conf.d"
  sock="$work/r/r.sock"
  printf 'maxmemory 128mb\n' >"$work/r/conf.d/10-jabali-socket.conf"
  cp "$dropin" "$work/r/conf.d/${dropin_name:-15-jabali-maxmemory.conf}"
  printf 'port 0\nunixsocket %s\ndir %s\nsave ""\ninclude %s/conf.d/*.conf\n' \
    "$sock" "$work/r" "$work/r" >"$work/r/redis.conf"
  start_redis() {
    redis-server "$work/r/redis.conf" --daemonize yes --pidfile "$work/r/r.pid" --logfile "$work/r/r.log"
    for _ in 1 2 3 4 5 6 7 8 9 10; do [[ -S "$sock" ]] && break; sleep 0.2; done
    pid=$(cat "$work/r/r.pid" 2>/dev/null || true)
  }
  stop_redis() {
    kill "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do [[ -S "$sock" ]] || break; sleep 0.2; done
    pid=""
  }
  maxmem() { local o; o=$(redis-cli -s "$sock" CONFIG GET maxmemory 2>&1 || true); printf '%s\n' "${o##*$'\n'}"; }

  start_redis
  if [[ "$(maxmem)" != "$((1024 * 1024 * 1024))" ]]; then
    echo "FAIL: Redis applied maxmemory $(maxmem), want the 15- drop-in's 1024mb over 10-'s 128mb"
    fail=1
  fi
  stop_redis

  printf 'maxmemory 2048mb\n' >"$work/r/conf.d/50-local.conf"
  start_redis
  if [[ "$(maxmem)" != "$((2048 * 1024 * 1024))" ]]; then
    echo "FAIL: an operator's 50-local.conf did not override the jabali drop-in (got $(maxmem))"
    fail=1
  fi
  stop_redis
fi

if [[ $fail -ne 0 ]]; then
  exit 1
fi
echo "PASS: Redis maxmemory is sized to host RAM on fresh installs and updates"
