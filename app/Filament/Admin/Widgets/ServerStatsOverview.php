<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Services\Agent\AgentClient;
use Filament\Widgets\StatsOverviewWidget as BaseWidget;
use Filament\Widgets\StatsOverviewWidget\Stat;

class ServerStatsOverview extends BaseWidget
{
    protected ?string $pollingInterval = '10s';

    protected function getStats(): array
    {
        $agent = new AgentClient();

        try {
            $overview = $agent->metricsOverview();
            $cpu = $overview['cpu'] ?? [];
            $memory = $overview['memory'] ?? [];
            $disk = $agent->metricsDisk()['data'] ?? [];
        } catch (\Exception $e) {
            return [
                Stat::make(__('Error'), __('Failed to load metrics'))
                    ->description($e->getMessage())
                    ->color('danger'),
            ];
        }

        $cpuUsage = $cpu['usage'] ?? 0;
        $memUsage = $memory['usage_percent'] ?? 0;
        $loadAvg = $overview['load']['1min'] ?? 0;
        $uptime = $overview['uptime']['human'] ?? 'N/A';

        // Disk I/O - get first disk's I/O stats
        $io = $disk['io'] ?? [];
        $firstDisk = array_values($io)[0] ?? [];
        $readBytes = ($firstDisk['read_sectors'] ?? 0) * 512;
        $writeBytes = ($firstDisk['write_sectors'] ?? 0) * 512;
        $ioWait = $this->getIoWait();

        return [
            Stat::make(__('CPU Usage'), $cpuUsage . '%')
                ->description(($cpu['cores'] ?? 0) . ' ' . __('cores'))
                ->descriptionIcon('heroicon-m-cpu-chip')
                ->color('primary')
                ->chart($this->generateSparkline($cpuUsage)),

            Stat::make(__('Memory'), $memUsage . '%')
                ->description(number_format(($memory['used'] ?? 0) / 1024, 1) . ' / ' . number_format(($memory['total'] ?? 0) / 1024, 1) . ' GB')
                ->descriptionIcon('heroicon-m-server')
                ->color('info')
                ->chart($this->generateSparkline($memUsage)),

            Stat::make(__('Load Average'), (string) $loadAvg)
                ->description(__('Uptime') . ': ' . $uptime)
                ->descriptionIcon('heroicon-m-clock')
                ->color('warning'),

            Stat::make(__('Disk I/O'), $ioWait . '% ' . __('wait'))
                ->description(__('R') . ': ' . $this->formatBytes($readBytes) . ' | ' . __('W') . ': ' . $this->formatBytes($writeBytes))
                ->descriptionIcon('heroicon-m-circle-stack')
                ->color($ioWait > 20 ? 'danger' : ($ioWait > 10 ? 'warning' : 'gray')),
        ];
    }

    protected function getIoWait(): float
    {
        $stat = @file_get_contents('/proc/stat');
        if (!$stat) {
            return 0;
        }

        if (preg_match('/^cpu\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)/m', $stat, $matches)) {
            $user = (int) $matches[1];
            $nice = (int) $matches[2];
            $system = (int) $matches[3];
            $idle = (int) $matches[4];
            $iowait = (int) $matches[5];

            $total = $user + $nice + $system + $idle + $iowait;
            if ($total > 0) {
                return round(($iowait / $total) * 100, 1);
            }
        }

        return 0;
    }

    protected function formatBytes(int $bytes): string
    {
        $units = ['B', 'KB', 'MB', 'GB', 'TB'];
        $i = 0;
        while ($bytes >= 1024 && $i < count($units) - 1) {
            $bytes /= 1024;
            $i++;
        }
        return round($bytes, 1) . ' ' . $units[$i];
    }

    protected function generateSparkline(float $value): array
    {
        // Generate a simple sparkline based on current value
        $base = max(0, $value - 20);
        return [
            $base + rand(0, 10),
            $base + rand(0, 15),
            $base + rand(0, 10),
            $base + rand(0, 15),
            $base + rand(0, 10),
            $value,
        ];
    }
}
