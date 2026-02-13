<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Services\Agent\AgentClient;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;

class PhpManager extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-code-bracket';

    protected static ?int $navigationSort = 6;

    protected static ?string $slug = 'php-manager';

    protected string $view = 'filament.admin.pages.php-manager';

    public array $installedVersions = [];

    public array $availableVersions = [];

    public ?string $defaultVersion = null;

    public static function getNavigationLabel(): string
    {
        return __('PHP Manager');
    }

    public function getTitle(): string|Htmlable
    {
        return __('PHP Manager');
    }

    protected function getAgent(): AgentClient
    {
        return new AgentClient;
    }

    public function mount(): void
    {
        $this->loadPhpVersions();
    }

    public function loadPhpVersions(): void
    {
        if ((bool) env('JABALI_DEMO', false)) {
            $this->installedVersions = [
                ['version' => '8.4', 'fpm_status' => 'active'],
                ['version' => '8.3', 'fpm_status' => 'active'],
                ['version' => '8.2', 'fpm_status' => 'inactive'],
                ['version' => '8.1', 'fpm_status' => 'inactive'],
            ];
            $this->defaultVersion = '8.4';
            $allVersions = ['7.4', '8.0', '8.1', '8.2', '8.3', '8.4'];
            $installed = array_column($this->installedVersions, 'version');
            $this->availableVersions = array_diff($allVersions, $installed);

            return;
        }

        try {
            $result = $this->getAgent()->send('php.list_versions', []);
        } catch (\Exception $e) {
            $this->installedVersions = [];
            $this->defaultVersion = null;
            $this->availableVersions = [];

            return;
        }

        if ($result['success'] ?? false) {
            $this->installedVersions = $result['versions'] ?? [];
            $this->defaultVersion = $result['default'] ?? null;
        } else {
            $this->installedVersions = [];
            $this->defaultVersion = null;
        }

        $allVersions = ['7.4', '8.0', '8.1', '8.2', '8.3', '8.4'];
        $installed = array_column($this->installedVersions, 'version');
        $this->availableVersions = array_diff($allVersions, $installed);
    }

    protected function getForms(): array
    {
        return ['statsForm'];
    }

    public function statsForm(Schema $schema): Schema
    {
        return $schema->schema([
            Section::make(__('Warning: Modifying PHP versions can cause server downtime'))
                ->description(__('Uninstalling PHP versions may break websites that depend on them. Ensure you understand the impact before making changes.'))
                ->icon('heroicon-o-exclamation-triangle')
                ->iconColor('warning')
                ->collapsed(false)
                ->compact(),
            Grid::make(['default' => 1, 'sm' => 2])
                ->schema([
                    Section::make('PHP '.($this->defaultVersion ?? __('N/A')))
                        ->description(__('Default CLI Version'))
                        ->icon('heroicon-o-command-line')
                        ->iconColor('primary'),
                    Section::make((string) count($this->installedVersions))
                        ->description(__('Installed Versions'))
                        ->icon('heroicon-o-squares-2x2')
                        ->iconColor('success'),
                ]),
        ]);
    }

    public function getAllVersionsData(): array
    {
        $allVersions = ['8.4', '8.3', '8.2', '8.1', '8.0', '7.4'];
        $installedMap = [];

        foreach ($this->installedVersions as $php) {
            $installedMap[$php['version']] = $php;
        }

        $result = [];
        foreach ($allVersions as $version) {
            $installed = isset($installedMap[$version]);
            $result[] = [
                'version' => $version,
                'installed' => $installed,
                'fpm_status' => $installed ? ($installedMap[$version]['fpm_status'] ?? 'inactive') : null,
                'is_default' => $version === $this->defaultVersion,
            ];
        }

        return $result;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getAllVersionsData())
            ->columns([
                TextColumn::make('version')
                    ->label(__('PHP Version'))
                    ->formatStateUsing(fn (string $state): string => 'PHP '.$state)
                    ->icon('heroicon-o-code-bracket')
                    ->weight('bold')
                    ->sortable(),
                TextColumn::make('installed')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (bool $state): string => $state ? __('Installed') : __('Not Installed'))
                    ->color(fn (bool $state): string => $state ? 'success' : 'gray'),
                IconColumn::make('is_default')
                    ->label(__('Default'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-badge')
                    ->falseIcon('heroicon-o-minus')
                    ->trueColor('info')
                    ->falseColor('gray'),
                TextColumn::make('fpm_status')
                    ->label(__('FPM'))
                    ->badge()
                    ->formatStateUsing(fn (?string $state): string => $state === 'active' ? __('Running') : ($state ? __('Stopped') : '-'))
                    ->color(fn (?string $state): string => $state === 'active' ? 'success' : ($state ? 'danger' : 'gray')),
            ])
            ->recordActions([
                Action::make('install')
                    ->label(__('Install'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('primary')
                    ->size('sm')
                    ->visible(fn (array $record): bool => ! $record['installed'])
                    ->action(fn (array $record) => $this->installPhp($record['version'])),
                Action::make('reload')
                    ->label(__('Reload'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('warning')
                    ->size('sm')
                    ->visible(fn (array $record): bool => $record['installed'])
                    ->requiresConfirmation()
                    ->modalHeading(__('Reload PHP-FPM'))
                    ->modalDescription(fn (array $record): string => __('Are you sure you want to reload PHP :version FPM?', ['version' => $record['version']]))
                    ->action(fn (array $record) => $this->reloadFpm($record['version'])),
                Action::make('uninstall')
                    ->label(__('Uninstall'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->size('sm')
                    ->visible(fn (array $record): bool => $record['installed'] && ! $record['is_default'])
                    ->action(fn (array $record) => $this->uninstallPhp($record['version'])),
            ])
            ->heading(__('PHP Versions'))
            ->description(__('Install, manage and configure PHP versions'))
            ->striped()
            ->poll('30s');
    }

    public function installPhp(string $version): void
    {
        $result = $this->getAgent()->send('php.install', ['version' => $version]);

        if ($result['success'] ?? false) {
            $this->loadPhpVersions();
            $this->resetTable();
            Notification::make()
                ->title(__('PHP :version installed successfully!', ['version' => $version]))
                ->success()
                ->send();
        } else {
            Notification::make()
                ->title(__('Failed to install PHP :version', ['version' => $version]))
                ->body($result['error'] ?? __('Unknown error'))
                ->danger()
                ->send();
        }
    }

    public function uninstallPhp(string $version): void
    {
        if ($version === $this->defaultVersion) {
            Notification::make()
                ->title(__('Cannot uninstall default PHP version'))
                ->body(__('Please set a different PHP version as default first'))
                ->danger()
                ->send();

            return;
        }

        $result = $this->getAgent()->send('php.uninstall', ['version' => $version]);

        if ($result['success'] ?? false) {
            $this->loadPhpVersions();
            $this->resetTable();
            Notification::make()
                ->title(__('PHP :version uninstalled successfully!', ['version' => $version]))
                ->success()
                ->send();
        } else {
            Notification::make()
                ->title(__('Failed to uninstall PHP :version', ['version' => $version]))
                ->body($result['error'] ?? __('Unknown error'))
                ->danger()
                ->send();
        }
    }

    public function setDefaultPhp(string $version): void
    {
        $result = $this->getAgent()->send('php.set_default', ['version' => $version]);

        if ($result['success'] ?? false) {
            $this->defaultVersion = $version;
            $this->loadPhpVersions();
            $this->resetTable();
            Notification::make()
                ->title(__('PHP :version set as default', ['version' => $version]))
                ->success()
                ->send();
        } else {
            Notification::make()
                ->title(__('Failed to set default PHP version'))
                ->body($result['error'] ?? __('Unknown error'))
                ->danger()
                ->send();
        }
    }

    public function reloadFpm(string $version): void
    {
        $result = $this->getAgent()->send('php.reload_fpm', ['version' => $version]);

        if ($result['success'] ?? false) {
            $this->loadPhpVersions();
            $this->resetTable();
            Notification::make()
                ->title(__('PHP :version FPM reloaded', ['version' => $version]))
                ->success()
                ->send();
        } else {
            Notification::make()
                ->title(__('Failed to reload PHP-FPM'))
                ->body($result['error'] ?? __('Unknown error'))
                ->danger()
                ->send();
        }
    }

    protected function getHeaderActions(): array
    {
        return [
        ];
    }
}
