<?php

declare(strict_types=1);

namespace App\Agent\Jobs;

/**
 * Abstraction over systemd-run / systemctl so the dispatcher can be tested
 * without requiring root + a real systemd on CI. The real implementation is
 * SystemdRunner; tests inject a fake.
 */
interface SystemdRunnerInterface
{
    /**
     * Start a transient systemd unit running the given command with the given
     * cgroup limits.
     *
     * @param  list<string>  $argv  Absolute path first, arguments follow.
     * @param  array{cpu?: int, memory?: string, io?: int}  $limits
     * @return array{unit: string, pid: int}
     */
    public function startTransient(string $type, string $unit, array $argv, array $limits): array;

    /**
     * Send SIGTERM then SIGKILL after grace to the transient unit.
     */
    public function stopUnit(string $unit, int $graceSec = 10): void;

    /**
     * True if the transient unit is still running.
     */
    public function isActive(string $unit): bool;
}
