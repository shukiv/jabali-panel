<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskType;
use App\Jobs\IssueSslCertificate;
use App\Models\BackgroundTask;
use App\Models\Domain;
use App\Models\User;
use App\Services\Agent\AgentClientInterface;
use App\Services\SslManagementService;
use Mockery;
use Tests\TestCase;

class SslIssuanceMigrationTest extends TestCase
{
    protected function tearDown(): void
    {
        Mockery::close();
        BackgroundTask::query()->delete();
        parent::tearDown();
    }

    public function test_legacy_path_when_flag_off(): void
    {
        config(['jabali.agent_v2.ssl_enabled' => false]);

        $user = User::factory()->create();
        $domain = Domain::factory()->for($user)->create();

        $sslService = Mockery::mock(SslManagementService::class);
        $sslService->shouldReceive('issue')
            ->once()
            ->andReturn(new \App\Models\SslCertificate(['status' => 'active', 'last_error' => null]));

        $job = new IssueSslCertificate($domain->id, 'web');
        $job->handle($sslService);

        // No background task created because flag is off
        $this->assertEquals(0, BackgroundTask::query()->count());

        $domain->delete();
        $user->delete();
    }

    public function test_new_path_when_flag_on_dispatches_background_task(): void
    {
        config(['jabali.agent_v2.ssl_enabled' => true]);

        $user = User::factory()->create();
        $domain = Domain::factory()->for($user)->create();

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($params) use ($domain) {
                return $params['type'] === BackgroundTaskType::SslIssue->value
                    && isset($params['payload']['domain_id'])
                    && $params['payload']['domain_id'] === $domain->id;
            }))
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        // SslManagementService should NOT be called under flag-on path
        $sslService = Mockery::mock(SslManagementService::class);
        $sslService->shouldReceive('issue')->never();

        $job = new IssueSslCertificate($domain->id, 'web');
        $job->handle($sslService);

        $this->assertEquals(1, BackgroundTask::query()->count());
        $task = BackgroundTask::query()->first();
        $this->assertEquals(BackgroundTaskType::SslIssue, $task->type);
        $this->assertEquals($domain->id, (int) $task->target_id);
        $this->assertEquals(Domain::class, $task->target_type);
        $this->assertEquals('ssl:'.$domain->id.':web', $task->dedupe_key);

        $domain->delete();
        $user->delete();
    }

    public function test_dedupe_prevents_duplicate_dispatch(): void
    {
        config(['jabali.agent_v2.ssl_enabled' => true]);

        $user = User::factory()->create();
        $domain = Domain::factory()->for($user)->create();

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once() // only first dispatch should reach agent
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        $sslService = Mockery::mock(SslManagementService::class);

        (new IssueSslCertificate($domain->id, 'web'))->handle($sslService);
        (new IssueSslCertificate($domain->id, 'web'))->handle($sslService);

        $this->assertEquals(1, BackgroundTask::query()->count());

        $domain->delete();
        $user->delete();
    }

    public function test_run_ssl_issue_command_emits_jsonl_and_exits_on_success(): void
    {
        $task = BackgroundTask::factory()->running()->create(['type' => BackgroundTaskType::SslIssue]);
        $logPath = sys_get_temp_dir().'/ssl-issue-'.uniqid().'.log';

        $user = User::factory()->create();
        $domain = Domain::factory()->for($user)->create();

        $sslService = Mockery::mock(SslManagementService::class);
        $sslService->shouldReceive('issue')
            ->once()
            ->andReturn(new \App\Models\SslCertificate(['status' => 'active', 'last_error' => null]));
        $this->app->instance(SslManagementService::class, $sslService);

        $exit = $this->artisan('jabali:tasks:run-ssl-issue', [
            '--task-id' => $task->id,
            '--progress-log' => $logPath,
            '--domain-id' => $domain->id,
            '--service' => 'web',
        ])->run();

        $this->assertEquals(0, $exit);
        $this->assertFileExists($logPath);
        $lines = array_filter(file($logPath, FILE_IGNORE_NEW_LINES));
        $this->assertGreaterThanOrEqual(2, count($lines)); // started + done

        $first = json_decode($lines[0], true);
        $last = json_decode(end($lines), true);
        $this->assertEquals('started', $first['event']);
        $this->assertEquals('done', $last['event']);

        unlink($logPath);
        $task->delete();
        $domain->delete();
        $user->delete();
    }

    public function test_run_ssl_issue_command_emits_failed_on_service_error(): void
    {
        $task = BackgroundTask::factory()->running()->create(['type' => BackgroundTaskType::SslIssue]);
        $logPath = sys_get_temp_dir().'/ssl-issue-'.uniqid().'.log';

        $user = User::factory()->create();
        $domain = Domain::factory()->for($user)->create();

        $sslService = Mockery::mock(SslManagementService::class);
        $sslService->shouldReceive('issue')
            ->once()
            ->andReturn(new \App\Models\SslCertificate(['status' => 'failed', 'last_error' => 'rate limit exceeded']));
        $this->app->instance(SslManagementService::class, $sslService);

        $exit = $this->artisan('jabali:tasks:run-ssl-issue', [
            '--task-id' => $task->id,
            '--progress-log' => $logPath,
            '--domain-id' => $domain->id,
            '--service' => 'web',
        ])->run();

        $this->assertNotEquals(0, $exit);
        $lines = array_filter(file($logPath, FILE_IGNORE_NEW_LINES));
        $last = json_decode(end($lines), true);
        $this->assertEquals('failed', $last['event']);
        $this->assertStringContainsString('rate limit', $last['error']);

        unlink($logPath);
        $task->delete();
        $domain->delete();
        $user->delete();
    }
}
