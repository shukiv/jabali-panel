<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Support\Formatter;
use Filament\Widgets\Widget;

class NetworkStatsWidget extends Widget
{
    protected static ?int $sort = 4;

    protected int|string|array $columnSpan = 1;

    protected string $view = 'filament.admin.widgets.network-stats';

    public function getNetworkData(): array
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
