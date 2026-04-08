<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Support\Formatter;
use Filament\Widgets\ChartWidget;

class DiskChart extends ChartWidget
{
    protected ?string $heading = 'Disk';

    protected static ?int $sort = 3;

    protected int|string|array $columnSpan = 1;

    protected ?string $maxHeight = '120px';

    protected ?string $pollingInterval = '3s';

    protected function getData(): array
    {
        $total = @disk_total_space('/') ?: 0;
        $free = @disk_free_space('/') ?: 0;
        $used = max(0, $total - $free);
        $usage = $total > 0 ? round(($used / $total) * 100, 1) : 0;

        return [
            'datasets' => [
                [
                    'label' => 'Used',
                    'data' => [max($usage, 1)],
                    'backgroundColor' => [$this->barColor($usage)],
                    'borderWidth' => 0,
                    'borderRadius' => 4,
                    'borderSkipped' => false,
                    'barPercentage' => 0.6,
                ],
                [
                    'label' => 'Free',
                    'data' => [max(0, 100 - max($usage, 1))],
                    'backgroundColor' => 'rgba(128,128,128,0.1)',
                    'borderWidth' => 0,
                    'borderRadius' => 4,
                    'borderSkipped' => false,
                    'barPercentage' => 0.6,
                ],
            ],
            'labels' => ["/ {$usage}%  ".Formatter::bytes($used).'/'.Formatter::bytes($total)],
        ];
    }

    protected function getType(): string
    {
        return 'bar';
    }

    protected function getOptions(): array
    {
        return [
            'indexAxis' => 'y',
            'scales' => [
                'x' => ['stacked' => true, 'display' => false, 'max' => 100],
                'y' => [
                    'stacked' => true,
                    'grid' => ['display' => false],
                    'border' => ['display' => false],
                    'ticks' => ['font' => ['weight' => 'bold']],
                ],
            ],
            'plugins' => [
                'legend' => ['display' => false],
            ],
        ];
    }

    private function barColor(float $pct): string
    {
        if ($pct >= 90) {
            return 'rgb(239, 68, 68)';
        }
        if ($pct >= 50) {
            return 'rgb(245, 158, 11)';
        }

        return 'rgb(34, 197, 94)';
    }
}
