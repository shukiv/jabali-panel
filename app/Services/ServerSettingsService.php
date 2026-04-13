<?php

declare(strict_types=1);

namespace App\Services;

use App\Models\DnsSetting;
use App\Services\Agent\AgentClient;
use App\Services\Dns\PowerDnsService;

class ServerSettingsService
{
    public function __construct(private AgentClient $agent) {}

    /**
     * @return array{success: bool, error?: string}
     */
    public function saveHostname(string $hostname): array
    {
        $result = $this->agent->send('server.set_hostname', ['hostname' => $hostname]);

        if (! ($result['success'] ?? false)) {
            return ['success' => false, 'error' => $result['error'] ?? 'Unknown error'];
        }

        $failedServices = [];
        foreach (['stalwart-mail', 'nginx', 'pdns'] as $service) {
            try {
                $action = $service === 'nginx' ? 'reload' : 'restart';
                $r = $this->agent->send("service.{$action}", ['service' => $service]);
                if (! ($r['success'] ?? false)) {
                    $failedServices[] = $service;
                }
            } catch (\Throwable $e) {
                $failedServices[] = $service;
            }
        }

        return ['success' => true, 'failed_services' => $failedServices];
    }

    public function saveTimezone(string $timezone): bool
    {
        DnsSetting::set('server_timezone', $timezone);

        $result = $this->agent->send('server.set_timezone', ['timezone' => $timezone]);

        return $result['success'] ?? false;
    }

    /**
     * @return array{success: bool, error?: string}
     */
    public function savePanelPort(int $port): array
    {
        $result = $this->agent->send('server.set_panel_port', ['port' => $port]);

        if (! ($result['success'] ?? false)) {
            return ['success' => false, 'error' => $result['error'] ?? 'Unknown error'];
        }

        return ['success' => true];
    }

    /**
     * @param  array<string, mixed>  $data
     * @return array{success: bool, error?: string}
     */
    public function saveDns(array $data, string $hostname): array
    {
        DnsSetting::set('ns1', $data['ns1']);
        DnsSetting::set('ns1_ip', $data['ns1_ip']);
        DnsSetting::set('ns2', $data['ns2']);
        DnsSetting::set('ns2_ip', $data['ns2_ip']);
        DnsSetting::set('default_ip', $data['default_ip']);
        DnsSetting::set('default_ipv6', $data['default_ipv6'] ?: null);
        DnsSetting::set('default_ttl', $data['default_ttl']);
        DnsSetting::set('admin_email', $data['admin_email']);
        DnsSetting::clearCache();

        try {
            app(PowerDnsService::class)->createZone(
                $hostname,
                [$data['ns1'], $data['ns2']],
                $data['default_ip'],
                $data['default_ipv6'] ?: null
            );

            return ['success' => true];
        } catch (\Throwable $e) {
            return ['success' => false, 'error' => $e->getMessage()];
        }
    }

    /**
     * @param  array<string, string>  $nameservers
     * @return array{success: bool, error?: string}
     */
    public function saveResolvers(array $nameservers): array
    {
        $result = $this->agent->send('server.set_resolvers', ['nameservers' => $nameservers]);

        if ($result['success'] ?? false) {
            DnsSetting::set('custom_resolvers', implode(',', $nameservers));
            DnsSetting::clearCache();
        }

        return [
            'success' => $result['success'] ?? false,
            'error' => $result['error'] ?? null,
        ];
    }

    /**
     * @param  array<string, mixed>  $settings
     */
    public function saveFpmSettings(array $settings): void
    {
        foreach ($settings as $key => $value) {
            DnsSetting::set("fpm_{$key}", (string) $value);
        }
        DnsSetting::clearCache();
    }

    /**
     * @return array{success: bool, error?: string, results?: array<string, mixed>}
     */
    public function applyFpmToAllUsers(array $settings): array
    {
        $result = $this->agent->send('php.update_all_pool_limits', $settings);

        return [
            'success' => $result['success'] ?? false,
            'error' => $result['error'] ?? null,
            'results' => $result['results'] ?? [],
        ];
    }

    public function saveBranding(string $panelName): void
    {
        DnsSetting::set('panel_name', trim($panelName));
        DnsSetting::clearCache();
    }

    /**
     * @param  array<string, mixed>  $data
     */
    public function saveQuotaSettings(array $data): void
    {
        foreach ($data as $key => $value) {
            DnsSetting::set($key, (string) $value);
        }
        DnsSetting::clearCache();
    }

    /**
     * @param  array<string, mixed>  $data
     */
    public function saveFileManagerSettings(array $data): void
    {
        foreach ($data as $key => $value) {
            DnsSetting::set("fm_{$key}", is_bool($value) ? ($value ? '1' : '0') : (string) $value);
        }
        DnsSetting::clearCache();
    }

    /**
     * @param  array<string, mixed>  $data
     */
    public function saveEmailSettings(array $data): void
    {
        foreach ($data as $key => $value) {
            DnsSetting::set($key, is_bool($value) ? ($value ? '1' : '0') : (string) ($value ?? ''));
        }
        DnsSetting::clearCache();
    }
}
