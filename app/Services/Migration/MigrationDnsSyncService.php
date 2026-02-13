<?php

declare(strict_types=1);

namespace App\Services\Migration;

use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\Domain;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Exception;
use Illuminate\Database\Eloquent\Collection as EloquentCollection;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\Log;

class MigrationDnsSyncService
{
    public function __construct(private AgentClient $agent) {}

    /**
     * @param  array<int, string|array<string, mixed>>|null  $domainNames
     */
    public function syncDomainsForUser(User $user, ?array $domainNames = null): void
    {
        $query = Domain::query()->where('user_id', $user->id);

        if ($domainNames !== null) {
            $normalized = $this->normalizeDomainNames($domainNames);
            if ($normalized === []) {
                return;
            }

            $query->whereIn('domain', $normalized);
        }

        /** @var EloquentCollection<int, Domain> $domains */
        $domains = $query->get();

        foreach ($domains as $domain) {
            $this->syncDomain($domain);
        }
    }

    public function syncDomain(Domain $domain): void
    {
        try {
            $records = $this->ensureDnsRecords($domain);
            if ($records->isEmpty()) {
                return;
            }

            $settings = DnsSetting::getAll();
            $hostname = $this->getServerHostname();
            $serverIp = $this->getServerIp();
            $serverIpv6 = $settings['default_ipv6'] ?? null;

            $this->agent->send('dns.sync_zone', [
                'domain' => $domain->domain,
                'records' => $this->formatRecords($records),
                'ns1' => $settings['ns1'] ?? "ns1.{$hostname}",
                'ns2' => $settings['ns2'] ?? "ns2.{$hostname}",
                'admin_email' => $settings['admin_email'] ?? "admin.{$hostname}",
                'default_ip' => $settings['default_ip'] ?? $serverIp,
                'default_ipv6' => $serverIpv6,
                'default_ttl' => (int) ($settings['default_ttl'] ?? 3600),
            ]);
        } catch (Exception $e) {
            Log::warning("Failed to sync DNS zone for {$domain->domain}: {$e->getMessage()}");
        }
    }

    /**
     * @return Collection<int, DnsRecord>
     */
    protected function ensureDnsRecords(Domain $domain): Collection
    {
        $records = $domain->dnsRecords()->get();
        $defaultRecords = $this->getDefaultRecords($domain);

        foreach ($defaultRecords as $record) {
            if (! $this->shouldCreateDefaultRecord($records, $record)) {
                continue;
            }

            $domain->dnsRecords()->create($record);
        }

        return $domain->dnsRecords()->get();
    }

    /**
     * @return array<int, array<string, mixed>>
     */
    protected function getDefaultRecords(Domain $domain): array
    {
        $settings = DnsSetting::getAll();
        $defaultIp = $domain->ip_address ?: ($settings['default_ip'] ?? $this->getServerIp());
        $defaultIpv6 = $domain->ipv6_address ?: ($settings['default_ipv6'] ?? null);
        $defaultTtl = (int) ($settings['default_ttl'] ?? 3600);
        $hostname = $this->getServerHostname();
        $ns1 = $settings['ns1'] ?? "ns1.{$hostname}";
        $ns2 = $settings['ns2'] ?? "ns2.{$hostname}";

        $records = [
            ['name' => '@', 'type' => 'NS', 'content' => $ns1, 'ttl' => $defaultTtl],
            ['name' => '@', 'type' => 'NS', 'content' => $ns2, 'ttl' => $defaultTtl],
            ['name' => '@', 'type' => 'A', 'content' => $defaultIp, 'ttl' => $defaultTtl],
            ['name' => 'www', 'type' => 'A', 'content' => $defaultIp, 'ttl' => $defaultTtl],
            ['name' => 'mail', 'type' => 'A', 'content' => $defaultIp, 'ttl' => $defaultTtl],
            ['name' => '@', 'type' => 'MX', 'content' => "mail.{$domain->domain}", 'ttl' => $defaultTtl, 'priority' => 10],
            ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 mx a ~all', 'ttl' => $defaultTtl],
            ['name' => '_dmarc', 'type' => 'TXT', 'content' => "v=DMARC1; p=none; rua=mailto:postmaster@{$domain->domain}", 'ttl' => $defaultTtl],
        ];

        if (! empty($defaultIpv6)) {
            $records[] = ['name' => '@', 'type' => 'AAAA', 'content' => $defaultIpv6, 'ttl' => $defaultTtl];
            $records[] = ['name' => 'www', 'type' => 'AAAA', 'content' => $defaultIpv6, 'ttl' => $defaultTtl];
            $records[] = ['name' => 'mail', 'type' => 'AAAA', 'content' => $defaultIpv6, 'ttl' => $defaultTtl];
        }

        $records = $this->appendNameserverRecords(
            $records,
            $domain->domain,
            $ns1,
            $ns2,
            $defaultIp,
            $defaultIpv6,
            $defaultTtl
        );

        return $records;
    }

    /**
     * @param  Collection<int, DnsRecord>  $records
     * @param  array<string, mixed>  $record
     */
    protected function shouldCreateDefaultRecord(Collection $records, array $record): bool
    {
        $name = $record['name'] ?? '';
        $type = $record['type'] ?? '';

        if ($type === 'TXT' && $name === '@') {
            return ! $records->contains(function (DnsRecord $existing): bool {
                return $existing->type === 'TXT'
                    && $existing->name === '@'
                    && str_contains(strtolower($existing->content), 'v=spf1');
            });
        }

        if ($type === 'TXT' && $name === '_dmarc') {
            return ! $records->contains(function (DnsRecord $existing): bool {
                return $existing->type === 'TXT'
                    && $existing->name === '_dmarc';
            });
        }

        return ! $records->contains(function (DnsRecord $existing) use ($name, $type): bool {
            return $existing->type === $type
                && $existing->name === $name;
        });
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

    /**
     * @param  Collection<int, DnsRecord>  $records
     * @return array<int, array<string, mixed>>
     */
    protected function formatRecords(Collection $records): array
    {
        return $records->map(static function (DnsRecord $record): array {
            return [
                'name' => $record->name,
                'type' => $record->type,
                'content' => $record->content,
                'ttl' => $record->ttl,
                'priority' => $record->priority,
            ];
        })->all();
    }

    /**
     * @param  array<int, string|array<string, mixed>>  $domains
     * @return array<int, string>
     */
    protected function normalizeDomainNames(array $domains): array
    {
        $names = [];

        foreach ($domains as $domain) {
            if (is_array($domain)) {
                $name = $domain['name'] ?? $domain['domain'] ?? null;
            } else {
                $name = $domain;
            }

            if (! is_string($name) || $name === '') {
                continue;
            }

            $names[] = strtolower(trim($name));
        }

        return array_values(array_unique($names));
    }

    protected function getServerHostname(): string
    {
        return gethostname() ?: 'localhost';
    }

    protected function getServerIp(): string
    {
        $ip = trim(shell_exec("hostname -I | awk '{print $1}'") ?? '');

        return $ip !== '' ? $ip : '127.0.0.1';
    }
}
