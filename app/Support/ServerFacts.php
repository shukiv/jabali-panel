<?php

declare(strict_types=1);

namespace App\Support;

use App\Services\Agent\AgentClient;
use Exception;
use Illuminate\Support\Facades\Cache;

final class ServerFacts
{
    /**
     * Fetch server facts from jabali-agent when available.
     *
     * @return array<string, mixed>
     */
    public static function agentInfo(): array
    {
        // Keep this short-lived; it is used from request contexts.
        return Cache::remember('server.facts.agent_info', 30, function (): array {
            try {
                $agent = app(AgentClient::class);
                $result = $agent->send('server.info', []);

                if (! ($result['success'] ?? false)) {
                    return [];
                }

                return is_array($result['info'] ?? null) ? $result['info'] : [];
            } catch (Exception) {
                return [];
            }
        });
    }

    public static function serverIp(string $fallback = '127.0.0.1'): string
    {
        $ip = trim((string) (self::agentInfo()['server_ip'] ?? ''));

        if (filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) !== false) {
            return $ip;
        }

        // Best-effort fallback without shelling out.
        $host = gethostname() ?: 'localhost';
        $ip = gethostbyname($host);

        if (filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) !== false) {
            return $ip;
        }

        return $fallback;
    }

    public static function isSystemdResolved(): bool
    {
        // Prefer filesystem-based detection to avoid shelling out.
        if (@is_link('/etc/resolv.conf')) {
            $target = (string) @readlink('/etc/resolv.conf');
            if (str_contains($target, 'systemd/resolve')) {
                return true;
            }
        }

        return is_dir('/run/systemd/resolve');
    }
}
