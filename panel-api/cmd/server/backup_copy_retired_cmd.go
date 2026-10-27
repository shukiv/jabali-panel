// `jabali backup copy` — explicit retired-namespace stub.
//
// M30.1 plan documented `jabali backup copy run` + `jabali backup
// copy worker tick`. Both retired in M30.2 (ADR-0080) when the
// per-destination model replaced the copy_jobs queue: each
// destination now writes directly from the local backup; there's
// no source repo, no copy worker, no copy_jobs table.
//
// Without this stub, `jabali backup copy` falls back to the parent
// `backup` help output — operators following the M30.1 plan literally
// see no clear signal that the surface is gone. Stub prints the
// retirement notice + points at the per-destination write workflow.
package main

import (
	"github.com/spf13/cobra"
)

func newBackupCopyRetiredCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "copy",
		Short: "[RETIRED] superseded by per-destination model",
		Long: `The 'backup copy' namespace was retired in M30.2 (ADR-0080).

Original design (M30.1):
  - Local backup wrote to the shared restic repo
  - Copy worker async-replicated to each destination via
    'jabali backup copy run --copy-job-id=<id>'
  - backup_copy_jobs queue row per (backup_job, destination)

Retired model (M30.2):
  - Each backup_destination writes DIRECTLY from the local stage
    via its own restic invocation. No source repo, no copy worker,
    no copy_jobs table.
  - Per-destination credentials live at
    /etc/jabali-panel/restic-remotes/<dest-id>.env

Operator workflow today:
  jabali backup destination create --name S3 --type s3 ...
  jabali backup schedule create --kind account --user-id <ULID> \\
      --destination-ids <dest-id-1>,<dest-id-2>
  jabali backup scheduler tick   # or wait for the 60s automatic tick

The schedule fires once + writes to every linked destination
in parallel. No copy step needed.

If you read this from a docs link to M30.1 — the plan is stale.
Refer to plans/m30.1-backup-schedules-destinations.md §"M30.2
amendment" for the current shape, and ADR-0080 for the rationale
behind the retirement.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	return cmd
}
