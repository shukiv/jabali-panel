<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Support\Formatter;
use Filament\Widgets\ChartWidget;
use Illuminate\Contracts\Support\Htmlable;

class ServerMetricsChart extends ChartWidget
{
    protected static ?int $sort = 1;

    protected int|string|array $columnSpan = 'full';

    protected ?string $pollingInterval = '5s';

    protected ?string $maxHeight = '220px';

    protected function getType(): string
    {
        return 'bar';
    }

    public function getHeading(): string|Htmlable|null
    {
        return null;
    }

    public function getDescription(): string|Htmlable|null
    {
        $net = $this->getNetworkData();

        return '↑ '.$net['tx_speed'].' ('.$net['total_tx'].' total)  —  ↓ '.$net['rx_speed'].' ('.$net['total_rx'].' total)';
    }

    protected function getData(): array
    {
        $cpu = $this->getCpuData();
        $mem = $this->getMemoryData();
        $disks = $this->getDiskPartitions();

        $texts = [];
        $values = [];
        $colors = [];

        // CPU
        $texts[] = __('CPU').' '.$cpu['usage'].'%';
        $values[] = $cpu['usage'];
        $colors[] = $this->barColor($cpu['usage']);

        $texts[] = __('IO Wait').' '.$cpu['iowait'].'%';
        $values[] = $cpu['iowait'];
        $colors[] = $this->barColor($cpu['iowait']);

        // Memory
        $texts[] = __('RAM').' '.$mem['usage'].'% ('.$mem['used_gb'].'/'.$mem['total_gb'].' GB)';
        $values[] = $mem['usage'];
        $colors[] = $this->barColor($mem['usage']);

        if ($mem['has_swap']) {
            $texts[] = __('Swap').' '.$mem['swap_usage'].'% ('.$mem['swap_used_gb'].'/'.$mem['swap_total_gb'].' GB)';
            $values[] = $mem['swap_usage'];
            $colors[] = $this->barColor($mem['swap_usage']);
        }

        // Disk
        foreach ($disks as $p) {
            $texts[] = __('Disk').' '.$p['mount'].' '.$p['usage'].'% ('.$p['used_human'].'/'.$p['total_human'].')';
            $values[] = $p['usage'];
            $colors[] = $this->barColor($p['usage']);
        }

        $remaining = array_map(fn ($v) => max(0, 100 - $v), $values);

        return [
            'datasets' => [
                [
                    'data' => $values,
                    'backgroundColor' => $colors,
                    'borderWidth' => 0,
                    'borderSkipped' => false,
                    'barThickness' => 28,
                    'barTexts' => $texts,
                ],
                [
                    'data' => $remaining,
                    'backgroundColor' => 'rgba(128,128,128,0.12)',
                    'borderWidth' => 0,
                    'borderSkipped' => false,
                    'barThickness' => 28,
                ],
            ],
            'labels' => array_fill(0, count($values), ''),
        ];
    }

    protected function getOptions(): array
    {
        return [
            'indexAxis' => 'y',
            'scales' => [
                'x' => ['stacked' => true, 'display' => false, 'max' => 100],
                'y' => ['stacked' => true, 'display' => false],
            ],
            'plugins' => [
                'legend' => ['display' => false],
                'tooltip' => ['enabled' => false],
            ],
            'borderWidth' => 0,
            'maintainAspectRatio' => false,
        ];
    }

    // --- CPU ---

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

    // --- Memory ---

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

    // --- Disk ---

    private function getDiskPartitions(): array
    {
        $partitions = [];
        $seen = [];

        $mounts = @file('/proc/mounts', FILE_IGNORE_NEW_LINES) ?: [];
        foreach ($mounts as $line) {
            $parts = preg_split('/\s+/', $line);
            $device = $parts[0] ?? '';
            $mount = $parts[1] ?? '';
            $fstype = $parts[2] ?? '';

            if (! str_starts_with($device, '/dev/') || str_starts_with($device, '/dev/loop')) {
                continue;
            }
            if (in_array($fstype, ['squashfs', 'tmpfs', 'devtmpfs', 'overlay'], true)) {
                continue;
            }
            if (str_starts_with($mount, '/snap/') || str_starts_with($mount, '/var/jail/')) {
                continue;
            }

            $total = @disk_total_space($mount) ?: 0;
            if ($total <= 0) {
                continue;
            }

            if (isset($seen[$device])) {
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

    // --- Network ---

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

    private function barColor(float $value): string
    {
        if ($value >= 90) {
            return 'rgb(239,68,68)';
        }
        if ($value >= 50) {
            return 'rgb(245,158,11)';
        }

        return 'rgb(34,197,94)';
    }
}
