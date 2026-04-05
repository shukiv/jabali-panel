<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\Backup;
use App\Models\User;
use App\Services\AdminNotificationService;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Log;
use Throwable;

class RunServerBackup implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 60;

    public function __construct(
        public int $backupId
    ) {}

    public function handle(): void
    {
        $parentBackup = Backup::find($this->backupId);

        if (! $parentBackup) {
            Log::warning("RunServerBackup: Backup {$this->backupId} not found");

            return;
        }

        $users = ! empty($parentBackup->users)
            ? User::whereIn('username', $parentBackup->users)->where('is_active', true)->get()
            : User::where('is_active', true)->get();

        if ($users->isEmpty()) {
            $parentBackup->update(['status' => 'failed', 'error_message' => 'No active users to backup', 'completed_at' => now()]);

            return;
        }

        $timestamp = now()->format('Y-m-d_His');

        foreach ($users as $user) {
            $backup = Backup::create([
                'user_id' => $user->id,
                'name' => $parentBackup->name.' — '.$user->username,
                'filename' => $user->username.'_'.$timestamp.'.tar.gz',
                'type' => 'account',
                'status' => 'pending',
                'destination_id' => $parentBackup->destination_id,
                'schedule_id' => $parentBackup->schedule_id,
                'include_files' => $parentBackup->include_files ?? true,
                'include_databases' => $parentBackup->include_databases ?? true,
                'include_mailboxes' => $parentBackup->include_mailboxes ?? true,
                'include_dns' => $parentBackup->include_dns ?? true,
                'include_ssl' => $parentBackup->include_ssl ?? true,
            ]);

            RunAccountBackup::dispatch($backup->id);
        }

        Log::info("RunServerBackup: Dispatched {$users->count()} account backup jobs");

        // Delete the parent placeholder
        $parentBackup->delete();
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
