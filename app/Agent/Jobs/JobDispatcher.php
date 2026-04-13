<?php

declare(strict_types=1);

namespace App\Agent\Jobs;

use DateTimeImmutable;
use InvalidArgumentException;
use RuntimeException;

/**
 * Top-level façade the agent's socket handler calls. Orchestrates the three
 * subsystems:
 *
 *  - JobStore (what jobs exist and what their last-known status is)
 *  - SystemdRunnerInterface (spawn / cancel / inspect transient units)
 *  - JobLog (read the JSONL events the spawned binary emits)
 *
 * All methods return plain arrays — ready to be json_encoded into the agent
 * socket response.
 */
final class JobDispatcher
{
    public function __construct(
        private readonly JobStore $store,
        private readonly SystemdRunnerInterface $runner,
        private readonly string $logDir = '/var/lib/jabali/jobs',
    ) {
        if (! is_dir($this->logDir)) {
            @mkdir($this->logDir, 0700, recursive: true);
        }
    }

    /**
     * @param  list<string>  $argv  Binary + args. First element must be absolute.
     * @param  array{cpu?: int, memory?: string, io?: int}  $limits
     * @param  array<string, mixed>  $payload
     * @return array{id: string, unit: string, pid: int, log_path: string, started_at: string}
     */
    public function start(string $type, array $argv, array $limits, array $payload, ?string $id = null): array
    {
        if (count($argv) === 0) {
            throw new InvalidArgumentException('argv must not be empty');
        }
        if (! is_string($argv[0]) || ! str_starts_with($argv[0], '/')) {
            throw new InvalidArgumentException('argv[0] must be an absolute path');
        }

        // Use the caller-supplied id when present so the panel's
        // background_tasks.id and the agent's record share one identity.
        // Required for the reconciler to match rows across stores.
        $id ??= $this->uuid();
        $unit = 'jabali-job-'.$id;
        $logPath = rtrim($this->logDir, '/')."/{$id}.log";

        // Pre-create the log file with group ownership on the panel's uid
        // so the panel can tail it (TaskEventRelay runs as www-data). The
        // file is root-owned but group-readable to the panel group.
        // Not using Laravel's config() — the agent is a standalone script
        // that autoloads classes but doesn't boot the framework container.
        @touch($logPath);
        @chmod($logPath, 0640);
        @chgrp($logPath, getenv('JABALI_PANEL_GROUP') ?: 'www-data');

        // Inject the contract args every spawned binary must accept.
        $fullArgv = array_merge($argv, [
            '--task-id='.$id,
            '--progress-log='.$logPath,
        ]);

        $spawn = $this->runner->startTransient($type, $unit, $fullArgv, $limits);

        $nowIso = gmdate('Y-m-d\TH:i:s\Z');
        $this->store->insert([
            'id' => $id,
            'type' => $type,
            'status' => 'running',
            'unit' => $spawn['unit'],
            'pid' => $spawn['pid'],
            'log_path' => $logPath,
            'payload' => $payload,
        ]);
        $this->store->updateStatus($id, 'running', [
            'pid' => $spawn['pid'],
            'started_at' => $nowIso,
        ]);

        return [
            'id' => $id,
            'unit' => $spawn['unit'],
            'pid' => $spawn['pid'],
            'log_path' => $logPath,
            'started_at' => $nowIso,
        ];
    }

    /**
     * @return array<string, mixed> Row + recent_events key.
     */
    public function status(string $id): array
    {
        $row = $this->store->get($id);
        if ($row === null) {
            throw new RuntimeException("Job not found: {$id}");
        }

        $log = new JobLog($row['log_path']);
        $events = array_map(fn ($e) => $e->toArray(), $log->tail(10));

        $row['recent_events'] = $events;

        return $row;
    }

    public function cancel(string $id, int $graceSec = 10): void
    {
        $row = $this->store->get($id);
        if ($row === null) {
            throw new RuntimeException("Job not found: {$id}");
        }

        if (in_array($row['status'], ['done', 'failed', 'canceled'], true)) {
            return; // already terminal
        }

        $this->runner->stopUnit($row['unit'], $graceSec);
        $this->store->updateStatus($id, 'canceled', [
            'finished_at' => gmdate('Y-m-d\TH:i:s\Z'),
        ]);
    }

    /**
     * @param  array{status?: string, type?: string, limit?: int}  $filters
     * @return list<array<string, mixed>>
     */
    public function list(array $filters): array
    {
        return $this->store->list($filters);
    }

    /**
     * @return list<array<string, mixed>>
     */
    public function tail(string $id, ?string $since = null): array
    {
        $row = $this->store->get($id);
        if ($row === null) {
            throw new RuntimeException("Job not found: {$id}");
        }

        $log = new JobLog($row['log_path']);
        $events = $since !== null
            ? $log->since(new DateTimeImmutable($since))
            : $log->tail(100);

        return array_map(fn ($e) => $e->toArray(), $events);
    }

    private function uuid(): string
    {
        $data = random_bytes(16);
        $data[6] = chr((ord($data[6]) & 0x0F) | 0x40);
        $data[8] = chr((ord($data[8]) & 0x3F) | 0x80);

        return vsprintf('%s%s-%s-%s-%s-%s%s%s', str_split(bin2hex($data), 4));
    }
}
