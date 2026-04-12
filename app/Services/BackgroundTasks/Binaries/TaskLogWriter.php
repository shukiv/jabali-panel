<?php

declare(strict_types=1);

namespace App\Services\BackgroundTasks\Binaries;

/**
 * Helper for spawned task binaries that live inside the panel codebase.
 *
 * Writes JSONL events matching the contract in ADR-0007. External
 * binaries (like jabali-backup) implement this in their own language;
 * in-tree binaries can just use this class.
 */
final class TaskLogWriter
{
    public function __construct(private readonly string $path) {}

    public function started(): void
    {
        $this->write(['event' => 'started']);
    }

    public function progress(int $percent, ?string $step = null): void
    {
        $entry = ['event' => 'progress', 'percent' => max(0, min(100, $percent))];
        if ($step !== null) {
            $entry['step'] = $step;
        }
        $this->write($entry);
    }

    public function step(string $step): void
    {
        $this->write(['event' => 'step', 'step' => $step]);
    }

    public function done(array $data = []): void
    {
        $this->write(['event' => 'done', 'data' => $data]);
    }

    public function failed(string $error, ?string $step = null): void
    {
        $entry = ['event' => 'failed', 'error' => substr($error, 0, 4096)];
        if ($step !== null) {
            $entry['step'] = $step;
        }
        $this->write($entry);
    }

    public function canceled(?string $reason = null): void
    {
        $entry = ['event' => 'canceled'];
        if ($reason !== null) {
            $entry['error'] = $reason;
        }
        $this->write($entry);
    }

    private function write(array $entry): void
    {
        $entry = ['ts' => gmdate('Y-m-d\TH:i:s\Z')] + $entry;
        $line = json_encode($entry, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE)."\n";

        $dir = dirname($this->path);
        if (! is_dir($dir)) {
            @mkdir($dir, 0700, recursive: true);
        }

        file_put_contents($this->path, $line, FILE_APPEND | LOCK_EX);
    }
}
