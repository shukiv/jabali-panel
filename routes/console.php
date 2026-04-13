<?php

use App\Models\AuditLog;
use Illuminate\Foundation\Inspiring;
use Illuminate\Support\Facades\Artisan;
use Illuminate\Support\Facades\Schedule;

Artisan::command('inspire', function () {
    $this->comment(Inspiring::quote());
})->purpose('Display an inspiring quote');

// Panel Certificate Sync - runs twice daily to sync FrankenPHP/Caddy ACME cert info
Schedule::command('jabali:panel-cert-sync')
    ->twiceDaily(4, 16)
    ->withoutOverlapping()
    ->runInBackground()
    ->appendOutputTo(storage_path('logs/panel-cert-sync.log'));

// SSL Certificate Auto-Check - runs every 3 hours to issue/renew certificates
Schedule::command('jabali:ssl-check')
    ->everyThreeHours()
    ->withoutOverlapping()
    ->runInBackground()
    ->appendOutputTo(storage_path('logs/ssl-check.log'));

// SSL Certificate Renewal Check - runs daily to check expiring certificates
Schedule::command('jabali:ssl-check --renew-only')
    ->daily()
    ->at('03:00')
    ->withoutOverlapping()
    ->runInBackground()
    ->appendOutputTo(storage_path('logs/ssl-renewal.log'));

// Disk Quota Check - runs twice daily to check for users approaching quota limits
Schedule::command('jabali:check-quotas')
    ->twiceDaily(6, 18)
    ->withoutOverlapping()
    ->runInBackground()
    ->appendOutputTo(storage_path('logs/quota-check.log'));

// File Integrity Check - runs every 6 hours to detect unauthorized modifications
Schedule::command('jabali:check-integrity --notify')
    ->everySixHours()
    ->withoutOverlapping()
    ->runInBackground()
    ->appendOutputTo(storage_path('logs/integrity-check.log'));

// User Cron Jobs - runs every minute to execute due user cron jobs
Schedule::command('jabali:run-cron-jobs')
    ->everyMinute()
    ->withoutOverlapping()
    ->runInBackground()
    ->appendOutputTo(storage_path('logs/user-cron-jobs.log'));

// Bandwidth Sync - runs daily to track domain bandwidth from nginx logs
Schedule::command('jabali:sync-bandwidth')
    ->daily()
    ->at('00:15')
    ->withoutOverlapping()
    ->runInBackground()
    ->appendOutputTo(storage_path('logs/bandwidth-sync.log'));

// Mailbox Quota Sync - runs every 15 minutes to update mailbox usage from disk
Schedule::command('jabali:sync-mailbox-quotas')
    ->everyFifteenMinutes()
    ->withoutOverlapping()
    ->runInBackground()
    ->appendOutputTo(storage_path('logs/mailbox-quota-sync.log'));

Schedule::command('jabali:sync-mailbox-logins')
    ->everyFifteenMinutes()
    ->withoutOverlapping()
    ->runInBackground();

// Audit Log Rotation - runs daily to prune old audit logs (default: 90 days retention)
Schedule::call(function () {
    $deleted = AuditLog::prune();
    if ($deleted > 0) {
        logger()->info("Pruned {$deleted} audit log entries");
    }
})->daily()->at('02:00');

// Orphaned Records Cleanup - runs daily to remove orphaned records
Schedule::call(function () {
    $cleaned = 0;

    // Clean orphaned SSL certificates (domain deleted but certificate record remains)
    $cleaned += \App\Models\SslCertificate::whereNotIn('domain_id', \App\Models\Domain::pluck('id'))->delete();

    // Clean orphaned DNS records
    $cleaned += \App\Models\DnsRecord::whereNotIn('domain_id', \App\Models\Domain::pluck('id'))->delete();

    // Clean orphaned email domains
    $cleaned += \App\Models\EmailDomain::whereNotIn('domain_id', \App\Models\Domain::pluck('id'))->delete();

    // Clean orphaned mailboxes (email domain deleted)
    $cleaned += \App\Models\Mailbox::whereNotIn('email_domain_id', \App\Models\EmailDomain::pluck('id'))->delete();

    // Clean orphaned forwarders
    $cleaned += \App\Models\EmailForwarder::whereNotIn('email_domain_id', \App\Models\EmailDomain::pluck('id'))->delete();

    // Clean orphaned autoresponders (mailbox deleted)
    $cleaned += \App\Models\Autoresponder::whereNotIn('mailbox_id', \App\Models\Mailbox::pluck('id'))->delete();

    if ($cleaned > 0) {
        logger()->info("Cleaned up {$cleaned} orphaned database records");
    }
})->daily()->at('02:30');

// SSO Token Cleanup - runs every 5 minutes to remove expired SSO token files
Schedule::call(function () {
    $dir = '/var/lib/jabali/sso-tokens';
    if (! is_dir($dir)) {
        return;
    }
    foreach (glob($dir.'/*_sso_*') as $file) {
        $data = @json_decode(@file_get_contents($file), true);
        if (! is_array($data) || ($data['expires'] ?? 0) < time()) {
            @unlink($file);
        }
    }
})->everyFiveMinutes()->name('sso-token-cleanup')->withoutOverlapping();

// WordPress Screenshot Refresh - runs weekly to update site previews
Schedule::call(function () {
    $agent = app(\App\Services\Agent\AgentClient::class);
    $users = \App\Models\User::whereNotNull('username')->get();
    $captured = 0;

    foreach ($users as $user) {
        try {
            $result = $agent->send('wp.list', ['username' => $user->username]);
            $sites = $result['sites'] ?? [];

            foreach ($sites as $site) {
                $url = $site['url'] ?? '';
                $siteId = $site['id'] ?? '';
                if ($url && $siteId) {
                    $agent->send('screenshot.capture', ['url' => $url, 'site_id' => $siteId]);
                    $captured++;
                }
            }
        } catch (\Throwable) {
            // Skip user on error
        }
    }

    if ($captured > 0) {
        logger()->info("Refreshed {$captured} WordPress screenshots");
    }
})->weekly()->sundays()->at('03:00')->name('wp-screenshot-refresh')->withoutOverlapping();

// Domain DNS Health Check - runs every 6 hours to check DNS resolution status
Schedule::command('jabali:check-domain-dns')
    ->everySixHours()
    ->withoutOverlapping()
    ->runInBackground()
    ->appendOutputTo(storage_path('logs/domain-dns-check.log'));

// Background task reconciliation (ADR-0007) - runs every minute.
// Agent-originated event POSTs are the fast path; this is the safety net
// that catches dropped events, orphaned rows, and agent restarts.
Schedule::command('jabali:tasks:reconcile')
    ->everyMinute()
    ->withoutOverlapping()
    ->name('background-tasks-reconcile');

// Tail the JSONL progress logs and apply new events. Runs every 10s for
// near-real-time dashboard updates (until Reverb broadcasting is wired).
Schedule::command('jabali:tasks:relay')
    ->everyTenSeconds()
    ->withoutOverlapping()
    ->name('background-tasks-relay');

// Impersonation Token Cleanup - runs daily to remove expired/used tokens
Schedule::call(function () {
    \App\Models\ImpersonationToken::where(function ($q) {
        $q->where('expires_at', '<', now())
            ->orWhere(function ($q2) {
                $q2->whereNotNull('used_at')
                    ->where('created_at', '<', now()->subDay());
            });
    })->delete();
})->daily()->at('03:00')->name('impersonation-token-cleanup');
