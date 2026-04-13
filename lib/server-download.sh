#!/usr/bin/env bash
# lib/server-download.sh — shared helper to build a "full server download"
# tarball. Used by both the CLI (`jabali-backup server-download`) and the
# panel agent's `jbServerDownloadPipe` RPC.
#
# Two flavours:
#   - configs-only: restore the server snapshot only → tar.gz of configs
#   - full:         restore the server snapshot + every user's latest snapshot
#                   → single combined tar.gz with server/ and users/<user>/
#
# Contract:
#   build_server_download_archive <snapshot_id> <out_archive> [--configs-only]
#   Honours RESTIC_* env vars set by the caller (via restic_env / jbLoadResticEnv).
#   Writes progress to stderr; exits non-zero on restore-restic failure.

set -u

build_server_download_archive() {
    local snapshot_id="$1"
    local out_archive="$2"
    local configs_only="${3:-}"

    if [[ -z "$snapshot_id" || -z "$out_archive" ]]; then
        echo "build_server_download_archive: snapshot_id and out_archive required" >&2
        return 2
    fi

    local tmp_dir
    tmp_dir=$(mktemp -d -t jabali-server-export-XXXXXX)
    trap 'rm -rf "${tmp_dir:-}" 2>/dev/null' RETURN

    mkdir -p "${tmp_dir}/server" "${tmp_dir}/users"

    # Release any stale repo lock from a crashed prior restore
    restic unlock 2>/dev/null || true

    echo "server-download: restoring server snapshot ${snapshot_id}" >&2
    if ! restic restore "$snapshot_id" --target "${tmp_dir}/server" 2>&1 | grep -v '^restoring '; then
        echo "server-download: server restic restore failed" >&2
    fi

    if [[ "$configs_only" != "--configs-only" ]]; then
        # For each active user, resolve the latest snapshot tagged account:<user>
        # and restore it into users/<user>/. Best-effort: per-user failures do
        # not abort the archive build.
        local snaps
        snaps=$(restic snapshots --json 2>/dev/null || echo "[]")

        if command -v jq &>/dev/null && [[ "$snaps" != "[]" ]]; then
            local user uid
            while IFS=$'\t' read -r user uid; do
                [[ -z "$user" || -z "$uid" ]] && continue
                echo "server-download: restoring user $user snapshot $uid" >&2
                restic restore "$uid" --target "${tmp_dir}/users/${user}" 2>/dev/null || \
                    echo "server-download: restore failed for user $user" >&2
            done < <(echo "$snaps" | jq -r '
                [ .[] | select(.tags // [] | any(. == "type:full" or (startswith("account:"))))
                      | {
                          user: ((.tags // [])[] | select(startswith("account:")) | sub("^account:"; "")),
                          id: .short_id,
                          time: .time
                        }
                ]
                | group_by(.user)
                | map(sort_by(.time) | last)
                | .[] | [.user, .id] | @tsv
            ' 2>/dev/null)
        fi
    fi

    echo "server-download: building tarball ${out_archive}" >&2
    tar czf "$out_archive" -C "$tmp_dir" . 2>/dev/null
    local rc=$?
    if [[ $rc -ne 0 ]]; then
        echo "server-download: tar failed (exit $rc)" >&2
        return $rc
    fi
    return 0
}
