<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Services\SysstatMetrics;
use Filament\Widgets\Widget;

class ServerChartsWidget extends Widget
{
    protected string $view = 'filament.admin.widgets.server-charts';

    protected int|string|array $columnSpan = 'full';

    public int $refreshKey = 0;

    public string $range = '5m';

    /**
     * @var array<string, mixed>
     */
    public array $diskSnapshot = [];

    public function mount(): void
    {
        $this->diskSnapshot = $this->fetchDiskSnapshot();
    }

    public function refreshData(): void
    {
        $this->refreshKey++;
        $data = $this->getData();
        $config = $this->rangeConfig($this->range);
        $sysstat = new SysstatMetrics;
        $labelTime = now($sysstat->timezoneName());
        if ($config['interval_seconds'] < 60) {
            $second = intdiv($labelTime->second, $config['interval_seconds']) * $config['interval_seconds'];
            $labelTime = $labelTime->setTime($labelTime->hour, $labelTime->minute, $second);
        } else {
            $labelTime = $labelTime->second(0);
            if ($config['interval_seconds'] >= 3600) {
                $labelTime = $labelTime->minute(0);
            }
            if ($config['interval_seconds'] >= 86400) {
                $labelTime = $labelTime->hour(0)->minute(0);
            }
        }
        $label = $labelTime->format($config['label_format']);

        $this->dispatch('server-charts-updated', [
            'cpu' => $data['cpu']['usage'] ?? 0,
            'load' => $data['load']['1min'] ?? 0,
            'iowait' => $data['cpu']['iowait'] ?? 0,
            'memory' => $data['memory']['usage'] ?? 0,
            'swap' => $data['memory']['swap_usage'] ?? 0,
            'label' => $label,
            'history_points' => $config['points'],
            'label_format' => $config['label_format'],
            'interval_seconds' => $config['interval_seconds'],
        ]);
    }

    public function getData(): array
    {
        $sysstat = new SysstatMetrics;
        $latest = $sysstat->latest();

        if ($latest === null) {
            $fallback = $this->getLocalMetrics();
            if ($fallback !== null) {
                if (empty($this->diskSnapshot)) {
                    $this->diskSnapshot = $fallback['disk'] ?? [];
                }

                return [
                    'cpu' => [
                        'usage' => round($fallback['cpu']['usage'] ?? 0, 1),
                        'iowait' => round($fallback['cpu']['iowait'] ?? 0, 1),
                        'cores' => $fallback['cpu']['cores'] ?? 0,
                        'model' => $fallback['cpu']['model'] ?? 'Local',
                    ],
                    'memory' => $fallback['memory'],
                    'disk' => $fallback['disk'],
                    'load' => $fallback['load'],
                    'uptime' => $fallback['uptime'] ?? 'N/A',
                    'source' => 'local',
                    'warning' => 'sysstat data unavailable',
                ];
            }
        }

        try {
            $disk = $this->diskSnapshot;
            if (empty($disk)) {
                $disk = $this->fetchDiskSnapshot();
                $this->diskSnapshot = $disk;
            }

            $cpu = $this->getLocalCpuInfo();
            $memory = $this->readMeminfo();

            if ($latest !== null) {
                $memory['usage'] = round((float) $latest['memory'], 1);
                $memory['swap_usage'] = round((float) $latest['swap'], 1);
            }

            return [
                'cpu' => [
                    'usage' => 0,
                    'iowait' => round((float) ($latest['iowait'] ?? 0), 1),
                    'cores' => $cpu['cores'] ?? 0,
                    'model' => $cpu['model'] ?? 'Unknown',
                ],
                'memory' => [
                    'usage' => $memory['usage'] ?? 0,
                    'used' => $memory['used'] ?? 0,
                    'total' => $memory['total'] ?? 0,
                    'free' => $memory['free'] ?? 0,
                    'cached' => $memory['cached'] ?? 0,
                    'swap_used' => $memory['swap_used'] ?? 0,
                    'swap_total' => $memory['swap_total'] ?? 0,
                    'swap_usage' => $memory['swap_usage'] ?? 0,
                ],
                'disk' => [
                    'partitions' => $disk['partitions'] ?? [],
                ],
                'load' => [
                    '1min' => $latest['load1'] ?? 0,
                    '5min' => $latest['load5'] ?? 0,
                    '15min' => $latest['load15'] ?? 0,
                ],
                'uptime' => $this->getLocalUptime(),
                'source' => $latest !== null ? 'sysstat' : 'local',
            ];
        } catch (\Exception $e) {
            $fallback = $this->getLocalMetrics();
            if ($fallback !== null) {
                if (empty($this->diskSnapshot)) {
                    $this->diskSnapshot = $fallback['disk'] ?? [];
                }

                return [
                    'cpu' => [
                        'usage' => round($fallback['cpu']['usage'] ?? 0, 1),
                        'iowait' => round($fallback['cpu']['iowait'] ?? 0, 1),
                        'cores' => $fallback['cpu']['cores'] ?? 0,
                        'model' => $fallback['cpu']['model'] ?? 'Local',
                    ],
                    'memory' => $fallback['memory'],
                    'disk' => $fallback['disk'],
                    'load' => $fallback['load'],
                    'uptime' => $fallback['uptime'] ?? 'N/A',
                    'source' => 'local',
                    'warning' => $e->getMessage(),
                ];
            }

            return [
                'error' => $e->getMessage(),
                'cpu' => ['usage' => 0, 'iowait' => 0, 'cores' => 0, 'model' => 'Error'],
                'memory' => ['usage' => 0, 'used' => 0, 'total' => 0, 'free' => 0, 'cached' => 0],
                'disk' => $this->diskSnapshot,
                'load' => [],
                'uptime' => 'N/A',
                'source' => 'error',
            ];
        }
    }

