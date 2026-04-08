<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use Filament\Widgets\ChartWidget;

class CpuChartWidget extends ChartWidget
{
    protected static ?int $sort = 1;

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
        $current = $this->readCpuCounters();
        $usage = 0.0;
        $iowait = 0.0;

        if ($current !== null) {
            $prev = cache()->get('jabali_cpu_sample');
            cache()->put('jabali_cpu_sample', $current, 300);

            if (is_array($prev)) {
                $dTotal = $current['total'] - $prev['total'];
                if ($dTotal > 0) {
                    $dIdle = $current['idle'] - $prev['idle'];
                    $dIowait = $current['iowait'] - $prev['iowait'];
                    $usage = round((($dTotal - $dIdle - $dIowait) / $dTotal) * 100, 1);
                    $iowait = round(($dIowait / $dTotal) * 100, 1);
                }
            }
        }

        return [
            'datasets' => [
                [
                    'data' => [$usage, $iowait],
                    'backgroundColor' => [$this->barColor($usage), $this->barColor($iowait)],
                    'borderWidth' => 0,
                    'borderSkipped' => false,
                    'barThickness' => 28,
                    'barTexts' => [
                        __('CPU').' '.$usage.'%',
                        __('IO Wait').' '.$iowait.'%',
                    ],
                ],
                [
                    'data' => [max(0, 100 - $usage), max(0, 100 - $iowait)],
                    'backgroundColor' => 'rgba(128,128,128,0.12)',
                    'borderWidth' => 0,
                    'borderSkipped' => false,
                    'barThickness' => 28,
                ],
            ],
            'labels' => ['', ''],
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
