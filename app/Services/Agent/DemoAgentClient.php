<?php

declare(strict_types=1);

namespace App\Services\Agent;

/**
 * Fake agent client for demo mode.
 *
 * Returns plausible success responses without touching any system resources.
 * Activated when DEMO_MODE=true in .env.
 */
class DemoAgentClient extends AgentClient
{
    public function __construct()
    {
        // No socket connection needed in demo mode
    }

    public function send(string $action, array $data = []): array
    {
        // Read-only actions return realistic mock data
        return match (true) {
            str_starts_with($action, 'metrics.') => $this->mockMetrics($action),
            str_starts_with($action, 'service.') => $this->mockService($action, $data),
            str_starts_with($action, 'quota.') => $this->mockQuota($action, $data),
            str_starts_with($action, 'cgroup.') => $this->mockCgroup($action, $data),
            $action === 'wp.list' => ['success' => true, 'sites' => []],
            $action === 'php.list_versions' => ['success' => true, 'versions' => [['version' => '8.4', 'active' => true, 'default' => true]]],
            $action === 'ip.list' => ['success' => true, 'addresses' => []],
            // Write actions return success but do nothing
            default => ['success' => true, 'message' => 'Demo mode — no changes applied'],
        };
    }

    private function mockMetrics(string $action): array
    {
        return match ($action) {
            'metrics.cpu' => ['success' => true, 'usage' => 12.5, 'cores' => 2, 'io_wait' => 0.3],
            'metrics.memory' => ['success' => true, 'total_mb' => 2048, 'used_mb' => 820, 'free_mb' => 1228, 'swap_total_mb' => 512, 'swap_used_mb' => 0],
            'metrics.disk' => ['success' => true, 'partitions' => [['mount' => '/', 'total_gb' => 40, 'used_gb' => 12, 'percent' => 30]]],
            'metrics.network' => ['success' => true, 'interfaces' => []],
            'metrics.processes' => ['success' => true, 'processes' => [], 'total' => 85],
            'metrics.overview' => ['success' => true, 'hostname' => 'demo.jabali-panel.com', 'uptime' => '14 days', 'load' => [0.5, 0.3, 0.2]],
            default => ['success' => true],
        };
    }

    private function mockService(string $action, array $data): array
    {
        return match ($action) {
            'service.list' => ['success' => true, 'services' => []],
            'service.status' => ['success' => true, 'status' => 'running', 'name' => $data['name'] ?? 'unknown'],
            default => ['success' => true, 'message' => 'Demo mode'],
        };
    }

    private function mockQuota(string $action, array $data): array
    {
        return match ($action) {
            'quota.get' => ['success' => true, 'username' => $data['username'] ?? '', 'has_quota' => true, 'used_mb' => 82, 'soft_mb' => 300, 'hard_mb' => 300, 'usage_percent' => 27, 'quota_source' => 'quota'],
            'quota.status' => ['success' => true, 'enabled' => true],
            default => ['success' => true],
        };
    }

    private function mockCgroup(string $action, array $data): array
    {
        return match ($action) {
            'cgroup.check' => ['success' => true, 'available' => true, 'version' => 'v2', 'controllers' => ['cpu', 'memory', 'io', 'pids']],
            'cgroup.status' => ['success' => true, 'username' => $data['username'] ?? '', 'active' => true, 'slice' => 'jabali-user-demo.slice', 'cpu' => ['usage_seconds' => 45.2, 'quota_percent' => 100], 'memory' => ['used_mb' => 120, 'limit_mb' => 512, 'usage_percent' => 23.4], 'processes' => ['current' => 8, 'limit' => 50]],
            default => ['success' => true],
        };
    }
}
