<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskStatus;
use App\Enums\BackgroundTaskType;
use App\Models\BackgroundTask;
use App\Models\User;
use Illuminate\Support\Carbon;
use Tests\TestCase;

class BackgroundTaskModelTest extends TestCase
{
    public function test_factory_creates_task_with_defaults(): void
    {
        $task = BackgroundTask::factory()->create();

        $this->assertNotEmpty($task->id);
        $this->assertEquals(36, strlen($task->id), 'id should be UUID');
        $this->assertInstanceOf(BackgroundTaskStatus::class, $task->status);
        $this->assertEquals(BackgroundTaskStatus::Pending, $task->status);
        $this->assertInstanceOf(BackgroundTaskType::class, $task->type);
        $this->assertNotEmpty($task->callback_token, 'callback_token auto-generated');
        $this->assertEquals(64, strlen($task->callback_token), 'callback_token is 64 hex chars');
        $this->assertNotEmpty($task->log_path, 'log_path derived from id');
        $this->assertStringContainsString($task->id, $task->log_path);

        $task->delete();
    }

    public function test_enum_cast_round_trip(): void
    {
        $task = BackgroundTask::factory()->create([
            'type' => BackgroundTaskType::Backup,
            'status' => BackgroundTaskStatus::Running,
        ]);

        $fresh = BackgroundTask::find($task->id);
        $this->assertEquals(BackgroundTaskType::Backup, $fresh->type);
        $this->assertEquals(BackgroundTaskStatus::Running, $fresh->status);

        $task->delete();
    }

    public function test_payload_cast_as_array(): void
    {
        $task = BackgroundTask::factory()->create([
            'payload' => ['domain' => 'example.com', 'flags' => ['wildcard', 'staging']],
        ]);

        $fresh = BackgroundTask::find($task->id);
        $this->assertEquals('example.com', $fresh->payload['domain']);
        $this->assertEquals(['wildcard', 'staging'], $fresh->payload['flags']);

        $task->delete();
    }

    public function test_status_transitions_valid_paths(): void
    {
        $task = BackgroundTask::factory()->pending()->create();

        $task->markRunning(pid: 4567, systemdUnit: 'jabali-job-abc');
        $this->assertEquals(BackgroundTaskStatus::Running, $task->fresh()->status);
        $this->assertNotNull($task->fresh()->started_at);
        $this->assertEquals(4567, $task->fresh()->pid);

        $task->markProgress(percent: 42, step: 'uploading');
        $this->assertEquals(42, $task->fresh()->progress);
        $this->assertEquals('uploading', $task->fresh()->step);

        $task->markDone(exitCode: 0);
        $fresh = $task->fresh();
        $this->assertEquals(BackgroundTaskStatus::Done, $fresh->status);
        $this->assertNotNull($fresh->finished_at);
        $this->assertEquals(0, $fresh->exit_code);

        $task->delete();
    }

    public function test_status_transitions_failure_path(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $task->markFailed(exitCode: 1, errorMessage: 'rsync: connection lost');

        $fresh = $task->fresh();
        $this->assertEquals(BackgroundTaskStatus::Failed, $fresh->status);
        $this->assertEquals(1, $fresh->exit_code);
        $this->assertEquals('rsync: connection lost', $fresh->error_message);
        $this->assertNotNull($fresh->finished_at);

        $task->delete();
    }

    public function test_cancel_transition(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $task->markCanceled('User requested cancel');

        $fresh = $task->fresh();
        $this->assertEquals(BackgroundTaskStatus::Canceled, $fresh->status);
        $this->assertEquals('User requested cancel', $fresh->error_message);
        $this->assertNotNull($fresh->finished_at);

        $task->delete();
    }

    public function test_terminal_status_cannot_transition(): void
    {
        $task = BackgroundTask::factory()->done()->create();

        $this->expectException(\DomainException::class);
        $task->markRunning(pid: 1, systemdUnit: 'x');

        $task->delete();
    }

    public function test_error_message_truncated_at_4kb(): void
    {
        $long = str_repeat('A', 5000);
        $task = BackgroundTask::factory()->running()->create();

        $task->markFailed(exitCode: 1, errorMessage: $long);

        $this->assertLessThanOrEqual(4096, strlen($task->fresh()->error_message));

        $task->delete();
    }

    public function test_scope_active(): void
    {
        $pending = BackgroundTask::factory()->pending()->create();
        $running = BackgroundTask::factory()->running()->create();
        $done = BackgroundTask::factory()->done()->create();
        $failed = BackgroundTask::factory()->failed()->create();

        $active = BackgroundTask::active()->pluck('id')->all();

        $this->assertContains($pending->id, $active);
        $this->assertContains($running->id, $active);
        $this->assertNotContains($done->id, $active);
        $this->assertNotContains($failed->id, $active);

        BackgroundTask::whereIn('id', [$pending->id, $running->id, $done->id, $failed->id])->delete();
    }

    public function test_scope_of_type(): void
    {
        $backup = BackgroundTask::factory()->create(['type' => BackgroundTaskType::Backup]);
        $ssl = BackgroundTask::factory()->create(['type' => BackgroundTaskType::SslIssue]);

        $ids = BackgroundTask::ofType(BackgroundTaskType::Backup)->pluck('id')->all();
        $this->assertContains($backup->id, $ids);
        $this->assertNotContains($ssl->id, $ids);

        BackgroundTask::whereIn('id', [$backup->id, $ssl->id])->delete();
    }

    public function test_dedupe_key_prevents_duplicate_active_rows(): void
    {
        $first = BackgroundTask::factory()->pending()->create([
            'dedupe_key' => 'ssl:example.com',
        ]);

        $duplicate = BackgroundTask::tryCreateUnique([
            'type' => BackgroundTaskType::SslIssue->value,
            'dedupe_key' => 'ssl:example.com',
            'payload' => [],
        ]);

        $this->assertNull($duplicate, 'should reject duplicate while first is pending');

        $first->markDone(exitCode: 0);

        $second = BackgroundTask::tryCreateUnique([
            'type' => BackgroundTaskType::SslIssue->value,
            'dedupe_key' => 'ssl:example.com',
            'payload' => [],
        ]);

        $this->assertNotNull($second, 'should allow after first completes');

        $first->delete();
        $second->delete();
    }

    public function test_polymorphic_target(): void
    {
        $user = User::factory()->create();

        $task = BackgroundTask::factory()->create([
            'target_type' => User::class,
            'target_id' => (string) $user->id,
        ]);

        $this->assertInstanceOf(User::class, $task->target);
        $this->assertEquals($user->id, $task->target->id);

        $task->delete();
        $user->delete();
    }

    public function test_duration_in_seconds(): void
    {
        $task = BackgroundTask::factory()->create([
            'status' => BackgroundTaskStatus::Done,
            'started_at' => Carbon::now()->subSeconds(125),
            'finished_at' => Carbon::now(),
        ]);

        $this->assertEqualsWithDelta(125, $task->durationSeconds(), 2);

        $task->delete();
    }

    public function test_is_terminal(): void
    {
        $this->assertFalse(BackgroundTaskStatus::Pending->isTerminal());
        $this->assertFalse(BackgroundTaskStatus::Running->isTerminal());
        $this->assertTrue(BackgroundTaskStatus::Done->isTerminal());
        $this->assertTrue(BackgroundTaskStatus::Failed->isTerminal());
        $this->assertTrue(BackgroundTaskStatus::Canceled->isTerminal());
    }
}
