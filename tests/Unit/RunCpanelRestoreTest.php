<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Jobs\RunCpanelRestore;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Migration\MigrationDnsSyncService;
use Exception;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\File;
use Tests\TestCase;

class RunCpanelRestoreTest extends TestCase
{
    use RefreshDatabase;

    public function test_it_marks_restore_completed_and_logs_success(): void
    {
        $jobId = 'cpanel-restore-success';
        $logPath = storage_path('framework/testing/cpanel-restore-success.log');

        File::ensureDirectoryExists(dirname($logPath));
        File::put($logPath, '');

        User::factory()->create([
            'username' => 'example',
        ]);

        $this->app->instance(AgentClient::class, new class extends AgentClient
        {
            public function send(string $action, array $params = []): array
            {
                return ['success' => true];
            }
        });

        $job = new RunCpanelRestore(
            jobId: $jobId,
            logPath: $logPath,
            backupPath: '/tmp/backup.tar.gz',
            username: 'example',
            restoreFiles: true,
            restoreDatabases: true,
            restoreEmails: true,
            restoreSsl: true,
            discoveredData: null,
        );

        $job->handle(
            $this->app->make(AgentClient::class),
            $this->app->make(MigrationDnsSyncService::class),
        );

        $status = Cache::get('cpanel_restore_status_'.$jobId);
        $this->assertSame('completed', $status['status'] ?? null);

        $lines = file($logPath, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES);
        $this->assertNotEmpty($lines);

        $lastEntry = json_decode(end($lines), true);
        $this->assertSame('success', $lastEntry['status'] ?? null);
    }

    public function test_it_marks_restore_failed_on_exception(): void
    {
        $jobId = 'cpanel-restore-failed';
        $logPath = storage_path('framework/testing/cpanel-restore-failed.log');

        File::ensureDirectoryExists(dirname($logPath));
        File::put($logPath, '');

        User::factory()->create([
            'username' => 'example',
        ]);

        $this->app->instance(AgentClient::class, new class extends AgentClient
        {
            public function send(string $action, array $params = []): array
            {
                throw new Exception('boom');
            }
        });

        $job = new RunCpanelRestore(
            jobId: $jobId,
            logPath: $logPath,
            backupPath: '/tmp/backup.tar.gz',
            username: 'example',
            restoreFiles: true,
            restoreDatabases: true,
            restoreEmails: true,
            restoreSsl: true,
            discoveredData: null,
        );

        $job->handle(
            $this->app->make(AgentClient::class),
            $this->app->make(MigrationDnsSyncService::class),
        );

        $status = Cache::get('cpanel_restore_status_'.$jobId);
        $this->assertSame('failed', $status['status'] ?? null);
    }
}
