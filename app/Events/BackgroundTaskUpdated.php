<?php

declare(strict_types=1);

namespace App\Events;

use Illuminate\Broadcasting\Channel;
use Illuminate\Broadcasting\InteractsWithSockets;
use Illuminate\Broadcasting\PrivateChannel;
use Illuminate\Contracts\Broadcasting\ShouldBroadcast;
use Illuminate\Foundation\Events\Dispatchable;
use Illuminate\Queue\SerializesModels;

/**
 * Broadcast when a background task advances (progress, step, or status change).
 *
 * Phase 11 (ADR-0007). When Reverb is configured, Livewire listeners in the
 * Filament BackgroundTaskResource subscribe to `background-tasks.{task_id}`
 * and update their state without polling.
 *
 * Polling (Phase 5) remains the fallback for sites that don't run Reverb.
 */
class BackgroundTaskUpdated implements ShouldBroadcast
{
    use Dispatchable;
    use InteractsWithSockets;
    use SerializesModels;

    public function __construct(
        public readonly string $taskId,
        public readonly string $kind,
        public readonly string $status,
        public readonly ?int $progress = null,
        public readonly ?string $step = null,
        public readonly ?string $error = null,
    ) {}

    /**
     * @return list<Channel>
     */
    public function broadcastOn(): array
    {
        return [new PrivateChannel('background-tasks.'.$this->taskId)];
    }

    public function broadcastAs(): string
    {
        return 'updated';
    }

    /**
     * @return array<string, mixed>
     */
    public function broadcastWith(): array
    {
        return [
            'task_id' => $this->taskId,
            'kind' => $this->kind,
            'status' => $this->status,
            'progress' => $this->progress,
            'step' => $this->step,
            'error' => $this->error,
        ];
    }
}
