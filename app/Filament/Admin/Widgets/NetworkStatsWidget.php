<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Support\Formatter;
use Filament\Widgets\StatsOverviewWidget;
use Filament\Widgets\StatsOverviewWidget\Stat;

class NetworkStatsWidget extends StatsOverviewWidget
{
    protected static ?int $sort = 4;

    protected int|string|array $columnSpan = 1;

    protected ?string $pollingInterval = '5s';

    protected function getColumns(): int|array|null
    {
        return 1;
    }

    protected function getStats(): array
    {
        $data = $this->getNetworkData();

        return [
            Stat::make(__('Upload'), $data['tx_speed'])
                ->description(__('Total').': '.$data['total_tx'])
                ->descriptionIcon('heroicon-o-arrow-up')
                ->color('success'),
            Stat::make(__('Download'), $data['rx_speed'])
                ->description(__('Total').': '.$data['total_rx'])
                ->descriptionIcon('heroicon-o-arrow-down')
                ->color('info'),
        ];
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
