<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use App\Models\EmailDomain;
use Filament\Widgets\Widget;
use Illuminate\Support\Facades\Auth;

class EmailStatsWidget extends Widget
{
    protected static ?int $sort = 0;

    protected int|string|array $columnSpan = 'full';

    protected string $view = 'filament.jabali.widgets.email-stats';

    public function getStats(): array
    {
        $domains = EmailDomain::whereHas('domain', fn ($q) => $q->where('user_id', Auth::id()))
            ->with(['mailboxes', 'domain'])
            ->get();

        $totalMailboxes = 0;
        $totalUsed = 0;
        $totalQuota = 0;

        foreach ($domains as $domain) {
            $totalMailboxes += $domain->mailboxes->count();
            $totalUsed += $domain->mailboxes->sum('quota_used_bytes');
            $totalQuota += $domain->mailboxes->sum('quota_bytes');
        }

        $percent = $totalQuota > 0 ? round(($totalUsed / $totalQuota) * 100, 1) : 0;

        return [
            [
                'value' => $domains->count(),
                'label' => __('Domains'),
                'icon' => 'heroicon-o-globe-alt',
                'color' => 'success',
            ],
            [
                'value' => $totalMailboxes,
                'label' => __('Mailboxes'),
                'icon' => 'heroicon-o-envelope',
                'color' => 'info',
            ],
            [
                'value' => $this->formatBytes($totalUsed),
                'label' => __('Used'),
                'icon' => 'heroicon-o-server',
                'color' => 'warning',
            ],
            [
                'value' => $percent . '%',
                'label' => __('Quota'),
                'icon' => 'heroicon-o-chart-pie',
                'color' => $percent >= 90 ? 'danger' : ($percent >= 80 ? 'warning' : 'gray'),
            ],
        ];
    }

    protected function formatBytes(int $bytes): string
    {
        if ($bytes < 1024) return $bytes . ' B';
        if ($bytes < 1048576) return round($bytes / 1024, 1) . ' KB';
        if ($bytes < 1073741824) return round($bytes / 1048576, 1) . ' MB';
        return round($bytes / 1073741824, 1) . ' GB';
    }
}
