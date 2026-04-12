<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Migration\MigrationDnsSyncService;
use App\Services\Migration\MigrationEmailProvisionService;
use Exception;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\Log;
use Throwable;

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

    public function handle(AgentClient $agent, MigrationDnsSyncService $dnsSyncService, MigrationEmailProvisionService $emailProvisionService): void
    {
        $this->ensureLogPath();

        // Agent v2 path (ADR-0007): dispatch a BackgroundTask. Constructor
        // args are stashed in cache keyed by the job id so the dashboard's
        // payload column doesn't show backup paths (they can contain a user
        // home directory which is less sensitive but we treat them carefully
        // anyway).
        if (config('jabali.agent_v2.cpanel_restore_enabled')) {
            $cacheKey = 'bg-task:cpanel-restore:'.$this->jobId;
            Cache::put($cacheKey, [
                'job_id' => $this->jobId,
                'log_path' => $this->logPath,
                'backup_path' => $this->backupPath,
                'username' => $this->username,
                'restore_files' => $this->restoreFiles,
                'restore_databases' => $this->restoreDatabases,
                'restore_emails' => $this->restoreEmails,
                'restore_ssl' => $this->restoreSsl,
                'discovered_data' => $this->discoveredData,
            ], now()->addHours(3));

            $dispatcher = app(\App\Services\BackgroundTasks\BackgroundTaskDispatcher::class);
            $task = $dispatcher->dispatch(
                type: \App\Enums\BackgroundTaskType::CpanelRestore,
                argv: [
                    '/usr/bin/php',
                    base_path('artisan'),
                    'jabali:tasks:run-cpanel-restore',
                    '--cache-key='.$cacheKey,
                ],
                payload: ['job_id' => $this->jobId, 'username' => $this->username],
                dedupeKey: 'cpanel-restore:'.$this->jobId,
                limits: ['cpu' => 75, 'memory' => '2G', 'io' => 100],
            );
            if ($task === null) {
                Log::info("RunCpanelRestore: dedupe — restore already running for {$this->jobId}");
            }

            return;
        }

        $this->appendLog(__('Restore started for user: :user', ['user' => $this->username]), 'pending');
        Cache::put($this->getCacheKey(), ['status' => 'running'], now()->addHours(2));

        try {
            $result = $agent->call('cpanel.restore_backup', [
                'backup_path' => $this->backupPath,
                'username' => $this->username,
                'restore_files' => $this->restoreFiles,
                'restore_databases' => $this->restoreDatabases,
                'restore_emails' => $this->restoreEmails,
                'restore_ssl' => $this->restoreSsl,
                'discovered_data' => $this->discoveredData,
                'log_path' => $this->logPath,
            ]);

            if ($result->success) {
                $this->appendLog(__('Migration completed successfully.'), 'success');
                Cache::put($this->getCacheKey(), ['status' => 'completed'], now()->addHours(2));
                $this->syncDnsZones($dnsSyncService);
                $this->provisionEmails($emailProvisionService);

                return;
            }

            $error = $result->error ?? __('Migration failed');
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

    public function failed(Throwable $exception): void
    {
        Cache::put($this->getCacheKey(), ['status' => 'failed'], now()->addHours(2));
        $this->appendLog(__('Restore job failed: :error', ['error' => $exception->getMessage()]), 'error');

        Log::error('RunCpanelRestore job failed', [
            'job_id' => $this->jobId,
            'user' => $this->username,
            'error' => $exception->getMessage(),
        ]);
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

    protected function provisionEmails(MigrationEmailProvisionService $emailProvisionService): void
    {
        if (! $this->restoreEmails) {
            return;
        }

        $mailboxes = $this->discoveredData['mailboxes'] ?? [];
        $forwarders = $this->discoveredData['forwarders'] ?? [];

        if (empty($mailboxes) && empty($forwarders)) {
            return;
        }

        $user = User::where('username', $this->username)->first();
        if (! $user) {
            Log::warning('Unable to provision emails after cPanel restore: user not found', [
                'username' => $this->username,
            ]);

            return;
        }

        try {
            $this->appendLog(__('Provisioning email accounts...'), 'pending');
            $result = $emailProvisionService->provisionFromDiscoveredData(
                $user,
                $this->discoveredData,
                fn (string $msg, string $status) => $this->appendLog($msg, $status),
            );

            $summary = __(':mb mailbox(es), :fw forwarder(s)', [
                'mb' => count($result->mailboxes),
                'fw' => count($result->forwarders),
            ]);

            if (! empty($result->errors)) {
                $summary .= ', '.__(':err error(s)', ['err' => count($result->errors)]);
            }

            $this->appendLog(__('Email provisioning complete: :summary', ['summary' => $summary]), empty($result->errors) ? 'success' : 'warning');
        } catch (Exception $e) {
            Log::warning('Failed to provision emails after cPanel restore', [
                'user' => $this->username,
                'error' => $e->getMessage(),
            ]);
            $this->appendLog(__('Email provisioning failed: :error', ['error' => $e->getMessage()]), 'warning');
        }
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
