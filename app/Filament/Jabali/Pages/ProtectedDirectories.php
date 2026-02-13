<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Services\Agent\AgentClient;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Section;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;

class ProtectedDirectories extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-lock-closed';

    protected static ?int $navigationSort = 12;

    public static function getNavigationLabel(): string
    {
        return __('Protected Directories');
    }

    public function getTitle(): string|Htmlable
    {
        return __('Protected Directories');
    }

    protected static ?string $slug = 'protected-directories';

    protected string $view = 'filament.jabali.pages.protected-directories';

    public array $domains = [];

    public ?string $selectedDomain = null;

    public array $protectedDirs = [];

    protected ?AgentClient $agent = null;

    protected function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient;
        }

        return $this->agent;
    }

    protected function getUsername(): string
    {
        return Auth::user()->username ?? Auth::user()->name ?? 'unknown';
    }

    public function mount(): void
    {
        $this->loadDomains();

        if (! empty($this->domains)) {
            $this->selectedDomain = $this->domains[0]['domain'] ?? null;
            $this->loadProtectedDirectories();
        }
    }

    protected function loadDomains(): void
    {
        if ((bool) env('JABALI_DEMO', false)) {
            $this->domains = [
                ['domain' => 'jabali-panel.com'],
                ['domain' => 'demo-site.com'],
                ['domain' => 'store.demo'],
            ];

            return;
        }

        $result = $this->getAgent()->send('domain.list', [
            'username' => $this->getUsername(),
        ]);

        $this->domains = ($result['success'] ?? false) ? ($result['domains'] ?? []) : [];
    }

    public function selectDomain(string $domain): void
    {
        $this->selectedDomain = $domain;
        $this->loadProtectedDirectories();
        $this->resetTable();
    }

    public function loadProtectedDirectories(): void
    {
        if (! $this->selectedDomain) {
            $this->protectedDirs = [];

            return;
        }

        if ((bool) env('JABALI_DEMO', false)) {
            $this->protectedDirs = [
                [
                    'path' => '/admin',
                    'name' => 'Restricted Area',
                    'users_count' => 2,
                    'users' => [
                        ['username' => 'demo', 'created_at' => now()->subDays(10)->toDateTimeString()],
                        ['username' => 'editor', 'created_at' => now()->subDays(3)->toDateTimeString()],
                    ],
                ],
                [
                    'path' => '/private',
                    'name' => 'Private Files',
                    'users_count' => 1,
                    'users' => [
                        ['username' => 'staff', 'created_at' => now()->subDays(1)->toDateTimeString()],
                    ],
                ],
            ];

            return;
        }

        $result = $this->getAgent()->send('domain.list_protected_dirs', [
            'domain' => $this->selectedDomain,
            'username' => $this->getUsername(),
        ]);

        $this->protectedDirs = ($result['success'] ?? false) ? ($result['directories'] ?? []) : [];
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->protectedDirs)
            ->columns([
                TextColumn::make('path')
                    ->label(__('Directory'))
                    ->icon('heroicon-o-folder')
                    ->searchable(),
                TextColumn::make('name')
                    ->label(__('Protected Area Name'))
                    ->searchable(),
                TextColumn::make('users_count')
                    ->label(__('Users'))
                    ->badge()
                    ->color('info'),
            ])
            ->actions([
                \Filament\Actions\Action::make('manageUsers')
                    ->label(__('Manage Users'))
                    ->icon('heroicon-o-users')
                    ->color('gray')
                    ->modalHeading(fn (array $record): string => __('Manage Users for :path', ['path' => $record['path']]))
                    ->modalWidth('lg')
                    ->form(fn (array $record): array => [
                        Section::make(__('Current Users'))
                            ->description(__('Users who can access this protected directory.'))
                            ->schema([
                                \Filament\Schemas\Components\View::make('filament.jabali.components.protected-dir-users')
                                    ->viewData(['users' => $record['users'] ?? [], 'path' => $record['path']]),
                            ]),
                        Section::make(__('Add New User'))
                            ->schema([
                                TextInput::make('new_username')
                                    ->label(__('Username'))
                                    ->required()
                                    ->alphaNum()
                                    ->maxLength(32),
                                TextInput::make('new_password')
                                    ->label(__('Password'))
                                    ->password()
                                    ->required()
                                    ->minLength(6),
                            ]),
                    ])
                    ->action(function (array $data, array $record): void {
                        if (empty($data['new_username']) || empty($data['new_password'])) {
                            return;
                        }

                        $result = $this->getAgent()->send('domain.add_protected_dir_user', [
                            'domain' => $this->selectedDomain,
                            'username' => $this->getUsername(),
                            'path' => $record['path'],
                            'auth_username' => $data['new_username'],
                            'auth_password' => $data['new_password'],
                        ]);

                        if ($result['success'] ?? false) {
                            Notification::make()
                                ->title(__('User added'))
                                ->success()
                                ->send();
                            $this->loadProtectedDirectories();
                            $this->resetTable();
                        } else {
                            Notification::make()
                                ->title(__('Failed to add user'))
                                ->body($result['error'] ?? __('Unknown error'))
                                ->danger()
                                ->send();
                        }
                    }),
                \Filament\Actions\Action::make('remove')
                    ->label(__('Remove Protection'))
                    ->icon('heroicon-o-lock-open')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Remove Directory Protection'))
                    ->modalDescription(fn (array $record): string => __('Are you sure you want to remove password protection from ":path"? Anyone will be able to access this directory.', ['path' => $record['path']]))
                    ->action(function (array $record): void {
                        $result = $this->getAgent()->send('domain.remove_protected_dir', [
                            'domain' => $this->selectedDomain,
                            'username' => $this->getUsername(),
                            'path' => $record['path'],
                        ]);

                        if ($result['success'] ?? false) {
                            Notification::make()
                                ->title(__('Protection removed'))
                                ->body(__('Directory ":path" is no longer password protected.', ['path' => $record['path']]))
                                ->success()
                                ->send();
                            $this->loadProtectedDirectories();
                            $this->resetTable();
                        } else {
                            Notification::make()
                                ->title(__('Failed to remove protection'))
                                ->body($result['error'] ?? __('Unknown error'))
                                ->danger()
                                ->send();
                        }
                    }),
            ])
            ->emptyStateHeading(__('No protected directories'))
            ->emptyStateDescription(__('Add password protection to directories to restrict access.'))
            ->emptyStateIcon('heroicon-o-lock-open')
            ->striped();
    }

    protected function getHeaderActions(): array
    {
        return [
            $this->addProtectionAction(),
        ];
    }

    protected function addProtectionAction(): Action
    {
        return Action::make('addProtection')
            ->label(__('Protect Directory'))
            ->icon('heroicon-o-lock-closed')
            ->color('primary')
            ->visible(fn () => $this->selectedDomain !== null)
            ->modalHeading(__('Protect a Directory'))
            ->modalDescription(__('Add password protection to a directory. Users will need to enter a username and password to access it.'))
            ->form([
                TextInput::make('path')
                    ->label(__('Directory Path'))
                    ->placeholder(__('/admin'))
                    ->required()
                    ->helperText(__('Path relative to your document root (e.g., /admin, /private, /members)')),
                TextInput::make('name')
                    ->label(__('Protected Area Name'))
                    ->placeholder(__('Restricted Area'))
                    ->required()
                    ->maxLength(100)
                    ->helperText(__('This name will be shown in the browser login prompt.')),
                TextInput::make('auth_username')
                    ->label(__('Username'))
                    ->required()
                    ->alphaNum()
                    ->maxLength(32)
                    ->helperText(__('Username for accessing the protected directory.')),
                TextInput::make('auth_password')
                    ->label(__('Password'))
                    ->password()
                    ->required()
                    ->minLength(6)
                    ->helperText(__('Password for the user. Minimum 6 characters.')),
            ])
            ->action(function (array $data): void {
                // Normalize path
                $path = '/'.ltrim(trim($data['path']), '/');

                $result = $this->getAgent()->send('domain.add_protected_dir', [
                    'domain' => $this->selectedDomain,
                    'username' => $this->getUsername(),
                    'path' => $path,
                    'name' => $data['name'],
                    'auth_username' => $data['auth_username'],
                    'auth_password' => $data['auth_password'],
                ]);

                if ($result['success'] ?? false) {
                    Notification::make()
                        ->title(__('Directory protected'))
                        ->body(__('Password protection has been added to ":path".', ['path' => $path]))
                        ->success()
                        ->send();
                    $this->loadProtectedDirectories();
                    $this->resetTable();
                } else {
                    Notification::make()
                        ->title(__('Failed to protect directory'))
                        ->body($result['error'] ?? __('Unknown error'))
                        ->danger()
                        ->send();
                }
            });
    }

    public function deleteProtectedDirUser(string $path, string $authUsername): void
    {
        $result = $this->getAgent()->send('domain.remove_protected_dir_user', [
            'domain' => $this->selectedDomain,
            'username' => $this->getUsername(),
            'path' => $path,
            'auth_username' => $authUsername,
        ]);

        if ($result['success'] ?? false) {
            Notification::make()
                ->title(__('User removed'))
                ->success()
                ->send();
            $this->loadProtectedDirectories();
            $this->resetTable();
        } else {
            Notification::make()
                ->title(__('Failed to remove user'))
                ->body($result['error'] ?? __('Unknown error'))
                ->danger()
                ->send();
        }
    }
}
