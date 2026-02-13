<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Services\Agent\AgentClient;
use Filament\Widgets\StatsOverviewWidget as BaseWidget;
use Filament\Widgets\StatsOverviewWidget\Stat;

class MemoryWidget extends BaseWidget
{
    protected function getColumns(): int
    {
        return 4;
    }

    protected function getStats(): array
    {
        try {
            $agent = new AgentClient();
            $overview = $agent->metricsOverview();
            $memory = $overview['memory'] ?? [];
        } catch (\Exception $e) {
            return [];
        }

        $total = ($memory['total'] ?? 0) / 1024;
        $used = ($memory['used'] ?? 0) / 1024;
        $cached = ($memory['cached'] ?? 0) / 1024;
        $available = ($memory['available'] ?? 0) / 1024;

        return [
            Stat::make(__('Total'), number_format($total, 1) . ' GB')
                ->description(__('Total memory'))
                ->color('gray'),

            Stat::make(__('Used'), number_format($used, 1) . ' GB')
                ->description(__('In use'))
                ->color('success'),

            Stat::make(__('Cached'), number_format($cached, 1) . ' GB')
                ->description(__('Cached data'))
                ->color('primary'),

            Stat::make(__('Available'), number_format($available, 1) . ' GB')
                ->description(__('Free to use'))
                ->color('warning'),
        ];
    }
}
