<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Jobs\IndexRemoteBackups;
use App\Models\Backup;
use App\Models\BackupSchedule;
use App\Services\AdminNotificationService;
use App\Services\Agent\AgentClient;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Log;
use Exception;

class RunServerBackup implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;
    public int $timeout = 3600; // 1 hour max

    public function __construct(
        public int $backupId
    ) {}

    public function handle(): void
    {
        $backup = Backup::find($this->backupId);

        if (!$backup) {
            Log::warning("RunServerBackup: Backup {$this->backupId} not found");
            return;
        }

        // Skip if already completed or failed
        if (in_array($backup->status, ['completed', 'failed'])) {
            Log::info("RunServerBackup: Backup {$this->backupId} already {$backup->status}");
            return;
        }

        $backup->update(['status' => 'running', 'started_at' => now()]);

        $backupType = $backup->metadata['backup_type'] ?? 'full';
        $isIncrementalRemote = $backupType === 'incremental' && $backup->destination_id;

        try {
            $agent = new AgentClient();

            if ($isIncrementalRemote) {
                $destination = $backup->destination;
                if (!$destination) {
                    throw new Exception('Backup destination not found');
                }

                $config = array_merge($destination->config ?? [], ['type' => $destination->type]);

                $result = $agent->send('backup.incremental_direct', [
                    'destination' => $config,
                    'users' => $backup->users,
                    'include_files' => $backup->include_files,
                    'include_databases' => $backup->include_databases,
                    'include_mailboxes' => $backup->include_mailboxes,
                    'include_dns' => $backup->include_dns,
                ]);

                if ($result['success'] ?? false) {
                    $backup->update([
                        'status' => 'completed',
                        'completed_at' => now(),
                        'size_bytes' => $result['size'] ?? 0,
                        'users' => $result['users'] ?? $backup->users,
                        'remote_path' => $result['remote_path'] ?? null,
                        'metadata' => array_merge($backup->metadata ?? [], [
                            'user_count' => $result['user_count'] ?? 0,
                            'previous_backup' => $result['previous_backup'] ?? null,
                            'is_initialization' => $result['is_initialization'] ?? false,
                        ]),
                    ]);

                    Log::info("RunServerBackup: Incremental backup {$this->backupId} completed");

                    // Re-index remote backups for user discovery
                    IndexRemoteBackups::dispatch($backup->destination_id);

                    // Apply retention policy if this backup is from a schedule
                    $this->applyRetention($backup);

                    // Send success notification
                    AdminNotificationService::backupSuccess(
                        $backup->name,
                        $result['size'] ?? 0,
                        $backup->destination?->name
                    );
                } else {
                    throw new Exception($result['error'] ?? 'Incremental backup failed');
                }
            } else {
                // Full backup
                $outputPath = $backup->local_path;

                $result = $agent->send('backup.create_server', [
                    'output_path' => $outputPath,
                    'backup_type' => $backupType,
                    'users' => $backup->users,
                    'include_files' => $backup->include_files,
                    'include_databases' => $backup->include_databases,
                    'include_mailboxes' => $backup->include_mailboxes,
                    'include_dns' => $backup->include_dns,
                ]);

                if ($result['success'] ?? false) {
                    $backup->update([
                        'status' => 'completed',
                        'completed_at' => now(),
                        'size_bytes' => $result['size'] ?? 0,
                        'users' => $result['users'] ?? $backup->users,
                        'metadata' => array_merge($backup->metadata ?? [], [
                            'user_count' => $result['user_count'] ?? 0,
                        ]),
                    ]);

                    // Upload to remote if destination configured
                    if ($backup->destination_id) {
                        $this->uploadToRemote($backup, $agent);
                    }

                    Log::info("RunServerBackup: Full backup {$this->backupId} completed");

                    // Apply retention policy if this backup is from a schedule
                    $this->applyRetention($backup);

                    // Send success notification
                    AdminNotificationService::backupSuccess(
                        $backup->name,
                        $result['size'] ?? 0,
                        $backup->destination?->name
                    );
                } else {
                    throw new Exception($result['error'] ?? 'Backup failed');
                }
            }
        } catch (Exception $e) {
            $backup->update([
                'status' => 'failed',
                'completed_at' => now(),
                'error_message' => $e->getMessage(),
            ]);

            Log::error("RunServerBackup: Backup {$this->backupId} failed: " . $e->getMessage());

            // Send failure notification
            AdminNotificationService::backupFailure($backup->name, $e->getMessage());
        }
    }

    protected function uploadToRemote(Backup $backup, AgentClient $agent): void
    {
        if (!$backup->destination || !$backup->local_path) {
            return;
        }

        try {
            $backup->update(['status' => 'uploading']);

            $config = array_merge(
                $backup->destination->config ?? [],
                ['type' => $backup->destination->type]
            );
            $backupType = $backup->metadata['backup_type'] ?? 'full';

            $result = $agent->send('backup.upload_remote', [
                'local_path' => $backup->local_path,
                'destination' => $config,
                'backup_type' => $backupType,
            ]);

            if ($result['success'] ?? false) {
                $backup->update([
                    'status' => 'completed',
                    'remote_path' => $result['remote_path'] ?? null,
                ]);

                Log::info("RunServerBackup: Uploaded backup {$this->backupId} to remote");

                // Re-index remote backups for user discovery
                IndexRemoteBackups::dispatch($backup->destination_id);
            } else {
                // Keep as completed since local exists, just log warning
                $backup->update([
                    'status' => 'completed',
                    'error_message' => 'Remote upload failed: ' . ($result['error'] ?? 'Unknown error'),
                ]);

                Log::warning("RunServerBackup: Remote upload failed for {$this->backupId}");
            }
        } catch (Exception $e) {
            $backup->update([
                'status' => 'completed',
                'error_message' => 'Remote upload failed: ' . $e->getMessage(),
            ]);

            Log::warning("RunServerBackup: Remote upload exception for {$this->backupId}: " . $e->getMessage());
        }
    }

    protected function applyRetention(Backup $backup): void
    {
        Log::info("RunServerBackup: applyRetention called for backup {$backup->id}, schedule_id: " . ($backup->schedule_id ?? 'NULL'));

        // Only apply retention if backup has a schedule
        if (!$backup->schedule_id) {
            Log::info("RunServerBackup: No schedule_id, skipping retention");
            return;
        }

        $schedule = BackupSchedule::find($backup->schedule_id);
        if (!$schedule) {
            Log::info("RunServerBackup: Schedule not found for id {$backup->schedule_id}");
            return;
        }

        $retentionCount = $schedule->retention_count ?? 7;
        Log::info("RunServerBackup: Retention count is {$retentionCount}");

        // Get backups from this schedule, ordered by date
        $backups = Backup::where('schedule_id', $schedule->id)
            ->where('status', 'completed')
            ->orderByDesc('created_at')
            ->get();

        if ($backups->count() <= $retentionCount) {
            return;
        }

        // Get backups to delete (keep newest $retentionCount)
        $toDelete = $backups->slice($retentionCount);
        $agent = new AgentClient();

        foreach ($toDelete as $oldBackup) {
            Log::info("RunServerBackup: Deleting old backup per retention: {$oldBackup->name}");

            // Delete local file/folder
            if ($oldBackup->local_path && file_exists($oldBackup->local_path)) {
                if (is_file($oldBackup->local_path)) {
                    unlink($oldBackup->local_path);
                } else {
                    exec("rm -rf " . escapeshellarg($oldBackup->local_path));
                }
            }

            // Delete from remote if exists
            if ($oldBackup->remote_path && $oldBackup->destination) {
                try {
                    $config = array_merge(
                        $oldBackup->destination->config ?? [],
                        ['type' => $oldBackup->destination->type]
                    );
                    $agent->send('backup.delete_remote', [
                        'remote_path' => $oldBackup->remote_path,
                        'destination' => $config,
                    ]);
                } catch (Exception $e) {
                    Log::warning("RunServerBackup: Failed to delete remote backup: " . $e->getMessage());
                }
            }

            $oldBackup->delete();
        }

        Log::info("RunServerBackup: Deleted " . $toDelete->count() . " old backup(s) per retention policy");
    }
}
