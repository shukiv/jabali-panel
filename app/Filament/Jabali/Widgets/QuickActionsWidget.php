<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use Filament\Widgets\Widget;

class QuickActionsWidget extends Widget
{
    protected static ?int $sort = 0;

    protected int|string|array $columnSpan = 'full';

    protected string $view = 'filament.jabali.widgets.quick-actions';

    public function getActions(): array
    {
        return [
            // Primary actions
            [
                'label' => __('Domains'),
                'icon' => 'heroicon-o-globe-americas',
                'url' => route('filament.jabali.pages.domains'),
                'primary' => true,
            ],
            [
                'label' => __('Files'),
                'icon' => 'heroicon-o-folder',
                'url' => route('filament.jabali.pages.files'),
                'primary' => true,
            ],
            [
                'label' => __('Email'),
                'icon' => 'heroicon-o-envelope',
                'url' => route('filament.jabali.pages.email'),
                'primary' => true,
            ],
            // Secondary actions
            [
                'label' => __('Databases'),
                'icon' => 'heroicon-o-circle-stack',
                'url' => route('filament.jabali.pages.databases'),
            ],
            [
                'label' => __('WordPress'),
                'icon' => 'heroicon-o-command-line',
                'url' => route('filament.jabali.pages.wordpress'),
            ],
            [
                'label' => __('SSL'),
                'icon' => 'heroicon-o-lock-closed',
                'url' => route('filament.jabali.pages.ssl'),
            ],
            [
                'label' => __('Cron'),
                'icon' => 'heroicon-o-clock',
                'url' => route('filament.jabali.pages.cron-jobs'),
            ],
            [
                'label' => __('SSH'),
                'icon' => 'heroicon-o-key',
                'url' => route('filament.jabali.pages.ssh-keys'),
            ],
        ];
    }
}
