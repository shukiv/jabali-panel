<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use BackedEnum;
use Filament\Pages\Page;
use Illuminate\Contracts\Support\Htmlable;

class Support extends Page
{
    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-question-mark-circle';

    protected static ?int $navigationSort = 23;

    protected static ?string $slug = 'support';

    protected string $view = 'filament.admin.pages.support';

    public static function getNavigationLabel(): string
    {
        return __('Support');
    }

    public function getTitle(): string|Htmlable
    {
        return __('Support');
    }
}
