<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Enums\BackgroundTaskType;
use App\Services\BackgroundTasks\BackgroundTaskDispatcher;
use Illuminate\Contracts\Queue\ShouldBeUnique;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Log;

/**
 * Dispatch a jabali-backup run as a BackgroundTask.
 *
 * Unlike legacy jobs that did the work inline, this queue worker is a pure
 * dispatcher: it creates a background_tasks row, calls agent.job.start, and
 * returns immediately. The actual backup runs for hours inside a transient
 * systemd unit supervised by the agent.
 *
 * @see docs/background-task-binary-contract.md
 * @see docs/adr/0007-agent-v2-control-plane.md
 */
class RunBackup implements ShouldBeUnique, ShouldQueue
{
    use Queueable;

    public int $uniqueFor = 300;

    /**
     * @param  array<string, mixed>  $backupArgs  Forwarded verbatim to the binary (e.g. --mode=full).
     */
    public function __construct(
        public string $reason = 'manual',
        public array $backupArgs = [],
    ) {}

    public function uniqueId(): string
    {
        return 'backup:'.$this->reason;
    }

    public function handle(BackgroundTaskDispatcher $dispatcher): void
    {
        $backupBinary = config('jabali.agent_v2.backup_binary', '/opt/jabali-backup/bin/jabali-backup');

        $argv = [$backupBinary];
        foreach ($this->backupArgs as $key => $value) {
            $argv[] = is_int($key) ? (string) $value : "--{$key}=".$value;
        }

        $task = $dispatcher->dispatch(
            type: BackgroundTaskType::Backup,
            argv: $argv,
            payload: [
                'reason' => $this->reason,
                'args' => $this->backupArgs,
            ],
            dedupeKey: 'backup:'.$this->reason,
            limits: [
                'cpu' => 50,   // don't starve the panel
                'memory' => '1G',
                'io' => 100,
            ],
        );

        if ($task === null) {
            Log::info("RunBackup: dedupe — backup with reason={$this->reason} already running");

            return;
        }

        Log::info("RunBackup: dispatched task {$task->id}");
    }
}
