<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use App\Models\DomainBandwidthUsage;
use App\Support\Formatter;
use Filament\Widgets\Widget;
use Illuminate\Support\Facades\Auth;

class DiskUsageWidget extends Widget
{
    protected static ?int $sort = 4;

    protected int|string|array $columnSpan = 1;

    protected string $view = 'filament.jabali.widgets.disk-usage';

    public function getDiskData(): array
    {
        $user = Auth::user();
        $usedBytes = $user->getDiskUsageBytes();
        $quotaBytes = $user->quota_bytes;
        $percent = $user->disk_usage_percent;

        return [
            'used' => Formatter::bytes($usedBytes),
            'quota' => $quotaBytes > 0 ? Formatter::bytes($quotaBytes) : __('Unlimited'),
            'free' => $quotaBytes > 0 ? Formatter::bytes(max(0, $quotaBytes - $usedBytes)) : null,
            'percent' => $percent,
            'has_quota' => $quotaBytes > 0,
            'home' => $user->home_directory,
            'color' => $this->getColor($percent),
        ];
    }

    public function getBandwidthData(): array
    {
        $user = Auth::user();
        $package = $user->hostingPackage;
        $quotaBytes = $package ? (int) $package->bandwidth_gb * 1073741824 : 0;

        $usedBytes = (int) DomainBandwidthUsage::whereHas('domain', fn ($q) => $q->where('user_id', $user->id))
            ->whereYear('date', now()->year)
            ->whereMonth('date', now()->month)
            ->sum('bytes_out');

        $percent = $quotaBytes > 0 ? min(100, round(($usedBytes / $quotaBytes) * 100, 1)) : 0;

        return [
            'used' => Formatter::bytes($usedBytes),
            'quota' => $quotaBytes > 0 ? Formatter::bytes($quotaBytes) : __('Unlimited'),
            'free' => $quotaBytes > 0 ? Formatter::bytes(max(0, $quotaBytes - $usedBytes)) : null,
            'percent' => $percent,
            'has_quota' => $quotaBytes > 0,
            'period' => now()->format('F Y'),
            'color' => $this->getColor((float) $percent),
        ];
    }

    public function getLimitsData(): array
    {
        $user = Auth::user();
        $package = $user->hostingPackage;

        $domainsUsed = $user->domains()->count();

        // Count MySQL databases matching username prefix (managed by agent, no Laravel model)
        $databasesUsed = 0;
        try {
            $prefix = $user->username.'\\_';
            $rows = \Illuminate\Support\Facades\DB::select('SHOW DATABASES LIKE ?', [$prefix.'%']);
            $databasesUsed = count($rows);
        } catch (\Throwable) {
        }

        $mailboxesUsed = \App\Models\Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', $user->id))->count();

        return [
            'domains_used' => $domainsUsed,
            'domains_limit' => $package?->domains_limit ?: '∞',
            'databases_used' => $databasesUsed,
            'databases_limit' => $package?->databases_limit ?: '∞',
            'mailboxes_used' => $mailboxesUsed,
            'mailboxes_limit' => $package?->mailboxes_limit ?: '∞',
        ];
    }

    public function getChartData(): array
    {
        $disk = $this->getDiskData();
        $bw = $this->getBandwidthData();
        $limits = $this->getLimitsData();

        return [
            'disk' => [
                [__('Disk'), (float) $disk['percent'], $disk['used'].' / '.$disk['quota']],
            ],
            'bandwidth' => [
                [__('Bandwidth'), (float) $bw['percent'], $bw['used'].' / '.$bw['quota']],
            ],
            'limits' => [
                'domains' => $limits['domains_used'].' / '.$limits['domains_limit'],
                'databases' => $limits['databases_used'].' / '.$limits['databases_limit'],
                'mailboxes' => $limits['mailboxes_used'].' / '.$limits['mailboxes_limit'],
            ],
        ];
    }

    protected function getColor(float $percent): string
    {
        if ($percent >= 90) {
            return 'danger';
        }
        if ($percent >= 70) {
            return 'warning';
        }

        return 'success';
    }
}
