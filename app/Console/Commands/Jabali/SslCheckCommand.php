<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Models\Domain;
use App\Models\SslCertificate;
use App\Services\AdminNotificationService;
use App\Services\Agent\AgentClient;
use Carbon\Carbon;
use Exception;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\File;

class SslCheckCommand extends Command
{
    protected $signature = 'jabali:ssl-check
                            {--domain= : Check a specific domain only}
                            {--issue-only : Only issue certificates for domains without SSL}
                            {--renew-only : Only renew expiring certificates}';

    protected $description = 'Check SSL certificates and automatically issue/renew them';

    private AgentClient $agent;
    private int $issued = 0;
    private int $renewed = 0;
    private int $failed = 0;
    private int $skipped = 0;
    private array $logEntries = [];
    private string $logFile = '';

    public function __construct()
    {
        parent::__construct();
        $this->agent = new AgentClient();
    }

    public function handle(): int
    {
        $this->initializeLogging();
        $this->log('Starting SSL certificate check...');
        $this->info('Starting SSL certificate check...');
        $this->newLine();

        $domain = $this->option('domain');
        $issueOnly = $this->option('issue-only');
        $renewOnly = $this->option('renew-only');

        if ($domain) {
            $this->processSingleDomain($domain);
        } else {
            if (!$renewOnly) {
                $this->issueMissingCertificates();
            }

            if (!$issueOnly) {
                $this->renewExpiringCertificates();
            }

            // Check for certificates expiring very soon (7 days) and notify
            $this->notifyExpiringSoon();
        }

        $this->newLine();
        $this->info('SSL Check Complete');
        $this->table(
            ['Metric', 'Count'],
            [
                ['Issued', $this->issued],
                ['Renewed', $this->renewed],
                ['Failed', $this->failed],
                ['Skipped', $this->skipped],
            ]
        );

        // Log summary
        $this->log('');
        $this->log('=== SSL Check Complete ===');
        $this->log("Issued: {$this->issued}");
        $this->log("Renewed: {$this->renewed}");
        $this->log("Failed: {$this->failed}");
        $this->log("Skipped: {$this->skipped}");

        // Save log file
        $this->saveLog();

        // Clean old logs (older than 3 months)
        $this->cleanOldLogs();

        return $this->failed > 0 ? 1 : 0;
    }

    private function initializeLogging(): void
    {
        $logDir = storage_path('logs/ssl');

        try {
            if (!is_dir($logDir)) {
                mkdir($logDir, 0775, true);
            }

            // Ensure directory is writable
            if (!is_writable($logDir)) {
                chmod($logDir, 0775);
            }

            $this->logFile = $logDir . '/ssl-check-' . date('Y-m-d_H-i-s') . '.log';
        } catch (\Exception $e) {
            // Fall back to temp directory if storage is not writable
            $this->logFile = sys_get_temp_dir() . '/ssl-check-' . date('Y-m-d_H-i-s') . '.log';
        }

        $this->logEntries = [];
    }

    private function log(string $message, string $level = 'INFO'): void
    {
        $timestamp = date('Y-m-d H:i:s');
        $this->logEntries[] = "[{$timestamp}] [{$level}] {$message}";
    }

    private function saveLog(): void
    {
        if (empty($this->logFile) || empty($this->logEntries)) {
            return;
        }

        try {
            $content = implode("\n", $this->logEntries) . "\n";

            // Ensure parent directory exists
            $logDir = dirname($this->logFile);
            if (!is_dir($logDir)) {
                @mkdir($logDir, 0775, true);
            }

            if (@file_put_contents($this->logFile, $content) !== false) {
                // Also create/update a symlink to latest log
                $latestLink = storage_path('logs/ssl/latest.log');
                @unlink($latestLink);
                @symlink($this->logFile, $latestLink);

                $this->line("Log saved to: {$this->logFile}");
            } else {
                $this->warn("Could not save log to: {$this->logFile}");
            }
        } catch (\Exception $e) {
            $this->warn("Log save failed: {$e->getMessage()}");
        }
    }

    private function cleanOldLogs(): void
    {
        $logDir = storage_path('logs/ssl');
        $cutoffDate = now()->subMonths(3);
        $deletedCount = 0;

        foreach (glob("{$logDir}/ssl-check-*.log") as $file) {
            $fileTime = filemtime($file);
            if ($fileTime < $cutoffDate->timestamp) {
                unlink($file);
                $deletedCount++;
            }
        }

        if ($deletedCount > 0) {
            $this->log("Cleaned up {$deletedCount} old log files (older than 3 months)");
        }
    }

