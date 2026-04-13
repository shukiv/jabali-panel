<?php

declare(strict_types=1);

namespace App\Services\Migration;

use App\Jobs\IssueSslCertificate;
use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\Domain;
use App\Models\EmailDomain;
use App\Models\EmailForwarder;
use App\Models\Mailbox;
use App\Models\ServerImportAccount;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\System\MailRoutingSyncService;
use App\Support\ServerFacts;
use Exception;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;

class MigrationEmailProvisionService
{
    public function __construct(
        private AgentClient $agent,
        private MailRoutingSyncService $mailRoutingSyncService,
    ) {}

    /**
     * Provision email domains, mailboxes, and forwarders from cPanel/WHM discovered data.
     *
     * @param  array<string, mixed>  $discoveredData  Contains 'mailboxes' and 'forwarders' arrays
     * @param  callable(string, string): void|null  $logger  Optional callback for progress logging
     */
    public function provisionFromDiscoveredData(
        User $user,
        array $discoveredData,
        ?callable $logger = null,
    ): MigrationEmailProvisionResult {
        $result = new MigrationEmailProvisionResult;
        $logger ??= fn (string $msg, string $status) => Log::info("MigrationEmailProvision: {$msg}", ['status' => $status]);

        $mailboxes = $discoveredData['mailboxes'] ?? [];
        $forwarders = $discoveredData['forwarders'] ?? [];

        if (empty($mailboxes) && empty($forwarders)) {
            return $result;
        }

        // Group mailboxes by domain
        $mailboxesByDomain = $this->groupByDomain($mailboxes);
        $forwardersByDomain = $this->groupByDomain($forwarders);

        // Collect all unique domain names
        $allDomains = array_unique(array_merge(
            array_keys($mailboxesByDomain),
            array_keys($forwardersByDomain),
        ));

        // Enable each domain for email
        $emailDomains = [];
        foreach ($allDomains as $domainName) {
            try {
                $domain = Domain::where('domain', $domainName)
                    ->where('user_id', $user->id)
                    ->first();

                if (! $domain) {
                    $logger(__('Domain not found: :domain', ['domain' => $domainName]), 'warning');

                    continue;
                }

                $emailDomain = $this->enableEmailDomain($user, $domain);
                $emailDomains[$domainName] = $emailDomain;
                $logger(__('Email enabled for domain: :domain', ['domain' => $domainName]), 'success');
            } catch (Exception $e) {
                $result->errors[] = __('Failed to enable email for :domain: :error', [
                    'domain' => $domainName,
                    'error' => $e->getMessage(),
                ]);
                $logger($result->errors[array_key_last($result->errors)], 'error');
            }
        }

        // Create mailboxes
        foreach ($mailboxesByDomain as $domainName => $domainMailboxes) {
            $emailDomain = $emailDomains[$domainName] ?? null;
            if (! $emailDomain) {
                continue;
            }

            foreach ($domainMailboxes as $mailboxData) {
                $localPart = $mailboxData['local_part'] ?? '';
                if (empty($localPart)) {
                    continue;
                }

                try {
                    $mailboxResult = $this->createMailbox($user, $emailDomain, $localPart, $domainName);
                    if ($mailboxResult) {
                        $result->mailboxes[] = $mailboxResult;
                        $logger(__('Mailbox created: :email', ['email' => $mailboxResult['email']]), 'success');
                    }
                } catch (Exception $e) {
                    $result->errors[] = __('Failed to create mailbox :email: :error', [
                        'email' => "{$localPart}@{$domainName}",
                        'error' => $e->getMessage(),
                    ]);
                    $logger($result->errors[array_key_last($result->errors)], 'error');
                }
            }
        }

        // Create forwarders
        foreach ($forwardersByDomain as $domainName => $domainForwarders) {
            $emailDomain = $emailDomains[$domainName] ?? null;
            if (! $emailDomain) {
                continue;
            }

            foreach ($domainForwarders as $forwarderData) {
                $localPart = $forwarderData['local_part'] ?? '';
                $destinations = $forwarderData['destinations'] ?? '';
                if (empty($localPart) || empty($destinations)) {
                    continue;
                }

                $destinationsArray = $this->parseDestinations($destinations);
                if (empty($destinationsArray)) {
                    continue;
                }

                try {
                    $created = $this->createForwarder($user, $emailDomain, $localPart, $domainName, $destinationsArray);
                    if ($created) {
                        $result->forwarders[] = "{$localPart}@{$domainName}";
                        $logger(__('Forwarder created: :email', ['email' => "{$localPart}@{$domainName}"]), 'success');
                    }
                } catch (Exception $e) {
                    $result->errors[] = __('Failed to create forwarder :email: :error', [
                        'email' => "{$localPart}@{$domainName}",
                        'error' => $e->getMessage(),
                    ]);
                    $logger($result->errors[array_key_last($result->errors)], 'error');
                }
            }
        }

        // Sync mail routing once at the end
        $this->syncMailRouting($logger);

        return $result;
    }

