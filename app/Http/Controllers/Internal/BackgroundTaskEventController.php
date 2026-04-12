<?php

declare(strict_types=1);

namespace App\Http\Controllers\Internal;

use App\Enums\BackgroundTaskStatus;
use App\Events\BackgroundTaskUpdated;
use App\Models\BackgroundTask;
use App\Services\BackgroundTasks\Events\TaskEvent;
use App\Services\BackgroundTasks\Events\TaskEventKind;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Log;

/**
 * Receives JSONL events posted by the agent as a spawned task binary writes
 * to its progress log.
 *
 * Auth is per-task: the body must include the `callback_token` that was
 * generated when the task row was created. Even an attacker with access to
 * the localhost rate-limit bucket cannot update a task without knowing its
 * specific token.
 *
 * @see docs/adr/0007-agent-v2-control-plane.md
 */
class BackgroundTaskEventController
{
    public function __invoke(Request $request, string $id): JsonResponse
    {
        $task = BackgroundTask::find($id);
        if ($task === null) {
            return response()->json(['error' => 'task not found'], 404);
        }

        $providedToken = (string) $request->input('callback_token', '');
        if ($providedToken === '' || ! hash_equals((string) $task->callback_token, $providedToken)) {
            return response()->json(['error' => 'invalid callback_token'], 401);
        }

        try {
            $event = TaskEvent::fromJsonLine((string) json_encode($request->except('callback_token')));
        } catch (\InvalidArgumentException $e) {
            return response()->json(['error' => 'invalid event: '.$e->getMessage()], 422);
        }

        if ($task->status instanceof BackgroundTaskStatus && $task->status->isTerminal()) {
            // Late events for completed tasks are harmless; silently accept.
            return response()->json(['ok' => true, 'applied' => false]);
        }

        try {
            $this->apply($task, $event);
        } catch (\DomainException $e) {
            // Task transitioned to terminal between our check and the update.
            Log::info('BackgroundTaskEventController: late event on terminal task', [
                'task_id' => $task->id,
                'event' => $event->kind->value,
            ]);

            return response()->json(['ok' => true, 'applied' => false]);
        }

        // Broadcast so Livewire subscribers can update without polling.
        BackgroundTaskUpdated::dispatch(
            $task->id,
            $event->kind->value,
            (string) $task->fresh()->status->value,
            $task->fresh()->progress,
            $task->fresh()->step,
            $task->fresh()->error_message,
        );

        return response()->json(['ok' => true, 'applied' => true]);
    }

    private function apply(BackgroundTask $task, TaskEvent $event): void
    {
        match ($event->kind) {
            TaskEventKind::Started => $this->applyStarted($task, $event),
            TaskEventKind::Progress => $task->markProgress($event->percent ?? 0, $event->step),
            TaskEventKind::Step => $task->markStep($event->step ?? ''),
            TaskEventKind::Done => $task->markDone(exitCode: 0),
            TaskEventKind::Failed => $task->markFailed(exitCode: 1, errorMessage: $event->error),
            TaskEventKind::Canceled => $task->markCanceled($event->error),
        };
    }

    private function applyStarted(BackgroundTask $task, TaskEvent $event): void
    {
        // Started is emitted by the spawned binary itself; the agent already
        // updated systemd_unit/pid at dispatch time. Just make sure the row
        // is in `running` and started_at is set to the binary's own timestamp.
        $task->update([
            'status' => BackgroundTaskStatus::Running->value,
            'started_at' => Carbon::parse($event->ts->format('c')),
        ]);
    }
}
