<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Enums\BackgroundTaskType;
use App\Models\Domain;
use App\Models\SslCertificate;
use App\Services\AdminNotificationService;
use App\Services\BackgroundTasks\BackgroundTaskDispatcher;
use App\Services\SslManagementService;
use Illuminate\Contracts\Queue\ShouldBeUnique;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Log;
use Throwable;

class IssueSslCertificate implements ShouldBeUnique, ShouldQueue
{
    use Queueable;

    public int $tries = 3;

    public int $backoff = 60; // Wait 60 seconds between retries

    public int $uniqueFor = 300;

    public function __construct(
        public int $domainId,
        public string $service = 'web',
    ) {}

    public function uniqueId(): string
    {
        return "ssl-{$this->domainId}-{$this->service}";
    }

    public function handle(SslManagementService $service): void
    {
        $domain = Domain::with('user')->find($this->domainId);

        if (! $domain) {
            Log::warning("IssueSslCertificate: Domain {$this->domainId} not found");

            return;
        }

        // Skip if domain already has a real (non-snakeoil) active SSL for this service
        $existingSsl = SslCertificate::where('domain_id', $domain->id)
            ->where('service', $this->service)
            ->where('status', 'active')
            ->whereNotIn('type', ['none', 'self_signed', ''])
            ->whereNotNull('type')
            ->first();

        if ($existingSsl) {
            Log::info("IssueSslCertificate: Domain {$domain->domain} already has active {$this->service} SSL ({$existingSsl->type})");

            return;
        }

        if (! $domain->user) {
            Log::warning("IssueSslCertificate: Domain {$domain->domain} has no user");

            return;
        }

        // Agent v2 path (ADR-0007): dispatch a BackgroundTask that runs
        // certbot in a transient systemd unit with cgroup limits, and emits
        // progress to the unified Background Tasks dashboard.
        if (config('jabali.agent_v2.ssl_enabled')) {
            $this->dispatchViaAgentV2($domain);

            return;
        }

        // Legacy path: call the SSL service synchronously in this worker.
        Log::info("IssueSslCertificate: Issuing {$this->service} SSL for {$domain->domain}");

        $cert = $service->issue($domain, $this->service);

        if ($cert->status === 'failed') {
            Log::warning("IssueSslCertificate: Failed to issue {$this->service} SSL for {$domain->domain}: {$cert->last_error}");

            return;
        }

        if ($this->service === 'web') {
            $domain->update(['ssl_enabled' => true]);
        }

        Log::info("IssueSslCertificate: {$this->service} SSL issued successfully for {$domain->domain}");
    }

    public function failed(Throwable $exception): void
    {
        $domain = Domain::find($this->domainId);
        $domainName = $domain->domain ?? "Domain #{$this->domainId}";

        AdminNotificationService::sslError($domainName, $exception->getMessage());

        Log::error('IssueSslCertificate job failed', [
            'domain_id' => $this->domainId,
            'service' => $this->service,
            'error' => $exception->getMessage(),
        ]);
    }

    private function dispatchViaAgentV2(Domain $domain): void
    {
        $dispatcher = app(BackgroundTaskDispatcher::class);

        $argv = [
            '/usr/bin/php',
            base_path('artisan'),
            'jabali:tasks:run-ssl-issue',
            '--domain-id='.$domain->id,
            '--service='.$this->service,
        ];

        $task = $dispatcher->dispatch(
            type: BackgroundTaskType::SslIssue,
            argv: $argv,
            payload: [
                'domain_id' => $domain->id,
                'domain' => $domain->domain,
                'service' => $this->service,
            ],
            dedupeKey: 'ssl:'.$domain->id.':'.$this->service,
            targetType: Domain::class,
            targetId: (string) $domain->id,
            limits: ['cpu' => 50, 'memory' => '256M', 'io' => 100],
        );

        if ($task === null) {
            Log::info("IssueSslCertificate: dedupe — SSL task already running for {$domain->domain}/{$this->service}");

            return;
        }

        Log::info("IssueSslCertificate: dispatched agent-v2 task {$task->id} for {$domain->domain}/{$this->service}");
    }
}
