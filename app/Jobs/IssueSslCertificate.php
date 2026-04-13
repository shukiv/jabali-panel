<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\Domain;
use App\Models\SslCertificate;
use App\Services\AdminNotificationService;
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
}
