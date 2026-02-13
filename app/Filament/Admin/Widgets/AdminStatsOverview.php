<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\Domain;
use App\Models\SslCertificate;
use App\Models\User;
use Filament\Widgets\StatsOverviewWidget as BaseWidget;
use Filament\Widgets\StatsOverviewWidget\Stat;

class AdminStatsOverview extends BaseWidget
{
    protected function getStats(): array
    {
        $userCount = User::where('is_admin', false)->count();
        $domainCount = Domain::count();
        $sslActiveCount = SslCertificate::where('status', 'active')->count();
        $sslExpiringCount = SslCertificate::where('status', 'active')
            ->where('expires_at', '<=', now()->addDays(30))
            ->where('expires_at', '>', now())
            ->count();

        return [
            Stat::make(__('Users'), $userCount)
                ->description(__('Total accounts'))
                ->descriptionIcon('heroicon-m-users')
                ->color('danger')
                ->url(route('filament.admin.resources.users.index')),

            Stat::make(__('Domains'), $domainCount)
                ->description(__('Hosted domains'))
                ->descriptionIcon('heroicon-m-globe-alt')
                ->color('success')
                ->url(url('/jabali-admin/dns-zones')),

            Stat::make(__('SSL Certificates'), $sslActiveCount)
                ->description($sslExpiringCount > 0 ? __(':count expiring soon', ['count' => $sslExpiringCount]) : __('All certificates valid'))
                ->descriptionIcon($sslExpiringCount > 0 ? 'heroicon-m-exclamation-triangle' : 'heroicon-m-check-circle')
                ->color($sslExpiringCount > 0 ? 'warning' : 'info')
                ->url(url('/jabali-admin/ssl-manager')),

            Stat::make(__('Server Status'), __('Healthy'))
                ->description(__('View metrics'))
                ->descriptionIcon('heroicon-m-server')
                ->color('gray')
                ->url(url('/jabali-admin/server-status')),
        ];
    }
}
