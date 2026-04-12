<?php

declare(strict_types=1);

namespace App\Agent;

/**
 * Immutable result of a RunSafe invocation.
 *
 * Carries stdout, stderr, the process exit code, and the wall-clock duration
 * in milliseconds. Use this to drive retries, audit logging, and error
 * propagation.
 */
final class RunResult
{
    public function __construct(
        public readonly int $exitCode,
        public readonly string $stdout,
        public readonly string $stderr,
        public readonly int $durationMs,
    ) {}

    public function succeeded(): bool
    {
        return $this->exitCode === 0;
    }

    /**
     * @return array{exit_code: int, stdout: string, stderr: string, duration_ms: int}
     */
    public function toArray(): array
    {
        return [
            'exit_code' => $this->exitCode,
            'stdout' => $this->stdout,
            'stderr' => $this->stderr,
            'duration_ms' => $this->durationMs,
        ];
    }
}
