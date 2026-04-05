<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\Backup;
use App\Services\AdminNotificationService;
use App\Services\Backup\BackupOrchestrator;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Log;
use Throwable;

class RunServerBackup implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 3600;

    public function __construct(
        public int $backupId
    ) {}

    public function handle(BackupOrchestrator $orchestrator): void
    {
        $backup = Backup::find($this->backupId);

        if (! $backup) {
            Log::warning("RunServerBackup: Backup {$this->backupId} not found");

            return;
        }

        $orchestrator->executePerAccount($backup);
    }

    public function failed(Throwable $exception): void
    {
        $backup = Backup::find($this->backupId);

        if ($backup) {
            $backup->update([
                'status' => 'failed',
                'error_message' => $exception->getMessage(),
                'completed_at' => now(),
            ]);

            AdminNotificationService::backupFailure($backup->name ?? "Backup #{$this->backupId}", $exception->getMessage());
        }

        Log::error('RunServerBackup job failed', [
            'backup_id' => $this->backupId,
            'error' => $exception->getMessage(),
        ]);
    }
}
