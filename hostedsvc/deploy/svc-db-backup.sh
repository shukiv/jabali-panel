#!/usr/bin/env bash
# Nightly SQLite backup for the jabalihosted service (JAB-213). Losing svc.db
# means every issued token is unrecoverable and every fleet cert dies at its
# next renewal — so this dumps a consistent snapshot (sqlite .backup, safe
# against a live WAL writer) and rsyncs it off-box. Run from root cron.
#
# Off-box target is set via JH_BACKUP_REMOTE (e.g. user@host:/path); if unset,
# the local rotation still runs and a loud warning is logged so a missing
# remote never passes silently.
set -euo pipefail

DB=/var/lib/jabalihosted/svc.db
DIR=/var/backups/jabalihosted
mkdir -p "$DIR"

day=$(date +%u) # 1..7, so we keep a rotating week
out="$DIR/svc-${day}.db"
sqlite3 "$DB" ".backup '$out'"
gzip -f "$out"

if [[ -n "${JH_BACKUP_REMOTE:-}" ]]; then
  rsync -az "${out}.gz" "$JH_BACKUP_REMOTE/" && echo "jabalihosted backup: pushed ${out}.gz -> $JH_BACKUP_REMOTE"
else
  logger -t jabalihosted-backup "WARNING: JH_BACKUP_REMOTE unset — svc.db backup is LOCAL ONLY (a box loss loses every token)"
  echo "WARNING: JH_BACKUP_REMOTE unset — local-only backup at ${out}.gz"
fi
