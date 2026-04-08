<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Support\Formatter;
use Filament\Widgets\ChartWidget;

class DiskChartWidget extends ChartWidget
{
    protected static ?int $sort = 3;

    protected int|string|array $columnSpan = 1;

    protected ?string $pollingInterval = '5s';

    protected ?string $maxHeight = '90px';

    protected ?string $heading = null;

    protected function getType(): string
    {
        return 'bar';
    }

    protected function getData(): array
    {
        $partitions = $this->getDiskPartitions();

        $texts = [];
        $values = [];
        $colors = [];

        foreach ($partitions as $p) {
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
                'y' => [
                    'stacked' => true,
                    'display' => false,
                ],
            ],
            'plugins' => [
                'legend' => ['display' => false],
                'tooltip' => ['enabled' => false],
            ],
            'borderWidth' => 0,
            'maintainAspectRatio' => false,
        ];
    }

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
