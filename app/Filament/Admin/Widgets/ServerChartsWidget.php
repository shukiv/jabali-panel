<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use Filament\Widgets\Widget;

class ServerChartsWidget extends Widget
{
    protected string $view = 'filament.admin.widgets.server-charts';

    protected int|string|array $columnSpan = 'full';

    public function getData(): array
    {
        return [
            'cpu' => $this->getCpuUsage(),
            'memory' => $this->readMeminfo(),
            'disk' => $this->readDiskUsage(),
            'network' => $this->readNetworkCounters(),
        ];
    }

    private function getCpuUsage(): array
    {
        $info = $this->getCpuInfo();

        $stat = @file_get_contents('/proc/stat');
        $usage = 0.0;
        $iowait = 0.0;

        if ($stat !== false) {
            $lines = explode("\n", $stat);
            foreach ($lines as $line) {
                if (str_starts_with($line, 'cpu ')) {
                    $parts = preg_split('/\s+/', trim($line));
                    if (count($parts) >= 8) {
                        $user = (float) $parts[1];
                        $nice = (float) $parts[2];
                        $system = (float) $parts[3];
                        $idle = (float) $parts[4];
                        $iowaitVal = (float) $parts[5];
                        $irq = (float) $parts[6];
                        $softirq = (float) $parts[7];
                        $steal = (float) ($parts[8] ?? 0);

                        $total = $user + $nice + $system + $idle + $iowaitVal + $irq + $softirq + $steal;
                        if ($total > 0) {
                            $usage = round((($total - $idle - $iowaitVal) / $total) * 100, 1);
                            $iowait = round(($iowaitVal / $total) * 100, 1);
                        }
                    }
                    break;
                }
            }
        }

        return [
            'usage' => $usage,
            'iowait' => $iowait,
            'cores' => $info['cores'],
            'model' => $info['model'],
        ];
    }

    private function getCpuInfo(): array
    {
        $cores = 0;
        $model = 'Unknown';

        if (is_readable('/proc/cpuinfo')) {
            $lines = file('/proc/cpuinfo', FILE_IGNORE_NEW_LINES);
            foreach ($lines as $line) {
                if (str_starts_with($line, 'processor')) {
                    $cores++;
                }
                if (str_starts_with($line, 'model name')) {
                    $model = trim(explode(':', $line, 2)[1] ?? $model);
                }
            }
        }

        return [
            'cores' => max(1, $cores),
            'model' => $model,
        ];
    }

    private function readMeminfo(): array
    {
        $memInfo = [];
        if (is_readable('/proc/meminfo')) {
            $lines = file('/proc/meminfo', FILE_IGNORE_NEW_LINES);
            foreach ($lines as $line) {
                if (preg_match('/^(\w+):\s+(\d+)/', $line, $matches)) {
                    $memInfo[$matches[1]] = (int) $matches[2];
                }
            }
        }

        $totalKb = $memInfo['MemTotal'] ?? 0;
        $availableKb = $memInfo['MemAvailable'] ?? ($memInfo['MemFree'] ?? 0);
        $swapTotalKb = $memInfo['SwapTotal'] ?? 0;
        $swapFreeKb = $memInfo['SwapFree'] ?? 0;

        $usedKb = max(0, $totalKb - $availableKb);
        $swapUsedKb = max(0, $swapTotalKb - $swapFreeKb);

        return [
            'usage' => $totalKb > 0 ? round(($usedKb / $totalKb) * 100, 1) : 0,
            'used_gb' => round($usedKb / 1048576, 1),
            'total_gb' => round($totalKb / 1048576, 1),
            'swap_usage' => $swapTotalKb > 0 ? round(($swapUsedKb / $swapTotalKb) * 100, 1) : 0,
            'swap_used_gb' => round($swapUsedKb / 1048576, 1),
            'swap_total_gb' => round($swapTotalKb / 1048576, 1),
            'has_swap' => $swapTotalKb > 0,
        ];
    }