    public function setRange(string $range): void
    {
        $this->range = $this->normalizeRange($range);

        $config = $this->rangeConfig($this->range);
        $data = $this->getData();
        $history = $this->getHistory(
            (float) ($data['load']['1min'] ?? 0),
            (float) ($data['cpu']['iowait'] ?? 0),
            (float) ($data['memory']['usage'] ?? 0),
            (float) ($data['memory']['swap_usage'] ?? 0),
            $data['disk']['partitions'] ?? [],
        );
        $this->dispatch('server-charts-range-changed', [
            'history' => $history,
            'history_points' => $config['points'],
            'label_format' => $config['label_format'],
            'interval_seconds' => $config['interval_seconds'],
            'range' => $this->range,
        ]);
    }

    public function getHistory(
        float $load = 0.0,
        float $iowait = 0.0,
        float $memory = 0.0,
        float $swap = 0.0,
        array $diskPartitions = []
    ): array {
        $config = $this->rangeConfig($this->range);
        $sysstat = new SysstatMetrics;
        $history = $sysstat->history($config['points'], $config['interval_seconds'], $config['label_format']);

        if (! empty($history)) {
            $history['disk'] = $this->seedDiskHistory($config['points'], $diskPartitions);

            return $history;
        }

        return $this->seedHistory(
            $config['points'],
            $config['label_format'],
            $config['interval_seconds'],
            $load,
            $iowait,
            $memory,
            $swap,
            $diskPartitions,
        );
    }

    private function rangeConfig(string $range): array
    {
        return match ($range) {
            '5m' => ['minutes' => 5, 'points' => 30, 'resolution' => '10s', 'label_format' => 'H:i:s', 'interval_seconds' => 10],
            '30m' => ['minutes' => 30, 'points' => 180, 'resolution' => '10s', 'label_format' => 'H:i:s', 'interval_seconds' => 10],
            'day' => ['minutes' => 1440, 'points' => 24, 'resolution' => '1h', 'label_format' => 'H:00', 'interval_seconds' => 3600],
            'week' => ['minutes' => 10080, 'points' => 28, 'resolution' => '6h', 'label_format' => 'M d H:00', 'interval_seconds' => 21600],
            'month' => ['minutes' => 43200, 'points' => 30, 'resolution' => '1d', 'label_format' => 'M d', 'interval_seconds' => 86400],
            default => ['minutes' => 30, 'points' => 30, 'resolution' => '1m', 'label_format' => 'H:i', 'interval_seconds' => 60],
        };
    }

    private function normalizeRange(string $range): string
    {
        return in_array($range, ['5m', '30m', 'day', 'week', 'month'], true) ? $range : '30m';
    }

    private function seedHistory(
        int $points,
        string $labelFormat,
        int $intervalSeconds,
        float $load,
        float $iowait,
        float $memory,
        float $swap,
        array $diskPartitions
    ): array {
        $labels = [];
        $loadSeries = [];
        $ioWaitSeries = [];
        $memorySeries = [];
        $swapSeries = [];
        $diskSeries = [];
        $diskSeeds = [];
        foreach ($diskPartitions as $partition) {
            $mount = $partition['mount'] ?? null;
            $usedGb = $this->bytesToGb((int) ($partition['used'] ?? 0));
            if ($mount !== null) {
                $diskSeeds[$mount] = $usedGb;
            }
        }
        foreach ($diskSeeds as $mount => $value) {
            $diskSeries[$mount] = [];
        }
        $now = now();

        for ($i = $points - 1; $i >= 0; $i--) {
            $labels[] = $now->copy()->subSeconds($i * $intervalSeconds)->format($labelFormat);
            $loadSeries[] = round($load, 3);
            $ioWaitSeries[] = round($iowait, 2);
            $memorySeries[] = round($memory, 1);
            $swapSeries[] = round($swap, 1);
            foreach ($diskSeeds as $mount => $value) {
                $diskSeries[$mount][] = round($value, 2);
            }
        }

        return [
            'labels' => $labels,
            'load' => $loadSeries,
            'iowait' => $ioWaitSeries,
            'memory' => $memorySeries,
            'swap' => $swapSeries,
            'disk' => $diskSeries,
        ];
    }

