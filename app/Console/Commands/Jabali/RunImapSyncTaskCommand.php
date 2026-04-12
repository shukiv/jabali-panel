<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Jobs\RunImapSync;
use App\Models\ImapSyncTask;
use App\Services\Agent\AgentClient;
use App\Services\BackgroundTasks\Binaries\TaskLogWriter;
use Illuminate\Console\Command;

/**
 * Spawned binary for the imap_sync task type.
 */
class RunImapSyncTaskCommand extends Command
{
    protected $signature = 'jabali:tasks:run-imap-sync
        {--task-id= : Background task UUID}
        {--progress-log= : Path to write JSONL events to}
        {--sync-task-id= : ImapSyncTask id}';

    protected $description = 'Spawned binary: run an IMAP sync and emit task progress';

    public function handle(AgentClient $agent): int
    {
        $logPath = (string) $this->option('progress-log');
        if ($logPath === '') {
            $this->error('--progress-log is required');

            return self::FAILURE;
        }

        $writer = new TaskLogWriter($logPath);
        $writer->started();

        $syncTaskId = (int) $this->option('sync-task-id');
        $syncTask = ImapSyncTask::find($syncTaskId);
        if ($syncTask === null) {
            $writer->failed("ImapSyncTask {$syncTaskId} not found");

            return self::FAILURE;
        }

        $writer->step('syncing '.$syncTask->source_email);

        try {
            config(['jabali.agent_v2.imap_sync_enabled' => false]);
            (new RunImapSync($syncTask->id))->handle($agent);
        } catch (\Throwable $e) {
            $writer->failed($e->getMessage());

            return self::FAILURE;
        }

        $fresh = $syncTask->fresh();
        if (($fresh->status ?? '') === 'failed') {
            $writer->failed((string) ($fresh->error_message ?? 'sync failed'));

            return self::FAILURE;
        }

        $writer->done([
            'status' => $fresh->status,
            'messages' => $fresh->messages_synced,
        ]);

        return self::SUCCESS;
    }
}
