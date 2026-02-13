<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use BackedEnum;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Pages\Page;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Livewire\Attributes\Url;

class Migration extends Page implements HasForms
{
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-arrow-down-tray';

    protected static ?string $navigationLabel = null;

    protected static ?int $navigationSort = 15;

    protected string $view = 'filament.jabali.pages.migration';

    #[Url(as: 'migration')]
    public string $activeTab = 'cpanel';

    public static function getNavigationLabel(): string
    {
        return __('Migration');
    }

    public function getTitle(): string|Htmlable
    {
        return __('Migration');
    }

    public function getSubheading(): ?string
    {
        return __('Migrate a cPanel or DirectAdmin account into your Jabali account');
    }

    public function mount(): void
    {
        if (! in_array($this->activeTab, ['cpanel', 'directadmin'], true)) {
            $this->activeTab = 'cpanel';
        }
    }

    public function updatedActiveTab(string $activeTab): void
    {
        if (! in_array($activeTab, ['cpanel', 'directadmin'], true)) {
            $this->activeTab = 'cpanel';
        }
    }

    protected function getForms(): array
    {
        return ['migrationForm'];
    }

    public function migrationForm(Schema $schema): Schema
    {
        return $schema->schema([
            Tabs::make(__('Migration Type'))
                ->livewireProperty('activeTab')
                ->tabs([
                    'cpanel' => Tabs\Tab::make(__('cPanel Migration'))
                        ->icon('heroicon-o-arrow-down-tray')
                        ->schema([
                            View::make('filament.jabali.pages.migration-cpanel-tab'),
                        ]),
                    'directadmin' => Tabs\Tab::make(__('DirectAdmin Migration'))
                        ->icon('heroicon-o-arrow-down-tray')
                        ->schema([
                            View::make('filament.jabali.pages.migration-directadmin-tab'),
                        ]),
                ]),
        ]);
    }
}
