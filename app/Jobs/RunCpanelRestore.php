<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Migration\MigrationDnsSyncService;
use Exception;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\Log;

class RunCpanelRestore implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 3600; // 1 hour max

    /**
     * @param  array<string, mixed>|null  $discoveredData
     */
    public function __construct(
        public string $jobId,
        public string $logPath,
        public string $backupPath,
        public string $username,
        public bool $restoreFiles,
        public bool $restoreDatabases,
        public bool $restoreEmails,
        public bool $restoreSsl,
        public ?array $discoveredData = null,
    ) {}

    public function handle(AgentClient $agent, MigrationDnsSyncService $dnsSyncService): void
    {
        $this->ensureLogPath();
        $this->appendLog(__('Restore started for user: :user', ['user' => $this->username]), 'pending');
        Cache::put($this->getCacheKey(), ['status' => 'running'], now()->addHours(2));

        try {
            $result = $agent->send('cpanel.restore_backup', [
                'backup_path' => $this->backupPath,
                'username' => $this->username,
                'restore_files' => $this->restoreFiles,
                'restore_databases' => $this->restoreDatabases,
                'restore_emails' => $this->restoreEmails,
                'restore_ssl' => $this->restoreSsl,
                'discovered_data' => $this->discoveredData,
                'log_path' => $this->logPath,
            ]);

            if ($result['success'] ?? false) {
                $this->appendLog(__('Migration completed successfully.'), 'success');
                Cache::put($this->getCacheKey(), ['status' => 'completed'], now()->addHours(2));
                $this->syncDnsZones($dnsSyncService);

                return;
            }

            $error = $result['error'] ?? __('Migration failed');
            $this->appendLog(__('Migration failed: :error', ['error' => $error]), 'error');
            Cache::put($this->getCacheKey(), ['status' => 'failed'], now()->addHours(2));
        } catch (Exception $e) {
            $this->appendLog(__('Migration failed: :error', ['error' => $e->getMessage()]), 'error');
            Cache::put($this->getCacheKey(), ['status' => 'failed'], now()->addHours(2));
            Log::error('RunCpanelRestore failed', [
                'job_id' => $this->jobId,
                'user' => $this->username,
                'error' => $e->getMessage(),
            ]);
        }
    }

    protected function ensureLogPath(): void
    {
        File::ensureDirectoryExists(dirname($this->logPath));
        if (! File::exists($this->logPath)) {
            File::put($this->logPath, '');
            @chmod($this->logPath, 0644);
        }
    }

    protected function appendLog(string $message, string $status): void
    {
        $entry = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];

        file_put_contents($this->logPath, json_encode($entry).PHP_EOL, FILE_APPEND | LOCK_EX);
        @chmod($this->logPath, 0644);
    }

    protected function getCacheKey(): string
    {
        return 'cpanel_restore_status_'.$this->jobId;
    }

    protected function syncDnsZones(MigrationDnsSyncService $dnsSyncService): void
    {
        $user = User::where('username', $this->username)->first();
        if (! $user) {
            Log::warning('Unable to sync DNS zones after cPanel restore: user not found', [
                'username' => $this->username,
            ]);

            return;
        }

        try {
            $domains = $this->discoveredData['domains'] ?? null;
            $dnsSyncService->syncDomainsForUser($user, $domains);
        } catch (Exception $e) {
            Log::warning('Failed to sync DNS zones after cPanel restore', [
                'user' => $this->username,
                'error' => $e->getMessage(),
            ]);
        }
    }
}
