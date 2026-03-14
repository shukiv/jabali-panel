<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\Domain;
use App\Models\SslCertificate;
use App\Services\Agent\AgentClient;
use Carbon\Carbon;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Log;

class IssueSslCertificate implements ShouldQueue
{
    use Queueable;

    public int $tries = 3;
    public int $backoff = 60; // Wait 60 seconds between retries

    public function __construct(
        public int $domainId
    ) {}

    public function handle(): void
    {
        $domain = Domain::with('user')->find($this->domainId);

        if (!$domain) {
            Log::warning("IssueSslCertificate: Domain {$this->domainId} not found");
            return;
        }

        // Skip if domain already has active web SSL
        $existingSsl = SslCertificate::where('domain_id', $domain->id)
            ->where('service', 'web')
            ->where('status', 'active')
            ->first();

        if ($existingSsl) {
            Log::info("IssueSslCertificate: Domain {$domain->domain} already has active SSL");
            return;
        }

        if (!$domain->user) {
            Log::warning("IssueSslCertificate: Domain {$domain->domain} has no user");
            return;
        }

        Log::info("IssueSslCertificate: Issuing SSL for {$domain->domain}");

        try {
            $agent = new AgentClient();
            $result = $agent->sslIssue(
                $domain->domain,
                $domain->user->username,
                $domain->user->email,
                true // Force issue
            );

            if ($result['success'] ?? false) {
                SslCertificate::updateOrCreate(
                    ['domain_id' => $domain->id, 'service' => 'web'],
                    [
                        'type' => 'lets_encrypt',
                        'status' => 'active',
                        'issuer' => "Let's Encrypt",
                        'certificate' => $result['certificate'] ?? null,
                        'issued_at' => now(),
                        'expires_at' => isset($result['valid_to']) ? Carbon::parse($result['valid_to']) : now()->addMonths(3),
                        'last_check_at' => now(),
                        'last_error' => null,
                        'renewal_attempts' => 0,
                        'auto_renew' => true,
                    ]
                );

                $domain->update(['ssl_enabled' => true]);

                Log::info("IssueSslCertificate: SSL issued successfully for {$domain->domain}");
            } else {
                $error = $result['error'] ?? 'Unknown error';

                // Record the failure but don't throw - let the scheduled SSL check retry later
                SslCertificate::updateOrCreate(
                    ['domain_id' => $domain->id, 'service' => 'web'],
                    [
                        'type' => 'lets_encrypt',
                        'status' => 'pending',
                        'last_check_at' => now(),
                        'last_error' => $error,
                        'auto_renew' => true,
                    ]
                );

                Log::warning("IssueSslCertificate: Failed to issue SSL for {$domain->domain}: {$error}");
            }
        } catch (\Exception $e) {
            Log::error("IssueSslCertificate: Exception for {$domain->domain}: " . $e->getMessage());

            // Record pending state so scheduled check can retry
            SslCertificate::updateOrCreate(
                ['domain_id' => $domain->id, 'service' => 'web'],
                [
                    'type' => 'lets_encrypt',
                    'status' => 'pending',
                    'last_check_at' => now(),
                    'last_error' => $e->getMessage(),
                    'auto_renew' => true,
                ]
            );

            // Re-throw to trigger job retry
            throw $e;
        }
    }
}