    private function processSingleDomain(string $domainName): void
    {
        $domain = Domain::where('domain', $domainName)->with(['user', 'sslCertificate'])->first();

        if (!$domain) {
            $this->log("Domain not found: {$domainName}", 'ERROR');
            $this->error("Domain not found: {$domainName}");
            $this->failed++;
            return;
        }

        $this->log("Processing domain: {$domainName}");
        $this->line("Processing domain: {$domainName}");

        $ssl = $domain->sslCertificate;

        if (!$ssl || $ssl->status === 'failed') {
            $this->issueCertificate($domain);
        } elseif ($ssl->needsRenewal()) {
            $this->renewCertificate($domain);
        } else {
            $this->log("Certificate is valid for {$domainName}, expires: {$ssl->expires_at->format('Y-m-d')}");
            $this->line("  - Certificate is valid, expires: {$ssl->expires_at->format('Y-m-d')}");
            $this->skipped++;
        }
    }

    private function issueMissingCertificates(): void
    {
        $this->log('Checking domains without SSL certificates...');
        $this->info('Checking domains without SSL certificates...');

        $domains = Domain::whereDoesntHave('sslCertificate')
            ->orWhereHas('sslCertificate', function ($q) {
                $q->where('status', 'failed')
                    ->where('renewal_attempts', '<', 3)
                    ->where(function ($q2) {
                        $q2->whereNull('last_check_at')
                            ->orWhere('last_check_at', '<', now()->subHours(6));
                    });
            })
            ->with(['user', 'sslCertificate'])
            ->get();

        $this->log("Found {$domains->count()} domains without valid SSL");
        $this->line("Found {$domains->count()} domains without valid SSL");

        foreach ($domains as $domain) {
            $this->issueCertificate($domain);
        }
    }

    private function renewExpiringCertificates(): void
    {
        $this->log('Checking certificates that need renewal...');
        $this->info('Checking certificates that need renewal...');

        $certificates = SslCertificate::where('auto_renew', true)
            ->where('type', 'lets_encrypt')
            ->where('status', 'active')
            ->where('expires_at', '<=', now()->addDays(30))
            ->where('renewal_attempts', '<', 5)
            ->with(['domain.user'])
            ->get();

        $this->log("Found {$certificates->count()} certificates needing renewal");
        $this->line("Found {$certificates->count()} certificates needing renewal");

        foreach ($certificates as $ssl) {
            if ($ssl->domain) {
                $this->renewCertificate($ssl->domain);
            }
        }
    }

    private function notifyExpiringSoon(): void
    {
        $certificates = SslCertificate::where('status', 'active')
            ->where('expires_at', '<=', now()->addDays(7))
            ->where('expires_at', '>', now())
            ->with(['domain'])
            ->get();

        foreach ($certificates as $ssl) {
            if ($ssl->domain) {
                $daysLeft = (int) now()->diffInDays($ssl->expires_at);
                $this->log("Certificate expiring soon: {$ssl->domain->domain} ({$daysLeft} days left)", 'WARN');
                AdminNotificationService::sslExpiring($ssl->domain->domain, $daysLeft);
            }
        }
    }

    private function issueCertificate(Domain $domain): void
    {
        if (!$domain->user) {
            $this->log("Skipping {$domain->domain}: No user associated", 'WARN');
            $this->warn("  Skipping {$domain->domain}: No user associated");
            $this->skipped++;
            return;
        }

        // Check if domain DNS points to this server
        if (!$this->domainPointsToServer($domain->domain)) {
            $this->log("Skipping {$domain->domain}: DNS does not point to this server", 'WARN');
            $this->warn("  Skipping {$domain->domain}: DNS does not point to this server");
            $this->skipped++;
            return;
        }

        $this->log("Issuing SSL for: {$domain->domain} (user: {$domain->user->username})");
        $this->line("  Issuing SSL for: {$domain->domain}");

        try {
            $result = $this->agent->sslIssue(
                $domain->domain,
                $domain->user->username,
                $domain->user->email,
                true
            );

            if ($result['success'] ?? false) {
                $expiresAt = isset($result['valid_to']) ? Carbon::parse($result['valid_to']) : now()->addMonths(3);

                SslCertificate::updateOrCreate(
                    ['domain_id' => $domain->id],
                    [
                        'type' => 'lets_encrypt',
                        'status' => 'active',
                        'issuer' => "Let's Encrypt",
                        'certificate' => $result['certificate'] ?? null,
                        'issued_at' => now(),
                        'expires_at' => $expiresAt,
                        'last_check_at' => now(),
                        'last_error' => null,
                        'renewal_attempts' => 0,
                        'auto_renew' => true,
                    ]
                );

                $domain->update(['ssl_enabled' => true]);

                $this->log("SUCCESS: Certificate issued for {$domain->domain}, expires: {$expiresAt->format('Y-m-d')}", 'SUCCESS');
                $this->info("    ✓ Certificate issued successfully");
                $this->issued++;
            } else {
                $error = $result['error'] ?? 'Unknown error';

                $ssl = SslCertificate::firstOrNew(['domain_id' => $domain->id]);
                $ssl->type = 'lets_encrypt';
                $ssl->status = 'failed';
                $ssl->last_check_at = now();
                $ssl->last_error = $error;
                $ssl->increment('renewal_attempts');
                $ssl->save();

                $this->log("FAILED: Certificate issue for {$domain->domain}: {$error}", 'ERROR');
                $this->error("    ✗ Failed: {$error}");
                $this->failed++;

                // Send admin notification
                AdminNotificationService::sslError($domain->domain, $error);
            }
        } catch (Exception $e) {
            $this->log("EXCEPTION: Certificate issue for {$domain->domain}: {$e->getMessage()}", 'ERROR');
            $this->error("    ✗ Exception: {$e->getMessage()}");
            $this->failed++;

            // Send admin notification
            AdminNotificationService::sslError($domain->domain, $e->getMessage());
        }
    }

