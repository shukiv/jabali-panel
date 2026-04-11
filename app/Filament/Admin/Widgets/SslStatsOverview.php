<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\Domain;
use App\Models\SslCertificate;
use Filament\Widgets\StatsOverviewWidget;
use Filament\Widgets\StatsOverviewWidget\Stat;

class SslStatsOverview extends StatsOverviewWidget
{
    protected ?string $pollingInterval = '30s';

    protected function getColumns(): int
    {
        return 6;
    }

    protected function getStats(): array
    {
        $totalDomains = Domain::count();
        $webSsl = SslCertificate::where('status', 'active')->where('service', 'web')->count();
        $mailSsl = SslCertificate::where('status', 'active')->where('service', 'mail')->count();
        $expiringSoon = SslCertificate::where('status', 'active')
            ->where('expires_at', '<=', now()->addDays(30))
            ->where('expires_at', '>', now())
            ->count();
        $expired = SslCertificate::where('status', 'expired')
            ->orWhere(fn ($q) => $q->where('expires_at', '<', now()))
            ->count();
        $failed = SslCertificate::where('status', 'failed')->count();
        $withoutSsl = $totalDomains - $webSsl;

        return [
            Stat::make(__('Web SSL'), $webSsl)
                ->icon('heroicon-m-shield-check')
                ->color('success'),
            Stat::make(__('Mail SSL'), $mailSsl)
                ->icon('heroicon-m-envelope')
                ->color($mailSsl > 0 ? 'success' : 'gray'),
            Stat::make(__('Without SSL'), $withoutSsl)
                ->icon('heroicon-m-shield-exclamation')
                ->color('gray'),
            Stat::make(__('Expiring Soon'), $expiringSoon)
                ->icon('heroicon-m-clock')
                ->color($expiringSoon > 0 ? 'warning' : 'success'),
            Stat::make(__('Expired'), $expired)
                ->icon('heroicon-m-x-circle')
                ->color($expired > 0 ? 'danger' : 'success'),
            Stat::make(__('Failed'), $failed)
                ->icon('heroicon-m-exclamation-triangle')
                ->color($failed > 0 ? 'danger' : 'success'),
        ];
    }
}
