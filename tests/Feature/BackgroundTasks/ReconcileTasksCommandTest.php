<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskStatus;
use App\Models\BackgroundTask;
use App\Services\Agent\AgentClientInterface;
use Carbon\Carbon;
use Mockery;
use Tests\TestCase;

class ReconcileTasksCommandTest extends TestCase
{
    protected function tearDown(): void
    {
        Mockery::close();
        BackgroundTask::query()->delete();
        parent::tearDown();
    }

    public function test_no_drift_no_changes(): void
    {
        $task = BackgroundTask::factory()->running()->create([
            'systemd_unit' => 'jabali-job-xyz',
        ]);

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->with('job.list', Mockery::type('array'))
            ->andReturn([
                'success' => true,
                'tasks' => [[
                    'id' => $task->id,
                    'status' => 'running',
                    'unit' => 'jabali-job-xyz',
                ]],
            ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:tasks:reconcile')->assertSuccessful();

        $this->assertEquals(BackgroundTaskStatus::Running, $task->fresh()->status);
    }

    public function test_orphan_is_marked_failed(): void
    {
        // Panel thinks running, agent doesn't know about it anymore
        $task = BackgroundTask::factory()->running()->create([
            'systemd_unit' => 'jabali-job-orphan',
            'started_at' => Carbon::now()->subMinutes(10),
        ]);

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->with('job.list', Mockery::type('array'))
            ->andReturn(['success' => true, 'tasks' => []]); // agent knows nothing

        // Also calls job.status to double-check before orphaning
        $agent->shouldReceive('send')
            ->with('job.status', ['id' => $task->id])
            ->andReturn(['success' => false, 'error' => 'Job not found']);

        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:tasks:reconcile')->assertSuccessful();

        $fresh = $task->fresh();
        $this->assertEquals(BackgroundTaskStatus::Failed, $fresh->status);
        $this->assertStringContainsString('orphan', strtolower($fresh->error_message));
    }

    public function test_syncs_terminal_status_from_agent(): void
    {
        // Panel thinks running, agent says done
        $task = BackgroundTask::factory()->running()->create([
            'systemd_unit' => 'jabali-job-complete',
        ]);

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->with('job.list', Mockery::type('array'))
            ->andReturn([
                'success' => true,
                'tasks' => [[
                    'id' => $task->id,
                    'status' => 'done',
                    'exit_code' => 0,
                ]],
            ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:tasks:reconcile')->assertSuccessful();

        $this->assertEquals(BackgroundTaskStatus::Done, $task->fresh()->status);
    }

    public function test_syncs_failed_status_from_agent(): void
    {
        $task = BackgroundTask::factory()->running()->create([
            'systemd_unit' => 'jabali-job-bad',
        ]);

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->with('job.list', Mockery::type('array'))
            ->andReturn([
                'success' => true,
                'tasks' => [[
                    'id' => $task->id,
                    'status' => 'failed',
                    'exit_code' => 1,
                    'error' => 'segfault',
                ]],
            ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:tasks:reconcile')->assertSuccessful();

        $fresh = $task->fresh();
        $this->assertEquals(BackgroundTaskStatus::Failed, $fresh->status);
        $this->assertEquals('segfault', $fresh->error_message);
    }

    public function test_ignores_terminal_panel_rows(): void
    {
        // Terminal rows should never be touched — prevents flapping from
        // agent SQLite drift on restart
        $task = BackgroundTask::factory()->done()->create();

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')->with('job.list', Mockery::type('array'))
            ->andReturn(['success' => true, 'tasks' => []]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:tasks:reconcile')->assertSuccessful();

        $this->assertEquals(BackgroundTaskStatus::Done, $task->fresh()->status);
    }

    public function test_handles_agent_unreachable(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')->andThrow(new \RuntimeException('socket closed'));
        $this->app->instance(AgentClientInterface::class, $agent);

        // Should not throw — reconciler logs and returns. Task stays running.
        $this->artisan('jabali:tasks:reconcile')->assertSuccessful();

        $this->assertEquals(BackgroundTaskStatus::Running, $task->fresh()->status);
    }

    public function test_does_not_orphan_recently_started_tasks(): void
    {
        // Just started tasks might not have registered with the agent yet —
        // don't prematurely mark them as orphans.
        $task = BackgroundTask::factory()->running()->create([
            'started_at' => Carbon::now()->subSeconds(10), // 10s ago, very fresh
        ]);

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')->with('job.list', Mockery::type('array'))
            ->andReturn(['success' => true, 'tasks' => []]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:tasks:reconcile')->assertSuccessful();

        $this->assertEquals(BackgroundTaskStatus::Running, $task->fresh()->status);
    }
}
