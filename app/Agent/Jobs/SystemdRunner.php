<?php

declare(strict_types=1);

namespace App\Agent\Jobs;

use App\Agent\RunSafe;
use RuntimeException;

/**
 * Real systemd-run implementation. Uses RunSafe so there's no shell involved.
 *
 * @see docs/adr/0007-agent-v2-control-plane.md
 */
final class SystemdRunner implements SystemdRunnerInterface
{
    public function __construct(
        private readonly string $systemdRunBin = '/usr/bin/systemd-run',
        private readonly string $systemctlBin = '/usr/bin/systemctl',
    ) {}

    public function startTransient(string $type, string $unit, array $argv, array $limits): array
    {
        // Transient service (not --scope — scope attaches the caller process).
        // --no-block returns as soon as systemd accepts the unit; otherwise
        // systemd-run sits in the foreground until the service exits, which
        // for a backup is hours.
        $cmd = [
            $this->systemdRunBin,
            '--unit='.$unit,
            '--collect',
            '--no-block',
            '--description=Jabali task '.$type,
        ];
        if (isset($limits['cpu']) && is_int($limits['cpu'])) {
            $cmd[] = '--property=CPUQuota='.$limits['cpu'].'%';
        }
        if (isset($limits['memory']) && is_string($limits['memory'])) {
            $cmd[] = '--property=MemoryMax='.$limits['memory'];
        }
        if (isset($limits['io']) && is_int($limits['io'])) {
            $cmd[] = '--property=IOWeight='.$limits['io'];
        }
        $cmd[] = '--';
        foreach ($argv as $arg) {
            $cmd[] = $arg;
        }

        $result = RunSafe::run($cmd, timeout: 15);
        if (! $result->succeeded()) {
            throw new RuntimeException(
                "systemd-run failed for unit {$unit}: ".trim($result->stderr)
            );
        }

        // Parse the unit's main PID. `systemctl show -p MainPID` works after
        // the unit registers. Small retry since transient units may take a
        // moment to appear.
        $pid = 0;
        for ($i = 0; $i < 10; $i++) {
            $show = RunSafe::run([$this->systemctlBin, 'show', '-p', 'MainPID', '--value', $unit], timeout: 3);
            $pid = (int) trim($show->stdout);
            if ($pid > 0) {
                break;
            }
            usleep(100_000);
        }

        return ['unit' => $unit, 'pid' => $pid];
    }

    public function stopUnit(string $unit, int $graceSec = 10): void
    {
        // `systemctl stop` already does SIGTERM then SIGKILL based on
        // TimeoutStopSec. For transient units we'd have had to set that at
        // creation time; falling back to kill -s TERM then -s KILL is
        // simpler and maps directly to the grace semantics we want.
        RunSafe::run([$this->systemctlBin, 'kill', '--signal=SIGTERM', $unit], timeout: 5);

        $deadline = microtime(true) + $graceSec;
        while (microtime(true) < $deadline) {
            if (! $this->isActive($unit)) {
                return;
            }
            usleep(200_000);
        }

        RunSafe::run([$this->systemctlBin, 'kill', '--signal=SIGKILL', $unit], timeout: 5);
    }

    public function isActive(string $unit): bool
    {
        $result = RunSafe::run([$this->systemctlBin, 'is-active', '--quiet', $unit], timeout: 3);

        return $result->exitCode === 0;
    }
}