    /**
     * Provision email for a DirectAdmin/HestiaCP import account.
     *
     * @param  callable(string, string): void|null  $logger
     */
    public function provisionForImportAccount(
        User $user,
        ServerImportAccount $account,
        ?callable $logger = null,
    ): MigrationEmailProvisionResult {
        $emailAccounts = $account->email_accounts ?? [];
        $mainDomain = $account->main_domain;

        if (empty($emailAccounts) || empty($mainDomain)) {
            return new MigrationEmailProvisionResult;
        }

        // email_accounts contains local parts or full emails — normalize
        $mailboxes = [];
        foreach ($emailAccounts as $emailAccount) {
            if (str_contains($emailAccount, '@')) {
                [$localPart, $domain] = explode('@', $emailAccount, 2);
                $mailboxes[] = [
                    'email' => $emailAccount,
                    'local_part' => $localPart,
                    'domain' => $domain,
                ];
            } else {
                $mailboxes[] = [
                    'email' => "{$emailAccount}@{$mainDomain}",
                    'local_part' => $emailAccount,
                    'domain' => $mainDomain,
                ];
            }
        }

        return $this->provisionFromDiscoveredData($user, ['mailboxes' => $mailboxes], $logger);
    }

    /**
     * Enable email for a domain on the server (mirrors Email.php::getOrCreateEmailDomain).
     */
    private function enableEmailDomain(User $user, Domain $domain): EmailDomain
    {
        $emailDomain = $domain->emailDomain;
        if ($emailDomain) {
            return $emailDomain;
        }

        // Enable email on server via agent (also generates DKIM on Stalwart)
        $enableResult = $this->agent->emailEnableDomain($user->username, $domain->domain);

        // Create EmailDomain DB record
        $emailDomain = EmailDomain::create([
            'domain_id' => $domain->id,
            'is_active' => true,
        ]);

        // Sync Stalwart's DNS records (DKIM, SPF, DMARC) to BIND
        try {
            $stalwartDns = $enableResult['stalwart_dns_records'] ?? [];

            foreach ($stalwartDns as $rec) {
                $name = rtrim($rec['name'] ?? '', '.');
                $type = $rec['type'] ?? '';
                $content = $rec['content'] ?? '';

                if (empty($name) || empty($type) || empty($content)) {
                    continue;
                }

                // Strip the domain suffix from the name (BIND zone is relative)
                $name = preg_replace('/\.?'.preg_quote($domain->domain, '/').'\.?$/', '', $name);
                if (empty($name) || $name === $domain->domain) {
                    $name = '@';
                }

                // Update DKIM selector on EmailDomain
                if (str_contains($name, '_domainkey')) {
                    $selector = explode('.', $name)[0] ?? 'default';
                    $emailDomain->update(['dkim_selector' => $selector]);
                }

                DnsRecord::updateOrCreate(
                    [
                        'domain_id' => $domain->id,
                        'name' => $name,
                        'type' => $type,
                    ],
                    [
                        'content' => $content,
                        'ttl' => 3600,
                    ]
                );
            }
        } catch (Exception $e) {
            Log::warning("Migration email provision: DNS sync failed for {$domain->domain}", [
                'error' => $e->getMessage(),
            ]);
        }

        // Create autoconfig/autodiscover DNS records
        $serverIp = ServerFacts::serverIp('127.0.0.1');
        $defaultTtl = 3600;

        $autoconfigRecords = [
            ['name' => 'autoconfig', 'type' => 'A', 'content' => $serverIp, 'ttl' => $defaultTtl],
            ['name' => 'autodiscover', 'type' => 'A', 'content' => $serverIp, 'ttl' => $defaultTtl],
            ['name' => '_imaps._tcp', 'type' => 'SRV', 'content' => "1 993 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0],
            ['name' => '_pop3s._tcp', 'type' => 'SRV', 'content' => "1 995 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0],
            ['name' => '_submission._tcp', 'type' => 'SRV', 'content' => "1 587 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0],
        ];

        foreach ($autoconfigRecords as $record) {
            DnsRecord::firstOrCreate(
                [
                    'domain_id' => $domain->id,
                    'name' => $record['name'],
                    'type' => $record['type'],
                ],
                $record
            );
        }

        $this->regenerateDnsZone($domain);

        // Issue mail SSL certificate in background
        IssueSslCertificate::dispatch($domain->id, 'mail')->delay(now()->addSeconds(120));

        return $emailDomain;
    }

    /**
     * Create a mailbox via the agent and DB record.
     *
     * @return array{email: string, password: string}|null
     */
    private function createMailbox(User $user, EmailDomain $emailDomain, string $localPart, string $domainName): ?array
    {
        if (! $this->isValidLocalPart($localPart) || ! $this->isValidDomainName($domainName)) {
            return null;
        }

        // Check if mailbox already exists
        if (Mailbox::where('email_domain_id', $emailDomain->id)->where('local_part', $localPart)->exists()) {
            return null;
        }

        $email = "{$localPart}@{$domainName}";
        $tempPassword = Str::random(16);
        $quotaBytes = 1073741824; // 1GB default

        // Create mailbox on server via agent
        $result = $this->agent->mailboxCreate(
            $user->username,
            $email,
            $tempPassword,
            $quotaBytes
        );

        // Create DB record using agent-returned values
        Mailbox::create([
            'email_domain_id' => $emailDomain->id,
            'user_id' => $user->id,
            'local_part' => $localPart,
            'password_hash' => $result['password_hash'] ?? '',
            'password_encrypted' => Crypt::encryptString($tempPassword),
            'maildir_path' => $result['maildir_path'] ?? null,
            'system_uid' => $result['uid'] ?? null,
            'system_gid' => $result['gid'] ?? null,
            'name' => $localPart,
            'quota_bytes' => $quotaBytes,
            'is_active' => true,
            'imap_enabled' => true,
            'pop3_enabled' => true,
            'smtp_enabled' => true,
        ]);

        // Import Maildir data into Stalwart if available
        $maildirPath = "/home/{$user->username}/mail/{$domainName}/{$localPart}/";
        $this->importMaildirToStalwart($email, $maildirPath);

        return ['email' => $email, 'password' => $tempPassword];
    }

    /**
     * Create a forwarder via the agent and DB record.
     *
     * @param  array<int, string>  $destinations
     */
    private function createForwarder(User $user, EmailDomain $emailDomain, string $localPart, string $domainName, array $destinations): bool
    {
        if (! $this->isValidLocalPart($localPart) || ! $this->isValidDomainName($domainName)) {
            return false;
        }

        // Check if forwarder or mailbox with same address already exists
        if (EmailForwarder::where('email_domain_id', $emailDomain->id)->where('local_part', $localPart)->exists()) {
            return false;
        }
        if (Mailbox::where('email_domain_id', $emailDomain->id)->where('local_part', $localPart)->exists()) {
            return false;
        }

        $email = "{$localPart}@{$domainName}";

        // Create forwarder on server via agent
        $this->agent->send('email.forwarder_create', [
            'username' => $user->username,
            'email' => $email,
            'destinations' => $destinations,
        ]);

        // Create DB record
        EmailForwarder::create([
            'email_domain_id' => $emailDomain->id,
            'user_id' => $user->id,
            'local_part' => $localPart,
            'destinations' => $destinations,
            'is_active' => true,
        ]);

        // Sync Stalwart identities for forwarder
        if (config('jabali.mail_backend') === 'stalwart') {
            $this->syncStalwartIdentitiesForForwarder($email, $destinations);
        }

        return true;
    }

    /**
     * Import Maildir data into Stalwart via its REST API.
     */
    private function importMaildirToStalwart(string $email, string $maildirPath): void
    {
        if (config('jabali.mail_backend') !== 'stalwart') {
            return;
        }

        if (! is_dir($maildirPath)) {
            return;
        }

        try {
            $this->agent->send('email.stalwart_import_maildir', [
                'account' => $email,
                'maildir_path' => $maildirPath,
                'format' => 'maildir-nested',
            ]);
        } catch (Exception $e) {
            Log::warning("Migration: Maildir import to Stalwart failed for {$email}", [
                'error' => $e->getMessage(),
                'path' => $maildirPath,
            ]);
        }
    }

    /**
     * Sync Stalwart identities for a forwarder (mirrors Email.php logic).
     *
     * @param  array<int, string>  $destinations
     */
    private function syncStalwartIdentitiesForForwarder(string $forwarderEmail, array $destinations): void
    {
        try {
            foreach ($destinations as $destination) {
                $mailbox = $this->findLocalMailbox($destination);
                if (! $mailbox) {
                    continue;
                }

                $this->agent->send('email.stalwart_add_identity', [
                    'email' => $mailbox->email,
                    'identity' => $forwarderEmail,
                    'password' => $mailbox->plain_password ?? '',
                ]);
            }
        } catch (Exception $e) {
            Log::warning("Migration: Failed to sync Stalwart identities for forwarder {$forwarderEmail}", [
                'error' => $e->getMessage(),
            ]);
        }
    }

    /**
     * Find a local mailbox by email address.
     */
    private function findLocalMailbox(string $email): ?Mailbox
    {
        $parts = explode('@', $email);
        if (count($parts) !== 2) {
            return null;
        }

        [$localPart, $domainName] = $parts;

        return Mailbox::whereHas('emailDomain.domain', function ($q) use ($domainName) {
            $q->where('domain', $domainName);
        })->where('local_part', $localPart)->first();
    }

    /**
     * Regenerate BIND DNS zone for a domain (mirrors Email.php logic).
     */
    private function regenerateDnsZone(Domain $domain): void
    {
        try {
            $records = DnsRecord::where('domain_id', $domain->id)->get()->toArray();
            $settings = DnsSetting::getAll();
            $hostname = gethostname() ?: 'localhost';
            $serverIp = ServerFacts::serverIp('127.0.0.1');
            $serverIpv6 = $settings['default_ipv6'] ?? null;

            $this->agent->send('dns.sync_zone', [
                'domain' => $domain->domain,
                'records' => $records,
                'ns1' => $settings['ns1'] ?? "ns1.{$hostname}",
                'ns2' => $settings['ns2'] ?? "ns2.{$hostname}",
                'admin_email' => $settings['admin_email'] ?? "admin.{$hostname}",
                'default_ip' => $settings['default_ip'] ?? $serverIp,
                'default_ipv6' => $serverIpv6,
                'default_ttl' => $settings['default_ttl'] ?? 3600,
            ]);
        } catch (Exception $e) {
            Log::warning("Migration: DNS zone regeneration failed for {$domain->domain}", [
                'error' => $e->getMessage(),
            ]);
        }
    }

    /**
     * Sync mail routing maps via agent.
     *
     * @param  callable(string, string): void  $logger
     */
    private function syncMailRouting(callable $logger): void
    {
        try {
            $this->mailRoutingSyncService->sync();
        } catch (Exception $e) {
            $logger(__('Mail routing sync failed: :error', ['error' => $e->getMessage()]), 'warning');
        }
    }

    /**
     * Group mailbox/forwarder data by domain name.
     *
     * @param  array<int, array<string, mixed>>  $items
     * @return array<string, array<int, array<string, mixed>>>
     */
    private function groupByDomain(array $items): array
    {
        $grouped = [];
        foreach ($items as $item) {
            $domain = $item['domain'] ?? '';
            if (empty($domain)) {
                continue;
            }
            $grouped[$domain][] = $item;
        }

        return $grouped;
    }

    /**
     * Parse destinations string or array into a validated array of email addresses.
     *
     * @param  string|array<int, string>  $destinations
     * @return array<int, string>
     */
    private function parseDestinations(string|array $destinations): array
    {
        if (is_string($destinations)) {
            $destinations = array_map('trim', explode(',', $destinations));
        }

        return array_values(array_filter(
            $destinations,
            fn ($d) => filter_var($d, FILTER_VALIDATE_EMAIL)
        ));
    }

    private function isValidLocalPart(string $localPart): bool
    {
        return (bool) preg_match('/^[a-zA-Z0-9._%+\-]+$/', $localPart);
    }

    private function isValidDomainName(string $domain): bool
    {
        return (bool) preg_match('/^(?:[a-z0-9](?:[a-z0-9\-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$/i', $domain);
    }
}
