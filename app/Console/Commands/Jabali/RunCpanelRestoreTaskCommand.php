<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Jobs\RunCpanelRestore;
use App\Services\Agent\AgentClient;
use App\Services\BackgroundTasks\Binaries\TaskLogWriter;
use App\Services\Migration\MigrationDnsSyncService;
use App\Services\Migration\MigrationEmailProvisionService;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Cache;

/**
 * Spawned binary for the cpanel_restore task type.
 *
 * Constructor args of the legacy RunCpanelRestore job are stashed in the
 * Laravel cache under a key passed via --cache-key. We reconstitute the
 * job and call its handle() method with the feature flag bypassed to
 * avoid recursion.
 */
class RunCpanelRestoreTaskCommand extends Command
{
    protected $signature = 'jabali:tasks:run-cpanel-restore
        {--task-id= : Background task UUID}
        {--progress-log= : Path to write JSONL events to}
        {--cache-key= : Laravel cache key holding the serialized job args}';

    protected $description = 'Spawned binary: run a cPanel restore and emit task progress';

    public function handle(
        AgentClient $agent,
        MigrationDnsSyncService $dnsSyncService,
        MigrationEmailProvisionService $emailProvisionService,
    ): int {
        $logPath = (string) $this->option('progress-log');
        if ($logPath === '') {
            $this->error('--progress-log is required');

            return self::FAILURE;
        }

        $writer = new TaskLogWriter($logPath);
        $writer->started();

        $cacheKey = (string) $this->option('cache-key');
        $args = Cache::get($cacheKey);
        if (! is_array($args)) {
            $writer->failed("No cached args at key: {$cacheKey}");

            return self::FAILURE;
        }

        $writer->step('restoring user '.($args['username'] ?? 'unknown'));

        try {
            config(['jabali.agent_v2.cpanel_restore_enabled' => false]);

            $job = new RunCpanelRestore(
                jobId: (string) $args['job_id'],
                logPath: (string) $args['log_path'],
                backupPath: (string) $args['backup_path'],
                username: (string) $args['username'],
                restoreFiles: (bool) $args['restore_files'],
                restoreDatabases: (bool) $args['restore_databases'],
                restoreEmails: (bool) $args['restore_emails'],
                restoreSsl: (bool) $args['restore_ssl'],
                discoveredData: $args['discovered_data'] ?? null,
            );
            $job->handle($agent, $dnsSyncService, $emailProvisionService);
        } catch (\Throwable $e) {
            $writer->failed($e->getMessage());
            Cache::forget($cacheKey);

            return self::FAILURE;
        }

        // Job writes its own final state to Cache under getCacheKey(); read it back
        $statusKey = 'migration_status_'.$args['job_id'];
        $status = Cache::get($statusKey);
        Cache::forget($cacheKey);

        if (is_array($status) && ($status['status'] ?? '') === 'failed') {
            $writer->failed('Migration reported failure');

            return self::FAILURE;
        }

        $writer->done(['status' => $status['status'] ?? 'completed']);

        return self::SUCCESS;
    }
}
