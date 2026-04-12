<?php

declare(strict_types=1);

namespace App\Agent\Jobs;

use App\Services\BackgroundTasks\Events\TaskEvent;
use DateTimeImmutable;

/**
 * Reads JSONL progress events emitted by a spawned task binary.
 *
 * The agent is single-threaded and event-driven; this class is invoked
 * on-demand when a job.status / job.tail request arrives. Malformed lines
 * are silently skipped (a stray line from the binary must not break the
 * whole status endpoint).
 */
final class JobLog
{
    public function __construct(private readonly string $path) {}

    /**
     * @return list<TaskEvent>
     */
    public function tail(int $count): array
    {
        $events = $this->readAll();

        return array_slice($events, -$count);
    }

    /**
     * @return list<TaskEvent>
     */
    public function since(DateTimeImmutable $ts): array
    {
        $events = $this->readAll();

        return array_values(array_filter(
            $events,
            fn (TaskEvent $e) => $e->ts > $ts,
        ));
    }

    /**
     * Extract the latest known progress + step from the log.
     *
     * @return array{progress: ?int, step: ?string}
     */
    public function latestStatus(): array
    {
        $events = $this->readAll();
        $progress = null;
        $step = null;

        foreach ($events as $e) {
            if ($e->percent !== null) {
                $progress = $e->percent;
            }
            if ($e->step !== null) {
                $step = $e->step;
            }
        }

        return ['progress' => $progress, 'step' => $step];
    }

    /**
     * @return list<TaskEvent>
     */
    private function readAll(): array
    {
        if (! is_readable($this->path)) {
            return [];
        }

        $events = [];
        $fp = @fopen($this->path, 'rb');
        if ($fp === false) {
            return [];
        }

        try {
            while (($line = fgets($fp)) !== false) {
                $line = trim($line);
                if ($line === '') {
                    continue;
                }
                try {
                    $events[] = TaskEvent::fromJsonLine($line);
                } catch (\InvalidArgumentException) {
                    // Skip malformed line. Binaries must be tolerant.
                }
            }
        } finally {
            fclose($fp);
        }

        return $events;
    }
}
