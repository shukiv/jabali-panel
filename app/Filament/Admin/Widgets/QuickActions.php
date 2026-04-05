<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use Filament\Widgets\Widget;

class QuickActions extends Widget
{
    protected static ?int $sort = 0;

    protected int|string|array $columnSpan = 'full';

    protected string $view = 'filament.admin.widgets.quick-actions';

    public function getActions(): array
    {
        return [
            [
                'label' => __('Users'),
                'icon' => 'heroicon-o-users',
                'url' => route('filament.admin.resources.users.index'),
            ],
            [
                'label' => __('Services'),
                'icon' => 'heroicon-o-server',
                'url' => route('filament.admin.pages.services'),
            ],
            [
                'label' => __('Server'),
                'icon' => 'heroicon-o-cpu-chip',
                'url' => route('filament.admin.pages.server-status'),
            ],
            [
                'label' => __('Settings'),
                'icon' => 'heroicon-o-cog-6-tooth',
                'url' => route('filament.admin.pages.server-settings'),
            ],
            [
                'label' => __('SSL'),
                'icon' => 'heroicon-o-lock-closed',
                'url' => route('filament.admin.pages.ssl-manager'),
            ],
            [
                'label' => __('PHP'),
                'icon' => 'heroicon-o-code-bracket',
                'url' => route('filament.admin.pages.php-manager'),
            ],
            [
                'label' => __('DNS'),
                'icon' => 'heroicon-o-server-stack',
                'url' => route('filament.admin.pages.dns-zones'),
            ],
        ];
    }
}
