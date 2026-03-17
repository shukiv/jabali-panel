<?php

declare(strict_types=1);

namespace App\Observers;

use App\Jobs\IssueSslCertificate;
use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\Domain;
use App\Services\Agent\AgentClient;
use App\Support\ServerFacts;
use Exception;
use Illuminate\Support\Facades\Log;

class DomainObserver
{
    public function created(Domain $domain): void
    {
        $this->createDefaultDnsRecords($domain);
        $this->createDnsZone($domain);
        $this->scheduleSSLIssuance($domain);
    }

    protected function scheduleSSLIssuance(Domain $domain): void
    {
        // Dispatch SSL issuance job with a 120 second delay
        // This gives time for DNS to propagate and web server to be configured
        IssueSslCertificate::dispatch($domain->id)->delay(now()->addSeconds(120));
        Log::info("Scheduled SSL certificate issuance for {$domain->domain}");
    }

    public function deleted(Domain $domain): void
    {
        try {
            $agent = app(AgentClient::class);
            $agent->send('dns.delete_zone', ['domain' => $domain->domain]);
        } catch (Exception $e) {
            Log::warning("Failed to delete DNS zone for {$domain->domain}: ".$e->getMessage());
        }
    }

    protected function createDefaultDnsRecords(Domain $domain): void
    {
        $settings = DnsSetting::getAll();
        $defaultIp = $domain->ip_address ?: ($settings['default_ip'] ?? $this->getServerIp());
        $defaultIpv6 = $domain->ipv6_address ?: ($settings['default_ipv6'] ?? null);
        $defaultTtl = (int) ($settings['default_ttl'] ?? 3600);
        $hostname = $this->getServerHostname();
        $ns1 = $settings['ns1'] ?? "ns1.{$hostname}";
        $ns2 = $settings['ns2'] ?? "ns2.{$hostname}";

        $defaultRecords = [
            // NS records
            ['name' => '@', 'type' => 'NS', 'content' => $ns1, 'ttl' => $defaultTtl],
            ['name' => '@', 'type' => 'NS', 'content' => $ns2, 'ttl' => $defaultTtl],
            // A records
            ['name' => '@', 'type' => 'A', 'content' => $defaultIp, 'ttl' => $defaultTtl],
            ['name' => 'www', 'type' => 'A', 'content' => $defaultIp, 'ttl' => $defaultTtl],
            ['name' => 'mail', 'type' => 'A', 'content' => $defaultIp, 'ttl' => $defaultTtl],
            // MX record
            ['name' => '@', 'type' => 'MX', 'content' => "mail.{$domain->domain}", 'ttl' => $defaultTtl, 'priority' => 10],
            // SPF record - allows mail from this server
            ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 mx a ~all', 'ttl' => $defaultTtl],
            // DMARC record - basic policy
            ['name' => '_dmarc', 'type' => 'TXT', 'content' => 'v=DMARC1; p=none; rua=mailto:postmaster@'.$domain->domain, 'ttl' => $defaultTtl],
        ];

        if (! empty($defaultIpv6)) {
            $defaultRecords[] = ['name' => '@', 'type' => 'AAAA', 'content' => $defaultIpv6, 'ttl' => $defaultTtl];
            $defaultRecords[] = ['name' => 'www', 'type' => 'AAAA', 'content' => $defaultIpv6, 'ttl' => $defaultTtl];
            $defaultRecords[] = ['name' => 'mail', 'type' => 'AAAA', 'content' => $defaultIpv6, 'ttl' => $defaultTtl];
        }

        // Autoconfig/Autodiscover A records
        $defaultRecords[] = ['name' => 'autoconfig', 'type' => 'A', 'content' => $defaultIp, 'ttl' => $defaultTtl];
        $defaultRecords[] = ['name' => 'autodiscover', 'type' => 'A', 'content' => $defaultIp, 'ttl' => $defaultTtl];

        // SRV records for mail client auto-discovery
        $defaultRecords[] = ['name' => '_imaps._tcp', 'type' => 'SRV', 'content' => "1 993 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0];
        $defaultRecords[] = ['name' => '_pop3s._tcp', 'type' => 'SRV', 'content' => "1 995 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0];
        $defaultRecords[] = ['name' => '_submission._tcp', 'type' => 'SRV', 'content' => "1 587 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0];

        // Add server hostname A record if this domain is the server's base domain
        $hostnameLabel = $this->getServerHostnameLabel($domain->domain);
        if ($hostnameLabel !== null) {
            $defaultRecords[] = ['name' => $hostnameLabel, 'type' => 'A', 'content' => $defaultIp, 'ttl' => $defaultTtl];
            if (! empty($defaultIpv6)) {
                $defaultRecords[] = ['name' => $hostnameLabel, 'type' => 'AAAA', 'content' => $defaultIpv6, 'ttl' => $defaultTtl];
            }
        }

        $defaultRecords = $this->appendNameserverRecords(
            $defaultRecords,
            $domain->domain,
            $ns1,
            $ns2,
            $defaultIp,
            $defaultIpv6,
            $defaultTtl
        );

        foreach ($defaultRecords as $record) {
            DnsRecord::create(array_merge(['domain_id' => $domain->id], $record));
        }
    }

    protected function createDnsZone(Domain $domain): void
    {
        try {
            $settings = DnsSetting::getAll();
            $records = DnsRecord::where('domain_id', $domain->id)->get()->toArray();
            $hostname = $this->getServerHostname();
            $serverIp = $this->getServerIp();
            $serverIpv6 = $settings['default_ipv6'] ?? null;

            $agent = app(AgentClient::class);
            $agent->send('dns.sync_zone', [
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
            Log::warning("Failed to create DNS zone for {$domain->domain}: ".$e->getMessage());
        }
    }

    protected function getServerHostname(): string
    {
        return gethostname() ?: 'localhost';
    }

    /**
     * If the server FQDN is a subdomain of the given domain, return the hostname label.
     * E.g., FQDN "web02.example.com" + domain "example.com" → "web02"
     * Returns null if the domain doesn't match or there's no subdomain part.
     */
    protected function getServerHostnameLabel(string $domain): ?string
    {
        $fqdn = $this->getServerHostname();
        $suffix = '.'.$domain;

        if (str_ends_with($fqdn, $suffix)) {
            $label = substr($fqdn, 0, -strlen($suffix));
            if ($label !== '' && ! str_contains($label, '.')) {
                return $label;
            }
        }

        return null;
    }

    protected function getServerIp(): string
    {
        return ServerFacts::serverIp('127.0.0.1');
    }

    /**
     * @param  array<int, array<string, mixed>>  $records
     * @return array<int, array<string, mixed>>
     */
    protected function appendNameserverRecords(
        array $records,
        string $domain,
        string $ns1,
        string $ns2,
        string $ipv4,
        ?string $ipv6,
        int $ttl
    ): array {
        $labels = $this->getNameserverLabels($domain, [$ns1, $ns2]);

        if ($labels === []) {
            return $records;
        }

        $existingA = array_map(
            fn (array $record): string => $record['name'] ?? '',
            array_filter($records, fn (array $record): bool => ($record['type'] ?? '') === 'A')
        );
        $existingAAAA = array_map(
            fn (array $record): string => $record['name'] ?? '',
            array_filter($records, fn (array $record): bool => ($record['type'] ?? '') === 'AAAA')
        );

        foreach ($labels as $label) {
            if (! in_array($label, $existingA, true)) {
                $records[] = ['name' => $label, 'type' => 'A', 'content' => $ipv4, 'ttl' => $ttl];
            }

            if ($ipv6 && ! in_array($label, $existingAAAA, true)) {
                $records[] = ['name' => $label, 'type' => 'AAAA', 'content' => $ipv6, 'ttl' => $ttl];
            }
        }

        return $records;
    }

    /**
     * @param  array<int, string>  $nameservers
     * @return array<int, string>
     */
    protected function getNameserverLabels(string $domain, array $nameservers): array
    {
        $domain = rtrim($domain, '.');
        $labels = [];

        foreach ($nameservers as $nameserver) {
            $nameserver = rtrim($nameserver ?? '', '.');

            if ($nameserver === '') {
                continue;
            }

            if ($nameserver === $domain) {
                $label = '@';
            } elseif (str_ends_with($nameserver, '.'.$domain)) {
                $label = substr($nameserver, 0, -strlen('.'.$domain));
            } else {
                continue;
            }

            if ($label !== '@') {
                $labels[] = $label;
            }
        }

        return array_values(array_unique($labels));
    }
}
