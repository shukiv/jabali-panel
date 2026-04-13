<?php

declare(strict_types=1);

namespace App\Services\Dns;

use Illuminate\Http\Client\PendingRequest;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Log;

class PowerDnsService
{
    private string $apiUrl;

    private string $apiKey;

    private string $serverId;

    public function __construct()
    {
        $this->apiUrl = rtrim(config('powerdns.api_url', 'http://127.0.0.1:8081'), '/');
        $this->apiKey = config('powerdns.api_key', '');
        $this->serverId = config('powerdns.server_id', 'localhost');
    }

    /**
     * Create a new DNS zone with default SOA and NS records.
     *
     * @param  array<string>  $nameservers  e.g. ['ns1.example.com', 'ns2.example.com']
     */
    public function createZone(string $domain, array $nameservers, string $serverIp, ?string $serverIpv6 = null): void
    {
        $zone = $this->ensureTrailingDot($domain);
        $ns = array_map(fn (string $ns) => $this->ensureTrailingDot($ns), $nameservers);

        $response = $this->client()->post($this->zonesUrl(), [
            'name' => $zone,
            'kind' => 'Native',
            'nameservers' => $ns,
        ]);

        if ($response->failed()) {
            $this->handleError('createZone', $domain, $response);
        }
    }

    /**
     * Delete a DNS zone entirely.
     */
    public function deleteZone(string $domain): void
    {
        $zone = $this->ensureTrailingDot($domain);

        $response = $this->client()->delete($this->zoneUrl($zone));

        if ($response->failed() && $response->status() !== 404) {
            $this->handleError('deleteZone', $domain, $response);
        }
    }

    /**
     * Get zone details including all RRsets.
     *
     * @api Part of the DNS service API surface alongside createZone /
     *      deleteZone / setRecords; used by ops tooling and CLI commands
     *      that inspect a zone's current state.
     *
     * @return array<string, mixed>
     */
    public function getZone(string $domain): array
    {
        $zone = $this->ensureTrailingDot($domain);

        $response = $this->client()->get($this->zoneUrl($zone));

        if ($response->failed()) {
            $this->handleError('getZone', $domain, $response);
        }

        return $response->json();
    }

    /**
     * Replace all records in a zone from a flat array of records.
     *
     * @param  array<int, array{name: string, type: string, content: string, ttl: int, priority?: int}>  $records
     */
    public function setRecords(string $domain, array $records): void
    {
        $zone = $this->ensureTrailingDot($domain);

        // Group records by name+type into RRsets
        $rrsets = [];
        foreach ($records as $record) {
            $name = $this->ensureTrailingDot($this->qualifyName($record['name'], $domain));
            $type = strtoupper($record['type']);
            $key = "{$name}|{$type}";

            if (! isset($rrsets[$key])) {
                $rrsets[$key] = [
                    'name' => $name,
                    'type' => $type,
                    'ttl' => $record['ttl'] ?? 3600,
                    'changetype' => 'REPLACE',
                    'records' => [],
                ];
            }

            $content = $record['content'];

            // MX records need priority prepended
            if ($type === 'MX') {
                $priority = $record['priority'] ?? 10;
                $content = "{$priority} ".$this->ensureTrailingDot($content);
            }

            // TXT records must be quoted for PowerDNS
            if ($type === 'TXT' && ! str_starts_with($content, '"')) {
                $content = '"'.$content.'"';
            }

            // SRV content must be: priority weight port target
            // DB stores weight+port+target in content, priority separate
            if ($type === 'SRV') {
                $priority = $record['priority'] ?? 0;
                $parts = preg_split('/\s+/', trim($content));
                if (count($parts) === 3) {
                    // Content is "weight port target" — prepend priority
                    $content = "{$priority} {$content}";
                }
                // Ensure target has trailing dot
                if (! str_ends_with($content, '.')) {
                    $content .= '.';
                }
            }

            // CNAME and NS targets need trailing dot
            if (in_array($type, ['CNAME', 'NS'])) {
                $content = $this->ensureTrailingDot($content);
            }

            $rrsets[$key]['records'][] = [
                'content' => $content,
                'disabled' => false,
            ];
        }

        $response = $this->client()->patch($this->zoneUrl($zone), [
            'rrsets' => array_values($rrsets),
        ]);

        if ($response->failed()) {
            $this->handleError('setRecords', $domain, $response);
        }
    }

    /**
     * Add or replace a single RRset.
     */
    public function addRecord(string $domain, string $name, string $type, string $content, int $ttl = 3600, int $priority = 0): void
    {
        $zone = $this->ensureTrailingDot($domain);
        $fqdn = $this->ensureTrailingDot($this->qualifyName($name, $domain));
        $type = strtoupper($type);

        if ($type === 'MX') {
            $content = "{$priority} ".$this->ensureTrailingDot($content);
        }

        if ($type === 'TXT' && ! str_starts_with($content, '"')) {
            $content = '"'.$content.'"';
        }

        if (in_array($type, ['CNAME', 'NS'])) {
            $content = $this->ensureTrailingDot($content);
        }

        $response = $this->client()->patch($this->zoneUrl($zone), [
            'rrsets' => [[
                'name' => $fqdn,
                'type' => $type,
                'ttl' => $ttl,
                'changetype' => 'REPLACE',
                'records' => [[
                    'content' => $content,
                    'disabled' => false,
                ]],
            ]],
        ]);

        if ($response->failed()) {
            $this->handleError('addRecord', $domain, $response);
        }
    }

