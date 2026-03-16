<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use BackedEnum;
use Filament\Pages\Page;

class PostgreSQL extends Page
{
    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-server-stack';

    protected static ?int $navigationSort = 9;

    protected static ?string $slug = 'postgresql';

    protected static bool $shouldRegisterNavigation = false;

    protected string $view = 'filament.jabali.pages.postgresql';

    public function mount(): void
    {
        $this->redirect(url('/jabali-panel/databases?tab=postgresql'));
    }
}
