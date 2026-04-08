<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Support\Formatter;
use Filament\Widgets\Widget;

class ServerMetricsWidget extends Widget
{
    protected static ?int $sort = 1;

    protected int|string|array $columnSpan = 'full';

    protected string $view = 'filament.admin.widgets.server-metrics';

    public function getMetrics(): array
    {
        return [
            'cpu' => $this->getCpuData(),
            'memory' => $this->getMemoryData(),
            'disk' => $this->getDiskData(),
            'network' => $this->getNetworkData(),
        ];
    }

    private function getCpuData(): array
    {
        $current = $this->readCpuCounters();
        if ($current === null) {
            return ['usage' => 0, 'iowait' => 0];
        }

        $prev = cache()->get('jabali_cpu_sample');
        cache()->put('jabali_cpu_sample', $current, 300);

        if (! is_array($prev)) {
            return ['usage' => 0, 'iowait' => 0];
        }

        $dTotal = $current['total'] - $prev['total'];
        if ($dTotal <= 0) {
            return ['usage' => 0, 'iowait' => 0];
        }

        $dIdle = $current['idle'] - $prev['idle'];
        $dIowait = $current['iowait'] - $prev['iowait'];

        return [
            'usage' => round((($dTotal - $dIdle - $dIowait) / $dTotal) * 100, 1),
            'iowait' => round(($dIowait / $dTotal) * 100, 1),
        ];
    }

    private function readCpuCounters(): ?array
    {
        $stat = @file_get_contents('/proc/stat');
        if ($stat === false) {
            return null;
        }

        foreach (explode("\n", $stat) as $line) {
            if (str_starts_with($line, 'cpu ')) {
                $parts = preg_split('/\s+/', trim($line));
                if (count($parts) >= 8) {
                    $user = (float) $parts[1];
                    $nice = (float) $parts[2];
                    $system = (float) $parts[3];
                    $idle = (float) $parts[4];
                    $iowait = (float) $parts[5];
                    $irq = (float) $parts[6];
                    $softirq = (float) $parts[7];
                    $steal = (float) ($parts[8] ?? 0);

                    return [
                        'idle' => $idle,
                        'iowait' => $iowait,
                        'total' => $user + $nice + $system + $idle + $iowait + $irq + $softirq + $steal,
                    ];
                }
            }
        }

        return null;
    }

    private function getMemoryData(): array
    {
        $memInfo = [];
        if (is_readable('/proc/meminfo')) {
            foreach (file('/proc/meminfo', FILE_IGNORE_NEW_LINES) as $line) {
                if (preg_match('/^(\w+):\s+(\d+)/', $line, $m)) {
                    $memInfo[$m[1]] = (int) $m[2];
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

    private function getDiskData(): array
    {
        $partitions = [];
        $seen = [];

        $mounts = @file('/proc/mounts', FILE_IGNORE_NEW_LINES) ?: [];
        foreach ($mounts as $line) {
            $parts = preg_split('/\s+/', $line);
            $device = $parts[0] ?? '';
            $mount = $parts[1] ?? '';
            $fstype = $parts[2] ?? '';

            // Skip virtual filesystems
            if (in_array($fstype, ['proc', 'sysfs', 'devtmpfs', 'devpts', 'cgroup', 'cgroup2', 'securityfs', 'pstore', 'debugfs', 'tracefs', 'fusectl', 'configfs', 'mqueue', 'hugetlbfs', 'binfmt_misc', 'autofs', 'overlay', 'nsfs', 'fuse.lxcfs'], true)) {
                continue;
            }

            // Skip snap, docker, and system mounts
            if (str_starts_with($mount, '/snap/') || str_starts_with($mount, '/sys/') || str_starts_with($mount, '/proc/') || str_starts_with($mount, '/dev/')) {
                continue;
            }

            $total = @disk_total_space($mount) ?: 0;
            if ($total <= 0) {
                continue;
            }

            // Skip duplicate devices (same device mounted multiple times)
            if (isset($seen[$device]) && $device !== 'tmpfs') {
                continue;
            }
            $seen[$device] = true;

            $free = @disk_free_space($mount) ?: 0;
            $used = max(0, $total - $free);

            $partitions[] = [
                'mount' => $mount,
                'usage' => round(($used / $total) * 100, 1),
                'used_human' => Formatter::bytes($used),
                'total_human' => Formatter::bytes($total),
            ];
        }

        // Ensure at least root partition
        if (empty($partitions)) {
            $total = @disk_total_space('/') ?: 0;
            $free = @disk_free_space('/') ?: 0;
            $used = max(0, $total - $free);
            $partitions[] = [
                'mount' => '/',
                'usage' => $total > 0 ? round(($used / $total) * 100, 1) : 0,
                'used_human' => Formatter::bytes($used),
                'total_human' => Formatter::bytes($total),
            ];
        }

        return $partitions;
    }

    private function getNetworkData(): array
    {
        $totalRx = 0;
        $totalTx = 0;

        if (is_readable('/proc/net/dev')) {
            foreach (file('/proc/net/dev', FILE_IGNORE_NEW_LINES) as $line) {
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
        $prev = cache()->get('jabali_net_sample');

        if (is_array($prev) && isset($prev['time'], $prev['rx'], $prev['tx'])) {
            $elapsed = $now - (float) $prev['time'];
            if ($elapsed > 0.5 && $elapsed < 120) {
                $rxSpeed = max(0, ($totalRx - (int) $prev['rx'])) / $elapsed;
                $txSpeed = max(0, ($totalTx - (int) $prev['tx'])) / $elapsed;
            }
        }

        cache()->put('jabali_net_sample', ['time' => $now, 'rx' => $totalRx, 'tx' => $totalTx], 300);

        return [
            'tx_speed' => Formatter::bytes($txSpeed).'/s',
            'rx_speed' => Formatter::bytes($rxSpeed).'/s',
            'total_tx' => Formatter::bytes($totalTx),
            'total_rx' => Formatter::bytes($totalRx),
        ];
    }
}