    /**
     * Delete a specific RRset from a zone.
     */
    public function deleteRecord(string $domain, string $name, string $type): void
    {
        $zone = $this->ensureTrailingDot($domain);
        $fqdn = $this->ensureTrailingDot($this->qualifyName($name, $domain));

        $response = $this->client()->patch($this->zoneUrl($zone), [
            'rrsets' => [[
                'name' => $fqdn,
                'type' => strtoupper($type),
                'changetype' => 'DELETE',
            ]],
        ]);

        if ($response->failed()) {
            $this->handleError('deleteRecord', $domain, $response);
        }
    }

    /**
     * Enable DNSSEC for a zone by creating a CSK (Combined Signing Key).
     *
     * @return array{ds: array<string>, keytype: string, algorithm: string}
     */
    public function enableDnssec(string $domain): array
    {
        $zone = $this->ensureTrailingDot($domain);

        $response = $this->client()->post("{$this->zoneUrl($zone)}/cryptokeys", [
            'keytype' => 'csk',
            'active' => true,
        ]);

        if ($response->failed()) {
            $this->handleError('enableDnssec', $domain, $response);
        }

        return $response->json();
    }

    /**
     * Disable DNSSEC by removing all cryptokeys.
     */
    public function disableDnssec(string $domain): void
    {
        $zone = $this->ensureTrailingDot($domain);
        $keys = $this->getCryptokeys($domain);

        foreach ($keys as $key) {
            $keyId = $key['id'] ?? null;
            if ($keyId !== null) {
                $this->client()->delete("{$this->zoneUrl($zone)}/cryptokeys/{$keyId}");
            }
        }
    }

    /**
     * Get DNSSEC status for a zone.
     *
     * @return array{enabled: bool, keys: array<mixed>}
     */
    public function getDnssecStatus(string $domain): array
    {
        $keys = $this->getCryptokeys($domain);

        return [
            'enabled' => count($keys) > 0,
            'keys' => $keys,
        ];
    }

    /**
     * Get DS records for registrar configuration.
     *
     * @return array<int, array{tag: int, algorithm: int, digest_type: int, digest: string}>
     */
    public function getDsRecords(string $domain): array
    {
        $keys = $this->getCryptokeys($domain);
        $dsRecords = [];

        foreach ($keys as $key) {
            foreach ($key['ds'] ?? [] as $ds) {
                // DS format: "tag algorithm digesttype digest"
                $parts = explode(' ', $ds, 4);
                if (count($parts) === 4) {
                    $dsRecords[] = [
                        'tag' => (int) $parts[0],
                        'algorithm' => (int) $parts[1],
                        'digest_type' => (int) $parts[2],
                        'digest' => $parts[3],
                    ];
                }
            }
        }

        return $dsRecords;
    }

    /**
     * Check if a zone exists in PowerDNS.
     */
    public function zoneExists(string $domain): bool
    {
        $zone = $this->ensureTrailingDot($domain);

        $response = $this->client()->get($this->zoneUrl($zone));

        return $response->successful();
    }

    /**
     * @return array<mixed>
     */
    private function getCryptokeys(string $domain): array
    {
        $zone = $this->ensureTrailingDot($domain);

        $response = $this->client()->get("{$this->zoneUrl($zone)}/cryptokeys");

        if ($response->failed()) {
            return [];
        }

        return $response->json() ?? [];
    }

    private function client(): PendingRequest
    {
        return Http::baseUrl($this->apiUrl)
            ->withHeaders(['X-API-Key' => $this->apiKey])
            ->acceptJson()
            ->asJson()
            ->timeout(10);
    }

    private function zonesUrl(): string
    {
        return "/api/v1/servers/{$this->serverId}/zones";
    }

    private function zoneUrl(string $zone): string
    {
        return "/api/v1/servers/{$this->serverId}/zones/{$zone}";
    }

    private function ensureTrailingDot(string $name): string
    {
        return str_ends_with($name, '.') ? $name : "{$name}.";
    }

    /**
     * Turn a record name into a FQDN.
     * "@" or "" → domain itself; "www" → "www.domain"; "www.domain." → as-is
     */
    private function qualifyName(string $name, string $domain): string
    {
        if ($name === '@' || $name === '' || $name === $domain) {
            return $domain;
        }

        // Already a FQDN (ends with domain or trailing dot)
        if (str_ends_with($name, ".{$domain}") || str_ends_with($name, ".{$domain}.") || str_ends_with($name, '.')) {
            return $name;
        }

        return "{$name}.{$domain}";
    }

    private function handleError(string $method, string $domain, \Illuminate\Http\Client\Response $response): void
    {
        $error = $response->json('error') ?? $response->body();
        Log::error("PowerDNS {$method} failed for {$domain}: {$error}");

        throw new \RuntimeException("PowerDNS {$method} failed for {$domain}: {$error}");
    }
}
