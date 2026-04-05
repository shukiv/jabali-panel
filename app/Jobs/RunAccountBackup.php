<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\Backup;
use App\Models\BackupDestination;
use App\Services\Agent\AgentClient;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Log;
use Throwable;

class RunAccountBackup implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 1800;

    public function __construct(
        public int $backupId
    ) {}

    public function handle(AgentClient $agent): void
    {
        $backup = Backup::with(['user', 'destination'])->find($this->backupId);

        if (! $backup || ! $backup->user) {
            Log::warning("RunAccountBackup: Backup {$this->backupId} not found or no user");

            return;
        }

        $backup->update(['status' => 'running', 'started_at' => now()]);

        $repo = $backup->destination
            ? $backup->destination->getResticRepoUrl()
            : BackupDestination::defaultRepo();
        $destConfig = $backup->destination
            ? array_merge($backup->destination->config ?? [], ['type' => $backup->destination->type])
            : [];

        $result = $agent->send('backup.create_user_snapshot', [
            'username' => $backup->user->username,
            'include_files' => $backup->include_files ?? true,
            'include_databases' => $backup->include_databases ?? true,
            'include_mailboxes' => $backup->include_mailboxes ?? true,
            'include_dns' => $backup->include_dns ?? true,
            'include_ssl' => $backup->include_ssl ?? true,
            'destination' => $destConfig,
            'repo' => $repo,
        ]);

        if (! ($result['success'] ?? false)) {
            throw new \Exception($result['error'] ?? 'Backup failed');
        }

        $backup->update([
            'status' => 'completed',
            'completed_at' => now(),
            'snapshot_id' => $result['snapshot_id'] ?? null,
            'size_bytes' => $result['size_bytes'] ?? 0,
            'file_count' => $result['file_count'] ?? 0,
            'domains' => $result['domains'] ?? null,
            'databases' => $result['databases'] ?? null,
            'mailboxes' => $result['mailboxes'] ?? null,
        ]);

        Log::info("RunAccountBackup: {$backup->user->username} completed (snapshot: ".($result['snapshot_id'] ?? 'unknown').')');
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
        }

        Log::error("RunAccountBackup: Backup {$this->backupId} failed: ".$exception->getMessage());
    }
}
