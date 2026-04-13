<?php

declare(strict_types=1);

namespace App\Services\BackgroundTasks;

use App\Enums\BackgroundTaskStatus;
use App\Models\BackgroundTask;
use App\Services\BackgroundTasks\Events\TaskEvent;
use App\Services\BackgroundTasks\Events\TaskEventKind;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Log;

/**
 * Read JSONL progress logs written by spawned task binaries and apply the
 * events to the matching background_tasks rows.
 *
 * Panel-side only. Since panel and agent live on the same host, we can
 * read /var/lib/jabali/jobs/*.log directly — no HTTP roundtrip, no agent
 * relay, no broadcast. Same semantics as the POST /event endpoint; that
 * endpoint remains for future multi-host setups.
 *
 * Tracks the last-applied event timestamp on each task so a single scan
 * never replays already-applied events.
 */
final class TaskEventRelay
{
    public function __construct(
        private readonly string $logDir = '/var/lib/jabali/jobs',
    ) {}

    /**
     * Scan every running task's log file and apply any new events.
     *
     * @return array{scanned: int, applied: int}
     */
    public function relayAll(): array
    {
        $running = BackgroundTask::query()->active()->get();
        $applied = 0;

        foreach ($running as $task) {
            $applied += $this->relay($task);
        }

        return ['scanned' => $running->count(), 'applied' => $applied];
    }

    public function relay(BackgroundTask $task): int
    {
        $path = $task->log_path ?: $this->logDir.'/'.$task->id.'.log';
        if (! is_readable($path)) {
            return 0;
        }

        $cursor = $task->payload['event_cursor'] ?? null;
        $cursorTs = $cursor !== null ? new \DateTimeImmutable($cursor) : null;

        $fp = @fopen($path, 'rb');
        if ($fp === false) {
            return 0;
        }

        $applied = 0;
        $maxTs = $cursor;

        try {
            while (($line = fgets($fp)) !== false) {
                $line = trim($line);
                if ($line === '') {
                    continue;
                }

                try {
                    $event = TaskEvent::fromJsonLine($line);
                } catch (\InvalidArgumentException) {
                    continue;
                }

                // Skip already-applied events by ts compare.
                if ($cursorTs !== null && $event->ts <= $cursorTs) {
                    continue;
                }

                $this->apply($task, $event);
                $applied++;
                $maxTs = $event->ts->format('Y-m-d\TH:i:s\Z');

                if ($event->kind->isTerminal()) {
                    break;
                }
            }
        } finally {
            fclose($fp);
        }

        if ($applied > 0 && $maxTs !== null) {
            $payload = $task->fresh()->payload ?? [];
            $payload['event_cursor'] = $maxTs;
            $task->fresh()->update(['payload' => $payload]);
        }

        return $applied;
    }

    private function apply(BackgroundTask $task, TaskEvent $event): void
    {
        if ($task->status instanceof BackgroundTaskStatus && $task->status->isTerminal()) {
            return;
        }

        try {
            match ($event->kind) {
                TaskEventKind::Started => $task->update([
                    'status' => BackgroundTaskStatus::Running->value,
                    'started_at' => Carbon::parse($event->ts->format('c')),
                ]),
                TaskEventKind::Progress => $task->markProgress($event->percent ?? 0, $event->step),
                TaskEventKind::Step => $task->markStep($event->step ?? ''),
                TaskEventKind::Done => $task->markDone(exitCode: 0),
                TaskEventKind::Failed => $task->markFailed(exitCode: 1, errorMessage: $event->error),
                TaskEventKind::Canceled => $task->markCanceled($event->error),
            };
        } catch (\DomainException $e) {
            // task became terminal mid-apply — harmless
            Log::debug('TaskEventRelay: late apply', ['task_id' => $task->id, 'reason' => $e->getMessage()]);
        }
    }
}
