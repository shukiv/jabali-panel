<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskStatus;
use App\Models\BackgroundTask;
use Tests\TestCase;

class TaskEventEndpointTest extends TestCase
{
    public function test_returns_404_for_unknown_task(): void
    {
        $response = $this->postJson('/api/internal/tasks/missing-uuid/event', [
            'event' => 'progress',
            'ts' => '2026-04-13T01:00:00Z',
            'callback_token' => 'any',
        ]);

        $response->assertStatus(404);
    }

    public function test_rejects_wrong_callback_token(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'progress',
            'ts' => '2026-04-13T01:00:00Z',
            'percent' => 50,
            'callback_token' => 'wrong-token',
        ]);

        $response->assertStatus(401);
        $task->delete();
    }

    public function test_progress_event_updates_task(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'progress',
            'ts' => '2026-04-13T01:00:00Z',
            'percent' => 42,
            'step' => 'uploading',
            'callback_token' => $task->callback_token,
        ]);

        $response->assertStatus(200);
        $fresh = $task->fresh();
        $this->assertEquals(42, $fresh->progress);
        $this->assertEquals('uploading', $fresh->step);

        $task->delete();
    }

    public function test_done_event_marks_task_done(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'done',
            'ts' => '2026-04-13T01:00:00Z',
            'callback_token' => $task->callback_token,
        ]);

        $response->assertStatus(200);
        $fresh = $task->fresh();
        $this->assertEquals(BackgroundTaskStatus::Done, $fresh->status);
        $this->assertNotNull($fresh->finished_at);

        $task->delete();
    }

    public function test_failed_event_marks_task_failed_with_error(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'failed',
            'ts' => '2026-04-13T01:00:00Z',
            'error' => 'rsync: connection lost',
            'step' => 'uploading',
            'callback_token' => $task->callback_token,
        ]);

        $response->assertStatus(200);
        $fresh = $task->fresh();
        $this->assertEquals(BackgroundTaskStatus::Failed, $fresh->status);
        $this->assertEquals('rsync: connection lost', $fresh->error_message);

        $task->delete();
    }

    public function test_started_event_captures_started_at(): void
    {
        $task = BackgroundTask::factory()->pending()->create([
            'callback_token' => bin2hex(random_bytes(32)),
        ]);

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'started',
            'ts' => '2026-04-13T01:00:00Z',
            'callback_token' => $task->callback_token,
        ]);

        $response->assertStatus(200);
        $fresh = $task->fresh();
        $this->assertEquals(BackgroundTaskStatus::Running, $fresh->status);
        $this->assertNotNull($fresh->started_at);

        $task->delete();
    }

    public function test_step_event_updates_step_only(): void
    {
        $task = BackgroundTask::factory()->running()->create(['progress' => 20]);

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'step',
            'ts' => '2026-04-13T01:00:00Z',
            'step' => 'verifying',
            'callback_token' => $task->callback_token,
        ]);

        $response->assertStatus(200);
        $fresh = $task->fresh();
        $this->assertEquals('verifying', $fresh->step);
        $this->assertEquals(20, $fresh->progress, 'progress unchanged');

        $task->delete();
    }

    public function test_canceled_event_marks_task_canceled(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'canceled',
            'ts' => '2026-04-13T01:00:00Z',
            'callback_token' => $task->callback_token,
        ]);

        $response->assertStatus(200);
        $this->assertEquals(BackgroundTaskStatus::Canceled, $task->fresh()->status);

        $task->delete();
    }

    public function test_ignores_events_for_terminal_task(): void
    {
        $task = BackgroundTask::factory()->done()->create();
        $originalProgress = $task->progress;

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'progress',
            'ts' => '2026-04-13T01:00:00Z',
            'percent' => 50,
            'callback_token' => $task->callback_token,
        ]);

        $response->assertStatus(200); // silently accepted, no-op
        $this->assertEquals($originalProgress, $task->fresh()->progress);

        $task->delete();
    }

    public function test_rejects_malformed_event(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'exploded', // unknown kind
            'ts' => '2026-04-13T01:00:00Z',
            'callback_token' => $task->callback_token,
        ]);

        $response->assertStatus(422);
        $task->delete();
    }

    public function test_rejects_percent_out_of_range(): void
    {
        $task = BackgroundTask::factory()->running()->create();

        $response = $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'progress',
            'ts' => '2026-04-13T01:00:00Z',
            'percent' => 150,
            'callback_token' => $task->callback_token,
        ]);

        $response->assertStatus(422);
        $task->delete();
    }
}
