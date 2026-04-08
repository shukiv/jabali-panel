<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use Filament\Widgets\ChartWidget;

class MemoryChart extends ChartWidget
{
    protected ?string $heading = 'Memory';

    protected static ?int $sort = 2;

    protected int|string|array $columnSpan = 1;

    protected ?string $maxHeight = '120px';

    protected ?string $pollingInterval = '5s';

    protected function getData(): array
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

        $memUsage = $totalKb > 0 ? round(($usedKb / $totalKb) * 100, 1) : 0;
        $swapUsage = $swapTotalKb > 0 ? round(($swapUsedKb / $swapTotalKb) * 100, 1) : 0;
        $hasSwap = $swapTotalKb > 0;

        $usedGb = round($usedKb / 1048576, 1);
        $totalGb = round($totalKb / 1048576, 1);
        $swapUsedGb = round($swapUsedKb / 1048576, 1);
        $swapTotalGb = round($swapTotalKb / 1048576, 1);

        $labels = ["Memory {$memUsage}%  {$usedGb}/{$totalGb} GB"];
        $values = [$memUsage];
        $remainder = [max(0, 100 - $memUsage)];
        $colors = [$this->barColor($memUsage)];

        if ($hasSwap) {
            $labels[] = "Swap {$swapUsage}%  {$swapUsedGb}/{$swapTotalGb} GB";
            $values[] = $swapUsage;
            $remainder[] = max(0, 100 - $swapUsage);
            $colors[] = $this->barColor($swapUsage);
        }

        return [
            'datasets' => [
                [
                    'label' => 'Used',
                    'data' => $values,
                    'backgroundColor' => $colors,
                    'borderRadius' => 4,
                    'borderSkipped' => false,
                    'barPercentage' => 0.6,
                ],
                [
                    'label' => 'Free',
                    'data' => $remainder,
                    'backgroundColor' => 'rgba(128,128,128,0.1)',
                    'borderRadius' => 4,
                    'borderSkipped' => false,
                    'barPercentage' => 0.6,
                ],
            ],
            'labels' => $labels,
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
