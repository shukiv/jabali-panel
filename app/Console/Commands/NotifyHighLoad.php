<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Services\AdminNotificationService;
use Illuminate\Console\Command;

class NotifyHighLoad extends Command
{
    protected $signature = 'notify:high-load
                            {event : Event type (high|recovered)}
                            {load : Current load average}
                            {minutes : Minutes the load has been high}';

    protected $description = 'Send notification for high server load events';

    public function handle(): int
    {
        $event = $this->argument('event');
        $load = (float) $this->argument('load');
        $minutes = (int) $this->argument('minutes');

        $result = match ($event) {
            'high' => $this->notifyHighLoad($load, $minutes),
            'recovered' => $this->notifyRecovered($load),
            default => false,
        };

        return $result ? Command::SUCCESS : Command::FAILURE;
    }

    protected function notifyHighLoad(float $load, int $minutes): bool
    {
        $hostname = gethostname() ?: 'server';
        $cpuCount = (int) shell_exec('nproc 2>/dev/null') ?: 1;

        return AdminNotificationService::send(
            'high_load',
            "High Server Load: {$hostname}",
            "The server load has been critically high for {$minutes} minutes. Current load: {$load} (CPU cores: {$cpuCount}). Please investigate immediately.",
            [
                'Load Average' => number_format($load, 2),
                'Duration' => "{$minutes} minutes",
                'CPU Cores' => $cpuCount,
                'Load per Core' => number_format($load / $cpuCount, 2),
            ]
        );
    }

    protected function notifyRecovered(float $load): bool
    {
        $hostname = gethostname() ?: 'server';

        return AdminNotificationService::send(
            'high_load',
            "Server Load Recovered: {$hostname}",
            "The server load has returned to normal levels. Current load: {$load}.",
            [
                'Load Average' => number_format($load, 2),
                'Status' => 'Recovered',
            ]
        );
    }
}
