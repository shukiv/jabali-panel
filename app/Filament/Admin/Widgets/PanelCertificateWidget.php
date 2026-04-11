<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\PanelCertificate;
use Filament\Widgets\StatsOverviewWidget;
use Filament\Widgets\StatsOverviewWidget\Stat;

class PanelCertificateWidget extends StatsOverviewWidget
{
    protected ?string $pollingInterval = '30s';

    protected function getHeading(): ?string
    {
        return __('Panel Certificate');
    }

    protected function getDescription(): ?string
    {
        $cert = $this->getCertificate();

        if (! $cert || $cert->isSelfSigned()) {
            return __('Using self-signed certificate. Run "Issue Certificate" from SSL Check to request a Let\'s Encrypt certificate.');
        }

        return null;
    }

    protected function getStats(): array
    {
        $cert = $this->getCertificate();

        $hostname = $cert?->hostname ?: (config('jabali.panel.hostname') ?: __('Not configured'));
        $issuer = $cert?->issuer ?? '-';
        $status = $cert ? $cert->status_label : __('No Certificate');
        $statusColor = $cert ? $cert->status_color : 'gray';

        $expiresStat = Stat::make(__('Expires'), $cert?->expires_at?->format('M j, Y') ?? '-');
        if ($cert?->days_until_expiry !== null) {
            $expiryColor = match (true) {
                $cert->days_until_expiry <= 7 => 'danger',
                $cert->days_until_expiry <= 30 => 'warning',
                default => 'success',
            };
            $expiresStat->description($cert->days_until_expiry.'d '.__('remaining'))
                ->color($expiryColor);
        }

        $renewalStat = Stat::make(__('Last Renewal'), $cert?->last_renewal_at?->diffForHumans() ?? '-');
        if ($cert?->last_renewal_result === 'success') {
            $renewalStat->description(__('Successful'))->color('success');
        } elseif ($cert?->last_renewal_result === 'failed') {
            $renewalStat->description($cert->last_error ?? __('Failed'))->color('danger');
        }

        return [
            Stat::make(__('Hostname'), $hostname),
            Stat::make(__('Status'), $status)
                ->color($statusColor),
            $expiresStat,
            $renewalStat,
        ];
    }

    protected function getCertificate(): ?PanelCertificate
    {
        return PanelCertificate::first();
    }
}
