<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Events\BackgroundTaskUpdated;
use App\Models\BackgroundTask;
use Illuminate\Support\Facades\Event;
use Tests\TestCase;

class BackgroundTaskBroadcastTest extends TestCase
{
    protected function tearDown(): void
    {
        BackgroundTask::query()->delete();
        parent::tearDown();
    }

    public function test_event_endpoint_broadcasts_update(): void
    {
        Event::fake([BackgroundTaskUpdated::class]);

        $task = BackgroundTask::factory()->running()->create();

        $this->postJson("/api/internal/tasks/{$task->id}/event", [
            'event' => 'progress',
            'ts' => '2026-04-13T01:00:00Z',
            'percent' => 42,
            'step' => 'uploading',
            'callback_token' => $task->callback_token,
        ])->assertStatus(200);

        Event::assertDispatched(BackgroundTaskUpdated::class, function ($event) use ($task) {
            return $event->taskId === $task->id
                && $event->kind === 'progress';
        });
    }

    public function test_event_broadcasts_on_private_channel(): void
    {
        $task = BackgroundTask::factory()->running()->create();
        $event = new BackgroundTaskUpdated(
            taskId: $task->id,
            kind: 'progress',
            status: 'running',
            progress: 42,
            step: 'uploading',
        );

        $channels = $event->broadcastOn();
        $this->assertCount(1, $channels);
        $this->assertStringContainsString($task->id, (string) $channels[0]);
    }
}
