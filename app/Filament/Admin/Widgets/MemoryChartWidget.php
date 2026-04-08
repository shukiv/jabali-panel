<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use Filament\Widgets\ChartWidget;

class MemoryChartWidget extends ChartWidget
{
    protected static ?int $sort = 2;

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
        $mem = $this->getMemoryData();

        $texts = [__('RAM').' '.$mem['usage'].'% ('.$mem['used_gb'].'/'.$mem['total_gb'].' GB)'];
        $values = [$mem['usage']];
        $colors = [$this->barColor($mem['usage'])];

        if ($mem['has_swap']) {
            $texts[] = __('Swap').' '.$mem['swap_usage'].'% ('.$mem['swap_used_gb'].'/'.$mem['swap_total_gb'].' GB)';
            $values[] = $mem['swap_usage'];
            $colors[] = $this->barColor($mem['swap_usage']);
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
