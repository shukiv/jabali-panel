<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskType;
use App\Jobs\RunCpanelRestore;
use App\Models\BackgroundTask;
use App\Services\Agent\AgentClientInterface;
use App\Services\Migration\MigrationDnsSyncService;
use App\Services\Migration\MigrationEmailProvisionService;
use Illuminate\Support\Facades\Cache;
use Mockery;
use Tests\TestCase;

class CpanelRestoreMigrationTest extends TestCase
{
    protected function tearDown(): void
    {
        Mockery::close();
        BackgroundTask::query()->delete();
        parent::tearDown();
    }

    public function test_dispatches_via_agent_v2_when_flag_on(): void
    {
        config(['jabali.agent_v2.cpanel_restore_enabled' => true]);

        // Ensure the log directory exists so ensureLogPath() doesn't fail
        $tmpLog = sys_get_temp_dir().'/cpanel-restore-'.uniqid().'.log';

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($p) {
                return $p['type'] === BackgroundTaskType::CpanelRestore->value
                    && str_contains($p['argv'][2] ?? '', 'jabali:tasks:run-cpanel-restore');
            }))
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        $job = new RunCpanelRestore(
            jobId: 'job-123',
            logPath: $tmpLog,
            backupPath: '/tmp/backup.tar.gz',
            username: 'testuser',
            restoreFiles: true,
            restoreDatabases: true,
            restoreEmails: false,
            restoreSsl: false,
            discoveredData: null,
        );

        $job->handle(
            Mockery::mock(\App\Services\Agent\AgentClient::class),
            Mockery::mock(MigrationDnsSyncService::class),
            Mockery::mock(MigrationEmailProvisionService::class),
        );

        // Cache entry was set with the constructor args
        $cached = Cache::get('bg-task:cpanel-restore:job-123');
        $this->assertIsArray($cached);
        $this->assertEquals('testuser', $cached['username']);
        $this->assertTrue($cached['restore_files']);

        @unlink($tmpLog);
        Cache::forget('bg-task:cpanel-restore:job-123');
    }
}