    private function readDiskUsage(): array
    {
        $partitions = [];
        $seen = [];

        $mounts = @file('/proc/mounts', FILE_IGNORE_NEW_LINES) ?: [];
        foreach ($mounts as $line) {
            $parts = preg_split('/\s+/', $line);
            $device = $parts[0] ?? '';
            $mount = stripcslashes($parts[1] ?? '');
            $fstype = $parts[2] ?? '';

            if (! str_starts_with($device, '/dev/') || isset($seen[$device])) {
                continue;
            }
            if (in_array($fstype, ['devtmpfs', 'tmpfs', 'squashfs', 'overlay'], true)) {
                continue;
            }

            $total = @disk_total_space($mount) ?: 0;
            if ($total <= 0) {
                continue;
            }

            $free = @disk_free_space($mount) ?: 0;
            $used = max(0, $total - $free);
            $seen[$device] = true;

            $partitions[] = [
                'mount' => $mount,
                'used' => $used,
                'total' => $total,
                'usage' => round(($used / $total) * 100, 1),
                'used_human' => $this->formatBytes($used),
                'total_human' => $this->formatBytes($total),
            ];
        }

        if (empty($partitions)) {
            $total = @disk_total_space('/') ?: 0;
            $free = @disk_free_space('/') ?: 0;
            $used = max(0, $total - $free);
            if ($total > 0) {
                $partitions[] = [
                    'mount' => '/',
                    'used' => $used,
                    'total' => $total,
                    'usage' => round(($used / $total) * 100, 1),
                    'used_human' => $this->formatBytes($used),
                    'total_human' => $this->formatBytes($total),
                ];
            }
        }

        return $partitions;
    }

    private function readNetworkCounters(): array
    {
        $totalRx = 0;
        $totalTx = 0;

        if (is_readable('/proc/net/dev')) {
            $lines = file('/proc/net/dev', FILE_IGNORE_NEW_LINES);
            foreach ($lines as $line) {
                if (str_contains($line, ':')) {
                    $parts = preg_split('/[\s:]+/', trim($line));
                    $iface = $parts[0] ?? '';
                    if ($iface === 'lo' || $iface === '') {
                        continue;
                    }
                    $totalRx += (int) ($parts[1] ?? 0);
                    $totalTx += (int) ($parts[9] ?? 0);
                }
            }
        }

        $rxSpeed = 0.0;
        $txSpeed = 0.0;
        $now = microtime(true);
        $cacheKey = 'jabali_net_sample';

        $prev = cache()->get($cacheKey);

        if (is_array($prev) && isset($prev['time'], $prev['rx'], $prev['tx'])) {
            $elapsed = $now - (float) $prev['time'];
            if ($elapsed > 0.5 && $elapsed < 120) {
                $rxSpeed = max(0, ($totalRx - (int) $prev['rx'])) / $elapsed;
                $txSpeed = max(0, ($totalTx - (int) $prev['tx'])) / $elapsed;
            }
        }

        cache()->put($cacheKey, [
            'time' => $now,
            'rx' => $totalRx,
            'tx' => $totalTx,
        ], 300);

        return [
            'total_rx' => $totalRx,
            'total_tx' => $totalTx,
            'total_rx_human' => $this->formatBytes($totalRx),
            'total_tx_human' => $this->formatBytes($totalTx),
            'rx_speed' => $this->formatBytes($rxSpeed).'/s',
            'tx_speed' => $this->formatBytes($txSpeed).'/s',
        ];
    }

    private function formatBytes(int|float $bytes): string
    {
        $units = ['B', 'KB', 'MB', 'GB', 'TB'];
        $size = max(0, (float) $bytes);
        $unit = 0;
        while ($size >= 1024 && $unit < count($units) - 1) {
            $size /= 1024;
            $unit++;
        }

        return round($size, 1).' '.$units[$unit];
    }
}
