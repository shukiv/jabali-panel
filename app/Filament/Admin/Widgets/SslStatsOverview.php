<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\Domain;
use App\Models\SslCertificate;
use Filament\Widgets\Widget;

class SslStatsOverview extends Widget
{
    protected ?string $pollingInterval = '30s';

    protected int|string|array $columnSpan = 'full';

    protected string $view = 'filament.admin.widgets.ssl-stats';

    protected function getStats(): array
    {
        $totalDomains = Domain::count();
        $domainsWithSsl = SslCertificate::where('status', 'active')->where('service', 'web')->count();
        $mailCerts = SslCertificate::where('status', 'active')->where('service', 'mail')->count();
        $expiringSoon = SslCertificate::where('status', 'active')
            ->where('expires_at', '<=', now()->addDays(30))
            ->where('expires_at', '>', now())
            ->count();
        $expired = SslCertificate::where('status', 'expired')
            ->orWhere(function ($q) {
                $q->where('expires_at', '<', now());
            })
            ->count();
        $failed = SslCertificate::where('status', 'failed')->count();
        $withoutSsl = $totalDomains - $domainsWithSsl;

        return [
            [
                'value' => $domainsWithSsl,
                'label' => __('Web SSL'),
                'icon' => 'heroicon-m-shield-check',
                'color' => 'success',
            ],
            [
                'value' => $mailCerts,
                'label' => __('Mail SSL'),
                'icon' => 'heroicon-m-envelope',
                'color' => $mailCerts > 0 ? 'success' : 'gray',
            ],
            [
                'value' => $withoutSsl,
                'label' => __('Without SSL'),
                'icon' => 'heroicon-m-shield-exclamation',
                'color' => 'gray',
            ],
            [
                'value' => $expiringSoon,
                'label' => __('Expiring Soon'),
                'icon' => 'heroicon-m-clock',
                'color' => $expiringSoon > 0 ? 'warning' : 'success',
            ],
            [
                'value' => $expired,
                'label' => __('Expired'),
                'icon' => 'heroicon-m-x-circle',
                'color' => $expired > 0 ? 'danger' : 'success',
            ],
            [
                'value' => $failed,
                'label' => __('Failed'),
                'icon' => 'heroicon-m-exclamation-triangle',
                'color' => $failed > 0 ? 'danger' : 'success',
            ],
        ];
    }
}
