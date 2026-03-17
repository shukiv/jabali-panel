<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use App\Support\Formatter;
use Filament\Widgets\Widget;
use Illuminate\Support\Facades\Auth;

class DiskUsageWidget extends Widget
{
    protected static ?int $sort = 4;

    protected int|string|array $columnSpan = 1;

    protected string $view = 'filament.jabali.widgets.disk-usage';

    public function getData(): array
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
