<?php

declare(strict_types=1);

namespace App\Services\Backup;

use App\Jobs\IndexRemoteBackups;
use App\Models\Backup;
use App\Models\BackupDestination;
use App\Models\BackupRestore;
use App\Models\BackupSchedule;
use App\Models\User;
use App\Services\AdminNotificationService;
use App\Services\Agent\AgentClient;
use Exception;
use Illuminate\Support\Facades\Log;

class BackupOrchestrator
{
    public function __construct(private AgentClient $agent) {}

    /**
     * Execute a server backup — creates one restic snapshot per user account.
     *
     * @return array{completed: int, failed: int, backups: list<Backup>}
     */
    public function executePerAccount(Backup $parentBackup): array
    {
        $repo = $parentBackup->destination
            ? $parentBackup->destination->getResticRepoUrl()
            : BackupDestination::defaultRepo();
        $destConfig = $parentBackup->destination
            ? array_merge($parentBackup->destination->config ?? [], ['type' => $parentBackup->destination->type])
            : [];

        // Get users to back up
        $users = ! empty($parentBackup->users)
            ? User::whereIn('username', $parentBackup->users)->where('is_active', true)->get()
            : User::where('is_active', true)->get();

        $completed = 0;
        $failed = 0;
        $backups = [];
        $totalSize = 0;

        foreach ($users as $user) {
            $timestamp = now()->format('Y-m-d_His');
            $backup = Backup::create([
                'user_id' => $user->id,
                'name' => $parentBackup->name.' — '.$user->username,
                'filename' => $user->username.'_'.$timestamp.'.tar.gz',
                'type' => 'account',
                'status' => 'running',
                'started_at' => now(),
                'destination_id' => $parentBackup->destination_id,
                'schedule_id' => $parentBackup->schedule_id,
                'include_files' => $parentBackup->include_files ?? true,
                'include_databases' => $parentBackup->include_databases ?? true,
                'include_mailboxes' => $parentBackup->include_mailboxes ?? true,
                'include_dns' => $parentBackup->include_dns ?? true,
                'include_ssl' => $parentBackup->include_ssl ?? true,
            ]);

            try {
                $result = $this->agent->send('backup.create_user_snapshot', [
                    'username' => $user->username,
                    'include_files' => $parentBackup->include_files,
                    'include_databases' => $parentBackup->include_databases,
                    'include_mailboxes' => $parentBackup->include_mailboxes,
                    'include_dns' => $parentBackup->include_dns,
                    'include_ssl' => $parentBackup->include_ssl ?? true,
                    'destination' => $destConfig,
                    'repo' => $repo,
                ]);

                if (! ($result['success'] ?? false)) {
                    throw new Exception($result['error'] ?? 'Backup failed');
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

                $totalSize += $result['size_bytes'] ?? 0;
                $completed++;
                Log::info("BackupOrchestrator: Account backup for {$user->username} completed (snapshot: ".($result['snapshot_id'] ?? 'unknown').')');
            } catch (Exception $e) {
                $backup->update([
                    'status' => 'failed',
                    'completed_at' => now(),
                    'error_message' => $e->getMessage(),
                ]);
                $failed++;
                Log::error("BackupOrchestrator: Account backup for {$user->username} failed: ".$e->getMessage());
            }

            $backups[] = $backup;
        }

        // Delete the parent placeholder
        $parentBackup->delete();

        // Index snapshots for discovery
        if ($parentBackup->destination_id) {
            IndexRemoteBackups::dispatch($parentBackup->destination_id);
        }

        // Apply retention per-user
        if ($parentBackup->schedule_id) {
            $schedule = BackupSchedule::find($parentBackup->schedule_id);
            if ($schedule) {
                $this->applyRetention($schedule, $repo);
            }
        }

        if ($failed === 0) {
            AdminNotificationService::backupSuccess(
                $parentBackup->name,
                $totalSize,
                $parentBackup->destination?->name
            );
        } else {
            AdminNotificationService::backupFailure(
                $parentBackup->name,
                "{$failed} account(s) failed, {$completed} succeeded"
            );
        }

        return ['completed' => $completed, 'failed' => $failed, 'backups' => $backups];
    }

    /**
     * Execute a server backup — alias for executePerAccount.
     */
    public function execute(Backup $backup): void
    {
        $this->executePerAccount($backup);
    }

    /**
     * Build the destination config array for agent calls.
     *
     * @return array<string, mixed>
     */
    public function buildDestinationConfig(BackupDestination $destination): array
    {
        return array_merge($destination->config ?? [], ['type' => $destination->type]);
    }

    /**
     * Test a backup destination connection.
     *
     * @return array<string, mixed>
     */
    public function testDestination(BackupDestination $destination): array
    {
        $config = $this->buildDestinationConfig($destination);
        $result = $this->agent->send('backup.test_destination', ['destination' => $config]);

        $destination->update([
            'last_tested_at' => now(),
            'test_status' => ($result['success'] ?? false) ? 'success' : 'failed',
            'test_message' => $result['message'] ?? $result['error'] ?? null,
        ]);

        if ($result['success'] ?? false) {
            Log::info("BackupDestination: Test passed for '{$destination->name}'");
        } else {
            Log::warning("BackupDestination: Test failed for '{$destination->name}': ".($result['error'] ?? 'Unknown error'));
        }

        return $result;
    }

    /**
     * Upload a backup to its remote destination.
     */
    public function uploadToRemote(Backup $backup, bool $keepLocal = false): bool
    {
        if (! $backup->destination || ! $backup->local_path) {
            return false;
        }

        try {
            $backup->update(['status' => 'uploading']);

            $config = $this->buildDestinationConfig($backup->destination);
            $backupType = $backup->metadata['backup_type'] ?? 'full';

            $result = $this->agent->backupUploadRemote($backup->local_path, $config, $backupType);

            if ($result['success'] ?? false) {
                $backup->update([
                    'status' => 'completed',
                    'remote_path' => $result['remote_path'] ?? null,
                ]);

                if (! $keepLocal && $backup->local_path) {
                    $this->agent->backupDeleteServer($backup->local_path);
                    $backup->update(['local_path' => null]);
                }

                Log::info("BackupOrchestrator: Uploaded backup {$backup->id} to remote");

                IndexRemoteBackups::dispatch($backup->destination_id);

                return true;
            } else {
                throw new Exception($result['error'] ?? 'Upload failed');
            }
        } catch (Exception $e) {
            $backup->update([
                'status' => 'completed',
                'error_message' => 'Remote upload failed: '.$e->getMessage(),
            ]);

            Log::warning("BackupOrchestrator: Remote upload failed for backup {$backup->id}: ".$e->getMessage());

            return false;
        }
    }

    /**
     * Delete a backup (local file, remote file, and DB record).
     *
     * @throws Exception If a local file path is invalid
     */
    public function deleteBackup(Backup $backup): void
    {
        // Delete Restic snapshot if available
        if ($backup->snapshot_id) {
            try {
                $repo = $backup->destination
                    ? $backup->destination->getResticRepoUrl()
                    : BackupDestination::defaultRepo();

                $destConfig = $backup->destination
                    ? $this->buildDestinationConfig($backup->destination)
                    : [];

                $this->agent->send('backup.delete', [
                    'snapshot_id' => $backup->snapshot_id,
                    'destination' => $destConfig,
                    'repo' => $repo,
                ]);
            } catch (Exception $e) {
                Log::warning('BackupOrchestrator: Failed to delete Restic snapshot: '.$e->getMessage());
            }
        } elseif ($backup->local_path) {
            // Legacy tar.gz deletion
            $this->validatePath($backup->local_path);
            $this->agent->backupDeleteServer($backup->local_path);
        }

        $backup->delete();
    }

    /**
     * Apply retention policy using Restic forget.
     */
    public function applyRetention(BackupSchedule $schedule, ?string $repo = null): int
    {
        $retentionCount = $schedule->retention_count ?? 7;
        $repo = $repo ?: ($schedule->destination ? $schedule->destination->getResticRepoUrl() : BackupDestination::defaultRepo());

        // Let Restic handle retention natively
        try {
            $this->agent->send('backup.forget', [
                'repo' => $repo,
                'keep_last' => $retentionCount,
            ]);
        } catch (Exception $e) {
            Log::warning('BackupOrchestrator: Restic forget failed: '.$e->getMessage());
        }

        // Also clean up old DB records beyond retention
        $backups = Backup::where('schedule_id', $schedule->id)
            ->where('status', 'completed')
            ->orderByDesc('created_at')
            ->get();

        if ($backups->count() <= $retentionCount) {
            return 0;
        }

        $toDelete = $backups->slice($retentionCount);
        $deletedCount = 0;

        foreach ($toDelete as $oldBackup) {
            $oldBackup->delete();
            $deletedCount++;
        }

        Log::info("BackupOrchestrator: Pruned {$deletedCount} old backup record(s)");

        return $deletedCount;
    }

    /**
     * Get the manifest for a backup, optionally filtered to a specific user.
     *
     * @return array<string, mixed>
     */
    public function getBackupManifest(Backup $backup, ?string $forUser = null): array
    {
        $backupPath = $backup->local_path;

        if (! $backupPath || ! file_exists($backupPath)) {
            return [
                'username' => $forUser ?? ($backup->users[0] ?? ''),
                'domains' => $backup->domains ?? [],
                'databases' => $backup->databases ?? [],
                'mailboxes' => $backup->mailboxes ?? [],
                'mysql_users' => $backup->metadata['mysql_users'] ?? [],
                'ssl_certificates' => $backup->ssl_certificates ?? [],
                'dns_zones' => $backup->dns_zones ?? [],
                'users' => $backup->users ?? [],
            ];
        }

        try {
            $result = $this->agent->backupGetInfo($backupPath);

            if ($result['success'] ?? false) {
                $manifest = $result['manifest'] ?? [];

                if (isset($manifest['users']) && is_array($manifest['users']) && $manifest['type'] === 'server') {
                    $userList = array_keys($manifest['users']);

                    if ($forUser === null) {
                        return [
                            'username' => $userList[0] ?? '',
                            'users' => $userList,
                            'type' => 'server',
                            'domains' => $this->aggregateFromUsers($manifest['users'], 'domains'),
                            'databases' => $this->aggregateFromUsers($manifest['users'], 'databases'),
                            'mailboxes' => $this->aggregateFromUsers($manifest['users'], 'mailboxes'),
                            'mysql_users' => [],
                            'ssl_certificates' => [],
                            'dns_zones' => [],
                        ];
                    }

                    if (isset($manifest['users'][$forUser])) {
                        $userData = $manifest['users'][$forUser];

                        return [
                            'username' => $forUser,
                            'users' => $userList,
                            'type' => 'server',
                            'domains' => $userData['domains'] ?? [],
                            'databases' => $userData['databases'] ?? [],
                            'mailboxes' => $userData['mailboxes'] ?? [],
                            'mysql_users' => [],
                            'ssl_certificates' => [],
                            'dns_zones' => [],
                        ];
                    }
                }

                return $manifest;
            }
        } catch (Exception $e) {
            // Fall back to stored data
        }

        return [
            'username' => $forUser ?? ($backup->users[0] ?? ''),
            'domains' => $backup->domains ?? [],
            'databases' => $backup->databases ?? [],
            'mailboxes' => $backup->mailboxes ?? [],
            'mysql_users' => [],
            'ssl_certificates' => [],
            'dns_zones' => [],
            'users' => $backup->users ?? [],
        ];
    }

    /**
     * Aggregate a key from multiple user data arrays.
     *
     * @param  array<string, array<string, mixed>>  $users
     * @return array<int, mixed>
     */
    private function aggregateFromUsers(array $users, string $key): array
    {
        $result = [];
        foreach ($users as $userData) {
            if (isset($userData[$key]) && is_array($userData[$key])) {
                $result = array_merge($result, $userData[$key]);
            }
        }

        return array_unique($result);
    }

    /**
     * Create a user-scoped backup via the agent.
     *
     * @param  array<string, mixed>  $data
     */
    public function createUserBackup(User $user, array $data): Backup
    {
        $timestamp = now()->format('Y-m-d_His');
        $filename = "backup_{$timestamp}.tar.gz";
        $outputPath = "/home/{$user->username}/backups/{$filename}";
        $destinationId = ! empty($data['destination_id']) ? (int) $data['destination_id'] : null;

        $backup = Backup::create([
            'user_id' => $user->id,
            'name' => $data['name'],
            'filename' => $filename,
            'type' => 'full',
            'include_files' => $data['include_files'] ?? true,
            'include_databases' => $data['include_databases'] ?? true,
            'include_mailboxes' => $data['include_mailboxes'] ?? true,
            'destination_id' => $destinationId,
            'status' => 'pending',
            'local_path' => $outputPath,
        ]);

        $backup->update(['status' => 'running', 'started_at' => now()]);

        $result = $this->agent->backupCreate($user->username, $outputPath, [
            'include_files' => $data['include_files'] ?? true,
            'include_databases' => $data['include_databases'] ?? true,
            'include_mailboxes' => $data['include_mailboxes'] ?? true,
            'include_ssl' => $data['include_ssl'] ?? true,
        ]);

        if (! ($result['success'] ?? false)) {
            $backup->update([
                'status' => 'failed',
                'completed_at' => now(),
                'error_message' => $result['error'] ?? 'Backup failed',
            ]);

            throw new Exception($result['error'] ?? 'Backup failed');
        }

        $backup->update([
            'status' => 'completed',
            'completed_at' => now(),
            'size_bytes' => $result['size'] ?? 0,
            'checksum' => $result['checksum'] ?? null,
            'domains' => $result['domains'] ?? null,
            'databases' => $result['databases'] ?? null,
            'mailboxes' => $result['mailboxes'] ?? null,
        ]);

        return $backup;
    }

    /**
     * Restore a user-scoped backup via the agent.
     *
     * @param  array<string, mixed>  $options
     * @return array{success: bool, result: array<string, mixed>, error: string}
     */
    public function restoreBackup(User $user, Backup $backup, array $options): array
    {
        if ($backup->user_id !== null && $backup->user_id !== $user->id) {
            return ['success' => false, 'result' => [], 'error' => 'Backup not found'];
        }

        $restore = BackupRestore::create([
            'backup_id' => $backup->id,
            'user_id' => $user->id,
            'restore_files' => $options['restore_files'] ?? true,
            'restore_databases' => $options['restore_databases'] ?? true,
            'restore_mailboxes' => $options['restore_mailboxes'] ?? true,
            'selected_domains' => ! empty($options['selected_domains']) ? $options['selected_domains'] : null,
            'selected_databases' => ! empty($options['selected_databases']) ? $options['selected_databases'] : null,
            'selected_mailboxes' => ! empty($options['selected_mailboxes']) ? $options['selected_mailboxes'] : null,
            'status' => 'pending',
        ]);

        try {
            $restore->markAsRunning();

            $repo = $backup->destination
                ? $backup->destination->getResticRepoUrl()
                : BackupDestination::defaultRepo();
            $destConfig = $backup->destination
                ? $this->buildDestinationConfig($backup->destination)
                : [];

            $result = $this->agent->send('backup.restore', [
                'snapshot_id' => $backup->snapshot_id,
                'username' => $user->username,
                'repo' => $repo,
                'destination' => $destConfig,
                'restore_files' => $options['restore_files'] ?? true,
                'restore_databases' => $options['restore_databases'] ?? true,
                'restore_mailboxes' => $options['restore_mailboxes'] ?? true,
                'selected_domains' => $options['selected_domains'] ?? null,
                'selected_databases' => $options['selected_databases'] ?? null,
                'selected_mailboxes' => $options['selected_mailboxes'] ?? null,
            ]);

            if ($result['success'] ?? false) {
                $restore->markAsCompleted($result['restored'] ?? []);

                return [
                    'success' => true,
                    'result' => $result,
                    'error' => '',
                ];
            }

            throw new Exception($result['error'] ?? 'Restore failed');
        } catch (Exception $e) {
            $restore->markAsFailed($e->getMessage());

            return [
                'success' => false,
                'result' => [],
                'error' => $e->getMessage(),
            ];
        }
    }

    /**
     * Download a user backup from a remote destination.
     */
    public function downloadFromRemote(User $user, int $destinationId, string $remotePath): Backup
    {
        $destination = BackupDestination::where('id', $destinationId)
            ->where('user_id', $user->id)
            ->first();

        if (! $destination) {
            throw new Exception('Destination not found');
        }

        $pathParts = explode('/', trim($remotePath, '/'));
        $backupDate = $pathParts[0] ?? now()->format('Y-m-d_His');
        $timestamp = now()->format('Y-m-d_His');
        $filename = "backup_{$timestamp}.tar.gz";
        $localPath = "/home/{$user->username}/backups/{$filename}";

        $config = $this->buildDestinationConfig($destination);

        $result = $this->agent->send('backup.download_user_archive', [
            'username' => $user->username,
            'remote_path' => $remotePath,
            'destination' => $config,
            'output_path' => $localPath,
        ]);

        if (! ($result['success'] ?? false)) {
            throw new Exception($result['error'] ?? 'Download failed');
        }

        return Backup::create([
            'user_id' => $user->id,
            'name' => "Server Backup ({$backupDate})",
            'filename' => $filename,
            'type' => 'full',
            'status' => 'completed',
            'local_path' => $localPath,
            'size_bytes' => $result['size'] ?? 0,
            'completed_at' => now(),
        ]);
    }

    /**
     * Test a user-scoped backup destination.
     *
     * @return array<string, mixed>
     */
    public function testUserDestination(User $user, int $destinationId): array
    {
        $destination = BackupDestination::where('id', $destinationId)
            ->where('user_id', $user->id)
            ->first();

        if (! $destination) {
            throw new Exception('Destination not found');
        }

        return $this->testDestination($destination);
    }

    /**
     * Delete a user-scoped backup (local file and DB record).
     */
    public function deleteUserBackup(User $user, Backup $backup): void
    {
        if ($backup->user_id !== $user->id) {
            throw new Exception('Backup not found');
        }

        if ($backup->local_path) {
            try {
                $this->agent->backupDelete($user->username, $backup->local_path);
            } catch (Exception $e) {
                Log::warning("BackupOrchestrator: Failed to delete local file for backup {$backup->id}: ".$e->getMessage());
            }
        }

        $backup->delete();
    }

    /**
     * Validate a backup path is safe to delete.
     *
     * @throws Exception If the path is invalid
     */
    private function validatePath(string $path): void
    {
        if (empty($path) || str_contains($path, '..') || (! str_starts_with($path, '/home/') && ! str_starts_with($path, '/var/backups/'))) {
            throw new Exception("Invalid backup path: {$path}");
        }
    }
}
