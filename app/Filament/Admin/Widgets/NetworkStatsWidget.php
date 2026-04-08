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

    protected ?string $pollingInterval = '3s';

    protected function getStats(): array
    {
        $totalRx = 0;
        $totalTx = 0;

        if (is_readable('/proc/net/dev')) {
            $lines = file('/proc/net/dev', FILE_IGNORE_NEW_LINES);
            foreach ($lines as $line) {
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
        $cacheKey = 'jabali_net_sample';

        $prev = cache()->get($cacheKey);

        if (is_array($prev) && isset($prev['time'], $prev['rx'], $prev['tx'])) {
            $elapsed = $now - (float) $prev['time'];
            if ($elapsed > 0.5 && $elapsed < 120) {
                $rxSpeed = max(0, ($totalRx - (int) $prev['rx'])) / $elapsed;
                $txSpeed = max(0, ($totalTx - (int) $prev['tx'])) / $elapsed;
            }
        }

        cache()->put($cacheKey, [
            'time' => $now,
            'rx' => $totalRx,
            'tx' => $totalTx,
        ], 300);

        return [
            Stat::make(__('Upload'), Formatter::bytes($txSpeed).'/s')
                ->description(Formatter::bytes($totalTx).' '.__('total'))
                ->icon('heroicon-o-arrow-up')
                ->color('success'),
            Stat::make(__('Download'), Formatter::bytes($rxSpeed).'/s')
                ->description(Formatter::bytes($totalRx).' '.__('total'))
                ->icon('heroicon-o-arrow-down')
                ->color('info'),
        ];
    }
}
