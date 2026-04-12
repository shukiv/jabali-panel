<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskType;
use App\Jobs\RunBackup;
use App\Models\BackgroundTask;
use App\Services\Agent\AgentClientInterface;
use Mockery;
use Tests\TestCase;

class RunBackupJobTest extends TestCase
{
    protected function tearDown(): void
    {
        Mockery::close();
        BackgroundTask::query()->delete();
        parent::tearDown();
    }

    public function test_dispatches_background_task_via_agent(): void
    {
        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($params) {
                return $params['type'] === BackgroundTaskType::Backup->value
                    && isset($params['limits']['memory'])
                    && $params['limits']['memory'] === '1G'
                    && $params['argv'][0] === '/opt/jabali-backup/bin/jabali-backup';
            }))
            ->andReturn(['success' => true, 'unit' => 'jabali-job-x', 'pid' => 4242, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        $job = new RunBackup(reason: 'scheduled');
        $job->handle(app(\App\Services\BackgroundTasks\BackgroundTaskDispatcher::class));

        $task = BackgroundTask::first();
        $this->assertNotNull($task);
        $this->assertEquals(BackgroundTaskType::Backup, $task->type);
        $this->assertEquals('backup:scheduled', $task->dedupe_key);
    }

    public function test_forwards_backup_args_as_flags(): void
    {
        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($params) {
                return in_array('--mode=full', $params['argv'], true)
                    && in_array('--target=primary', $params['argv'], true);
            }))
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        $job = new RunBackup(reason: 'manual', backupArgs: ['mode' => 'full', 'target' => 'primary']);
        $job->handle(app(\App\Services\BackgroundTasks\BackgroundTaskDispatcher::class));

        $this->assertEquals(1, BackgroundTask::query()->count());
    }

    public function test_dedupe_prevents_concurrent_backups(): void
    {
        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        $job1 = new RunBackup(reason: 'scheduled');
        $job2 = new RunBackup(reason: 'scheduled');

        $dispatcher = app(\App\Services\BackgroundTasks\BackgroundTaskDispatcher::class);
        $job1->handle($dispatcher);
        $job2->handle($dispatcher);

        $this->assertEquals(1, BackgroundTask::query()->count());
    }
}
