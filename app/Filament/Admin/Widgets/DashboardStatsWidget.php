<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\Domain;
use App\Models\Mailbox;
use App\Models\MysqlCredential;
use App\Models\User;
use Filament\Widgets\Widget;

class DashboardStatsWidget extends Widget
{
    protected static ?int $sort = 1;

    protected int|string|array $columnSpan = 'full';

    protected string $view = 'filament.admin.widgets.dashboard-stats';

    public function getStats(): array
    {
        $userCount = User::where('is_admin', false)->count();
        $domainCount = Domain::count();
        $mailboxCount = Mailbox::count();
        $databaseCount = MysqlCredential::count();
        return [
            [
                'value' => $userCount,
                'label' => __('Users'),
                'icon' => 'heroicon-o-users',
                'color' => 'primary',
            ],
            [
                'value' => $domainCount,
                'label' => __('Domains'),
                'icon' => 'heroicon-o-globe-alt',
                'color' => 'success',
            ],
            [
                'value' => $mailboxCount,
                'label' => __('Mailboxes'),
                'icon' => 'heroicon-o-envelope',
                'color' => 'info',
            ],
            [
                'value' => $databaseCount,
                'label' => __('Databases'),
                'icon' => 'heroicon-o-circle-stack',
                'color' => 'warning',
            ],
        ];
    }
}