    private function renewCertificate(Domain $domain): void
    {
        if (!$domain->user) {
            $this->log("Skipping renewal for {$domain->domain}: No user associated", 'WARN');
            $this->warn("  Skipping {$domain->domain}: No user associated");
            $this->skipped++;
            return;
        }

        // Check if domain DNS still points to this server
        if (!$this->domainPointsToServer($domain->domain)) {
            $this->log("Skipping renewal for {$domain->domain}: DNS does not point to this server", 'WARN');
            $this->warn("  Skipping {$domain->domain}: DNS does not point to this server");
            $this->skipped++;
            return;
        }

        $this->log("Renewing SSL for: {$domain->domain} (user: {$domain->user->username})");
        $this->line("  Renewing SSL for: {$domain->domain}");

        try {
            $result = $this->agent->sslRenew($domain->domain, $domain->user->username);

            if ($result['success'] ?? false) {
                $ssl = $domain->sslCertificate;
                $expiresAt = isset($result['valid_to']) ? Carbon::parse($result['valid_to']) : now()->addMonths(3);

                if ($ssl) {
                    $ssl->update([
                        'status' => 'active',
                        'issued_at' => now(),
                        'expires_at' => $expiresAt,
                        'last_check_at' => now(),
                        'last_error' => null,
                        'renewal_attempts' => 0,
                    ]);
                }

                $this->log("SUCCESS: Certificate renewed for {$domain->domain}, expires: {$expiresAt->format('Y-m-d')}", 'SUCCESS');
                $this->info("    ✓ Certificate renewed successfully");
                $this->renewed++;
            } else {
                $error = $result['error'] ?? 'Unknown error';

                $ssl = $domain->sslCertificate;
                if ($ssl) {
                    $ssl->incrementRenewalAttempts();
                    $ssl->update(['last_error' => $error]);
                }

                $this->log("FAILED: Certificate renewal for {$domain->domain}: {$error}", 'ERROR');
                $this->error("    ✗ Failed: {$error}");
                $this->failed++;

                // Send admin notification
                AdminNotificationService::sslError($domain->domain, "Renewal failed: {$error}");
            }
        } catch (Exception $e) {
            $this->log("EXCEPTION: Certificate renewal for {$domain->domain}: {$e->getMessage()}", 'ERROR');
            $this->error("    ✗ Exception: {$e->getMessage()}");
            $this->failed++;

            // Send admin notification
            AdminNotificationService::sslError($domain->domain, "Renewal exception: {$e->getMessage()}");
        }
    }

    private function domainPointsToServer(string $domain): bool
    {
        // Get server's public IP
        $serverIp = $this->getServerPublicIp();
        if (!$serverIp) {
            // If we can't determine server IP, assume it's okay to try
            return true;
        }

        // Get domain's DNS resolution
        $domainIp = gethostbyname($domain);

        // gethostbyname returns the original string if resolution fails
        if ($domainIp === $domain) {
            return false;
        }

        return $domainIp === $serverIp;
    }

    private function getServerPublicIp(): ?string
    {
        static $cachedIp = null;

        if ($cachedIp !== null) {
            return $cachedIp ?: null;
        }

        // Try multiple services to get public IP
        $services = [
            'https://api.ipify.org',
            'https://ipv4.icanhazip.com',
            'https://checkip.amazonaws.com',
        ];

        foreach ($services as $service) {
            $ip = @file_get_contents($service, false, stream_context_create([
                'http' => ['timeout' => 5],
            ]));

            if ($ip) {
                $ip = trim($ip);
                if (filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4)) {
                    $cachedIp = $ip;
                    return $ip;
                }
            }
        }

        $cachedIp = '';
        return null;
    }
}
