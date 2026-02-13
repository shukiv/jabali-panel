<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

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
            'used' => $this->formatBytes($usedBytes),
            'quota' => $quotaBytes > 0 ? $this->formatBytes($quotaBytes) : __('Unlimited'),
            'free' => $quotaBytes > 0 ? $this->formatBytes(max(0, $quotaBytes - $usedBytes)) : null,
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

    protected function formatBytes(int $bytes, int $precision = 2): string
    {
        if ($bytes === 0) {
            return '0 B';
        }

        $units = ['B', 'KB', 'MB', 'GB', 'TB'];
        $pow = floor(log($bytes) / log(1024));
        $pow = min($pow, count($units) - 1);

        return round($bytes / pow(1024, $pow), $precision) . ' ' . $units[$pow];
    }
}
