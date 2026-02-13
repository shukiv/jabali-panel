<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Services\AdminNotificationService;
use Illuminate\Console\Command;

class NotifyServiceHealth extends Command
{
    protected $signature = 'notify:service-health
                            {event : Event type (down|restarted|recovered|failed)}
                            {service : Service name}
                            {--description= : Service description}';

    protected $description = 'Send notification for service health events';

    public function handle(): int
    {
        $event = $this->argument('event');
        $service = $this->argument('service');
        $description = $this->option('description') ?? $service;

        $result = match ($event) {
            'down' => $this->notifyDown($service, $description),
            'restarted' => $this->notifyRestarted($service, $description),
            'recovered' => $this->notifyRecovered($service, $description),
            'failed' => $this->notifyFailed($service, $description),
            default => false,
        };

        return $result ? Command::SUCCESS : Command::FAILURE;
    }

    protected function notifyDown(string $service, string $description): bool
    {
        return AdminNotificationService::send(
            'service_health',
            "Service Down: {$description}",
            "The {$description} service ({$service}) has stopped and will be automatically restarted.",
            ['Service' => $service, 'Status' => 'Down', 'Action' => 'Auto-restart pending']
        );
    }

    protected function notifyRestarted(string $service, string $description): bool
    {
        return AdminNotificationService::send(
            'service_health',
            "Service Auto-Restarted: {$description}",
            "The {$description} service ({$service}) was down and has been automatically restarted by the health monitor.",
            ['Service' => $service, 'Status' => 'Restarted', 'Action' => 'Automatic recovery']
        );
    }

    protected function notifyRecovered(string $service, string $description): bool
    {
        return AdminNotificationService::send(
            'service_health',
            "Service Recovered: {$description}",
            "The {$description} service ({$service}) has recovered and is now running normally.",
            ['Service' => $service, 'Status' => 'Running', 'Action' => 'None required']
        );
    }

    protected function notifyFailed(string $service, string $description): bool
    {
        return AdminNotificationService::send(
            'service_health',
            "CRITICAL: Service Failed: {$description}",
            "The {$description} service ({$service}) could not be automatically restarted after multiple attempts. Manual intervention is required immediately.",
            ['Service' => $service, 'Status' => 'Failed', 'Action' => 'Manual intervention required']
        );
    }
}
