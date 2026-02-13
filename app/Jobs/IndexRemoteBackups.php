<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\BackupDestination;
use App\Models\User;
use App\Models\UserRemoteBackup;
use App\Services\Agent\AgentClient;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Log;
use Exception;

class IndexRemoteBackups implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;
    public int $timeout = 600; // 10 minutes max

    public function __construct(
        public ?int $destinationId = null
    ) {}

    public function handle(): void
    {
        $agent = new AgentClient();

        // Get destinations to index
        $query = BackupDestination::where('is_server_backup', true)->where('is_active', true);
        if ($this->destinationId) {
            $query->where('id', $this->destinationId);
        }
        $destinations = $query->get();

        // Get all users for lookup
        $users = User::pluck('id', 'username')->toArray();

        foreach ($destinations as $destination) {
            try {
                $this->indexDestination($agent, $destination, $users);
            } catch (Exception $e) {
                Log::warning("IndexRemoteBackups: Failed to index destination {$destination->name}: " . $e->getMessage());
            }
        }

        Log::info('IndexRemoteBackups: Indexing completed');
    }

    protected function indexDestination(AgentClient $agent, BackupDestination $destination, array $users): void
    {
        $config = array_merge($destination->config ?? [], ['type' => $destination->type]);

        // List root directory to get all backup timestamps
        $result = $agent->send('backup.list_remote', [
            'destination' => $config,
            'path' => '',
        ]);

        if (!($result['success'] ?? false) || empty($result['files'])) {
            return;
        }

        $indexedBackups = [];

        foreach ($result['files'] as $file) {
            if (!$file['is_directory']) {
                continue;
            }

            $backupName = basename($file['name']);

            // Check for timestamp directories (incremental backups: 2026-01-19_210219)
            if (!preg_match('/^\d{4}-\d{2}-\d{2}_\d{6}$/', $backupName)) {
                continue;
            }

            // List the backup directory to find user subdirectories
            $backupContents = $agent->send('backup.list_remote', [
                'destination' => $config,
                'path' => $backupName,
            ]);

            if (!($backupContents['success'] ?? false) || empty($backupContents['files'])) {
                continue;
            }

            // Check each subdirectory to see if it's a user
            foreach ($backupContents['files'] as $subFile) {
                if (!$subFile['is_directory']) {
                    continue;
                }

                $username = basename($subFile['name']);

                // Skip . and ..
                if ($username === '.' || $username === '..') {
                    continue;
                }

                // Check if this is a valid user
                if (!isset($users[$username])) {
                    continue;
                }

                $userId = $users[$username];
                $backupPath = $backupName . '/' . $username;
                $backupDate = UserRemoteBackup::parseBackupDate($backupName);

                // Upsert the backup record
                UserRemoteBackup::updateOrCreate(
                    [
                        'user_id' => $userId,
                        'destination_id' => $destination->id,
                        'backup_name' => $backupName,
                    ],
                    [
                        'backup_path' => $backupPath,
                        'backup_type' => 'incremental',
                        'backup_date' => $backupDate,
                        'indexed_at' => now(),
                    ]
                );

                $indexedBackups[] = "$username/$backupName";
            }
        }

        Log::info("IndexRemoteBackups: Indexed " . count($indexedBackups) . " backups from {$destination->name}");

        // Clean up old entries that no longer exist on the remote
        // (backups that were deleted via retention policy)
        $this->cleanupDeletedBackups($destination->id, $result['files']);
    }

    protected function cleanupDeletedBackups(int $destinationId, array $remoteFiles): void
    {
        // Get all backup names that exist on remote
        $remoteBackupNames = [];
        foreach ($remoteFiles as $file) {
            if ($file['is_directory']) {
                $name = basename($file['name']);
                if (preg_match('/^\d{4}-\d{2}-\d{2}_\d{6}$/', $name)) {
                    $remoteBackupNames[] = $name;
                }
            }
        }

        // Delete index entries for backups that no longer exist
        if (!empty($remoteBackupNames)) {
            $deleted = UserRemoteBackup::where('destination_id', $destinationId)
                ->whereNotIn('backup_name', $remoteBackupNames)
                ->delete();

            if ($deleted > 0) {
                Log::info("IndexRemoteBackups: Cleaned up {$deleted} deleted backup entries");
            }
        }
    }
}
