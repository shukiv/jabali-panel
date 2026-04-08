<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use Filament\Widgets\ChartWidget;

class CpuChart extends ChartWidget
{
    protected ?string $heading = 'CPU';

    protected static ?int $sort = 1;

    protected int|string|array $columnSpan = 1;

    protected ?string $maxHeight = '120px';

    protected ?string $pollingInterval = '5s';

    protected function getData(): array
    {
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
            'datasets' => [
                [
                    'label' => 'Used',
                    'data' => [$usage, $iowait],
                    'backgroundColor' => [
                        $this->barColor($usage),
                        $this->barColor($iowait),
                    ],
                    'borderRadius' => 4,
                    'borderSkipped' => false,
                    'barPercentage' => 0.6,
                ],
                [
                    'label' => 'Free',
                    'data' => [max(0, 100 - $usage), max(0, 100 - $iowait)],
                    'backgroundColor' => 'rgba(128,128,128,0.1)',
                    'borderRadius' => 4,
                    'borderSkipped' => false,
                    'barPercentage' => 0.6,
                ],
            ],
            'labels' => ['CPU '.$usage.'%', 'IO Wait '.$iowait.'%'],
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
