<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskStatus;
use App\Enums\BackgroundTaskType;
use App\Models\BackgroundTask;
use App\Services\Agent\AgentClientInterface;
use App\Services\BackgroundTasks\BackgroundTaskDispatcher;
use Mockery;
use Tests\TestCase;

class BackgroundTaskDispatcherTest extends TestCase
{
    protected function tearDown(): void
    {
        Mockery::close();
        parent::tearDown();
    }

    public function test_dispatch_creates_row_and_calls_agent(): void
    {
        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($params) {
                return $params['type'] === 'ssl_issue'
                    && $params['argv'] === ['/usr/bin/certbot', 'certonly', '-d', 'example.com']
                    && isset($params['limits']['cpu']);
            }))
            ->andReturn([
                'success' => true,
                'id' => 'agent-generated-id',
                'unit' => 'jabali-job-agent-generated-id',
                'pid' => 4242,
                'log_path' => '/var/lib/jabali/jobs/agent-generated-id.log',
                'started_at' => '2026-04-13T01:00:00Z',
            ]);

        $dispatcher = new BackgroundTaskDispatcher($agent);

        $task = $dispatcher->dispatch(
            type: BackgroundTaskType::SslIssue,
            argv: ['/usr/bin/certbot', 'certonly', '-d', 'example.com'],
            payload: ['domain' => 'example.com'],
            limits: ['cpu' => 25, 'memory' => '512M', 'io' => 50],
        );

        $this->assertInstanceOf(BackgroundTask::class, $task);
        $this->assertEquals(BackgroundTaskStatus::Running, $task->fresh()->status);
        $this->assertEquals('jabali-job-agent-generated-id', $task->fresh()->systemd_unit);
        $this->assertEquals(4242, $task->fresh()->pid);
        $this->assertNotNull($task->fresh()->started_at);

        $task->delete();
    }

    public function test_dispatch_respects_dedupe_key(): void
    {
        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')->once()->andReturn([
            'success' => true,
            'unit' => 'u',
            'pid' => 1,
            'started_at' => '2026-04-13T01:00:00Z',
        ]);

        $dispatcher = new BackgroundTaskDispatcher($agent);

        $first = $dispatcher->dispatch(
            type: BackgroundTaskType::SslIssue,
            argv: ['/usr/bin/certbot'],
            dedupeKey: 'ssl:example.com',
        );
        $this->assertNotNull($first);

        // Second call with same dedupe_key while first is active — rejected
        $second = $dispatcher->dispatch(
            type: BackgroundTaskType::SslIssue,
            argv: ['/usr/bin/certbot'],
            dedupeKey: 'ssl:example.com',
        );
        $this->assertNull($second);

        $first->delete();
    }

    public function test_dispatch_marks_failed_when_agent_returns_error(): void
    {
        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')->once()->andReturn([
            'success' => false,
            'error' => 'systemd-run: unit already exists',
        ]);

        $dispatcher = new BackgroundTaskDispatcher($agent);

        $task = $dispatcher->dispatch(
            type: BackgroundTaskType::Backup,
            argv: ['/opt/jabali-backup/bin/run'],
        );

        $this->assertNotNull($task);
        $fresh = $task->fresh();
        $this->assertEquals(BackgroundTaskStatus::Failed, $fresh->status);
        $this->assertStringContainsString('systemd-run', $fresh->error_message);

        $task->delete();
    }

    public function test_dispatch_marks_failed_when_agent_throws(): void
    {
        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')->once()->andThrow(new \RuntimeException('socket closed'));

        $dispatcher = new BackgroundTaskDispatcher($agent);

        $task = $dispatcher->dispatch(
            type: BackgroundTaskType::Backup,
            argv: ['/opt/jabali-backup/bin/run'],
        );

        $this->assertNotNull($task);
        $this->assertEquals(BackgroundTaskStatus::Failed, $task->fresh()->status);
        $this->assertStringContainsString('socket closed', $task->fresh()->error_message);

        $task->delete();
    }

    public function test_dispatch_passes_callback_token_in_payload(): void
    {
        $agent = Mockery::mock(AgentClientInterface::class);
        $capturedPayload = null;

        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($params) use (&$capturedPayload) {
                $capturedPayload = $params['payload'];

                return true;
            }))
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);

        $dispatcher = new BackgroundTaskDispatcher($agent);
        $task = $dispatcher->dispatch(
            type: BackgroundTaskType::Backup,
            argv: ['/opt/jabali-backup/bin/run'],
            payload: ['user' => 'alice'],
        );

        $this->assertNotNull($capturedPayload);
        $this->assertArrayHasKey('callback_token', $capturedPayload);
        $this->assertArrayHasKey('task_id', $capturedPayload);
        $this->assertEquals('alice', $capturedPayload['user']);
        $this->assertEquals($task->id, $capturedPayload['task_id']);

        $task->delete();
    }
}