    private function getLocalMetrics(): ?array
    {
        $load = sys_getloadavg();
        if (! is_array($load) || count($load) < 3) {
            return null;
        }

        $loadData = [
            '1min' => round((float) $load[0], 2),
            '5min' => round((float) $load[1], 2),
            '15min' => round((float) $load[2], 2),
        ];

        $cpuInfo = $this->getLocalCpuInfo();

        $memory = $this->readMeminfo();
        $disk = $this->readDiskUsage();

        return [
            'cpu' => [
                'usage' => 0,
                'iowait' => 0,
                'cores' => $cpuInfo['cores'],
                'model' => $cpuInfo['model'],
            ],
            'memory' => $memory,
            'disk' => $disk,
            'load' => $loadData,
            'uptime' => $this->getLocalUptime(),
        ];
    }

    private function fetchDiskSnapshot(): array
    {
        return $this->readDiskUsage();
    }

    /**
     * @return array{cores: int, model: string}
     */
    private function getLocalCpuInfo(): array
    {
        $cpuCores = 0;
        $cpuModel = 'Unknown';

        if (is_readable('/proc/cpuinfo')) {
            $lines = file('/proc/cpuinfo', FILE_IGNORE_NEW_LINES);
            foreach ($lines as $line) {
                if (str_starts_with($line, 'processor')) {
                    $cpuCores++;
                }
                if (str_starts_with($line, 'model name')) {
                    $cpuModel = trim(explode(':', $line, 2)[1] ?? $cpuModel);
                }
            }
        }

        return [
            'cores' => $cpuCores > 0 ? $cpuCores : 1,
            'model' => $cpuModel,
        ];
    }

    private function getLocalUptime(): string
    {
        if (! is_readable('/proc/uptime')) {
            return 'N/A';
        }

        $contents = trim((string) file_get_contents('/proc/uptime'));
        $parts = preg_split('/\s+/', $contents);
        $seconds = isset($parts[0]) ? (int) floor((float) $parts[0]) : 0;

        if ($seconds <= 0) {
            return 'N/A';
        }

        $days = intdiv($seconds, 86400);
        $hours = intdiv($seconds % 86400, 3600);
        $minutes = intdiv($seconds % 3600, 60);

        if ($days > 0) {
            return sprintf('%dd %dh %dm', $days, $hours, $minutes);
        }

        if ($hours > 0) {
            return sprintf('%dh %dm', $hours, $minutes);
        }

        return sprintf('%dm', $minutes);
    }

    /**
     * @param  array<int, array<string, mixed>>  $diskPartitions
     * @return array<string, array<int, float>>
     */
    private function seedDiskHistory(int $points, array $diskPartitions): array
    {
        $diskSeries = [];
        $diskSeeds = [];
        foreach ($diskPartitions as $partition) {
            $mount = $partition['mount'] ?? null;
            $usedGb = $this->bytesToGb((int) ($partition['used'] ?? 0));
            if ($mount !== null) {
                $diskSeeds[$mount] = $usedGb;
            }
        }
        foreach ($diskSeeds as $mount => $value) {
            $diskSeries[$mount] = array_fill(0, $points, round($value, 2));
        }

        return $diskSeries;
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
        $freeKb = $memInfo['MemFree'] ?? 0;
        $availableKb = $memInfo['MemAvailable'] ?? $freeKb;
        $buffersKb = $memInfo['Buffers'] ?? 0;
        $cachedKb = $memInfo['Cached'] ?? 0;
        $swapTotalKb = $memInfo['SwapTotal'] ?? 0;
        $swapFreeKb = $memInfo['SwapFree'] ?? 0;
        $swapUsedKb = max(0, $swapTotalKb - $swapFreeKb);

        $usedKb = max(0, $totalKb - $availableKb);

        return [
            'usage' => $totalKb > 0 ? round(($usedKb / $totalKb) * 100, 1) : 0,
            'used' => round($usedKb / 1024, 0),
            'total' => round($totalKb / 1024, 0),
            'free' => round($freeKb / 1024, 0),
            'cached' => round($cachedKb / 1024, 0),
            'swap_used' => round($swapUsedKb / 1024, 0),
            'swap_total' => round($swapTotalKb / 1024, 0),
            'swap_usage' => $swapTotalKb > 0 ? round(($swapUsedKb / $swapTotalKb) * 100, 1) : 0,
        ];
    }

    private function readDiskUsage(): array
    {
        $total = @disk_total_space('/') ?: 0;
        $free = @disk_free_space('/') ?: 0;
        $used = max(0, $total - $free);
        $usagePercent = $total > 0 ? round(($used / $total) * 100, 1) : 0;

        return [
            'partitions' => [
                [
                    'filesystem' => '/',
                    'mount' => '/',
                    'used' => $used,
                    'total' => $total,
                    'usage_percent' => $usagePercent,
                    'used_human' => $this->formatBytes($used),
                    'total_human' => $this->formatBytes($total),
                ],
            ],
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

    private function bytesToGb(int $bytes): float
    {
        if ($bytes <= 0) {
            return 0.0;
        }

        return round($bytes / 1024 / 1024 / 1024, 2);
    }
}
