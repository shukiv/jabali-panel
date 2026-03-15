<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Services\Agent\AgentClient;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Str;
use Livewire\Attributes\Url;

class PostgreSQL extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-circle-stack';

    protected static ?int $navigationSort = 9;

    protected static ?string $slug = 'postgresql';

    protected string $view = 'filament.jabali.pages.postgresql';

    protected ?AgentClient $agent = null;

    #[Url(as: 'tab')]
    public string $activeTab = 'databases';

    public array $databases = [];

    public array $users = [];

    public function getTitle(): string|Htmlable
    {
        return __('PostgreSQL');
    }

    public static function getNavigationLabel(): string
    {
        return __('PostgreSQL');
    }

    public function mount(): void
    {
        $this->activeTab = $this->normalizeTab($this->activeTab);
        $this->loadData();
    }

    public function updatedActiveTab(): void
    {
        $this->activeTab = $this->normalizeTab($this->activeTab);
        $this->loadData();
        $this->resetTable();
    }

    protected function getForms(): array
    {
        return ['tabsForm'];
    }

    public function tabsForm(Schema $schema): Schema
    {
        return $schema->schema([
            Tabs::make(__('PostgreSQL Sections'))
                ->contained()
                ->livewireProperty('activeTab')
                ->tabs([
                    'databases' => Tab::make(__('Databases'))
                        ->icon('heroicon-o-circle-stack')
                        ->schema([
                            View::make('filament.jabali.pages.postgresql-tab-table'),
                        ]),
                    'users' => Tab::make(__('Users'))
                        ->icon('heroicon-o-users')
                        ->schema([
                            View::make('filament.jabali.pages.postgresql-tab-table'),
                        ]),
                ]),
        ]);
    }

    protected function normalizeTab(string $tab): string
    {
        return in_array($tab, ['databases', 'users'], true) ? $tab : 'databases';
    }

    protected function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient;
        }

        return $this->agent;
    }

    protected function getUsername(): string
    {
        return Auth::user()->username;
    }

    protected function loadData(): void
    {
        if ($this->activeTab === 'users') {
            $this->loadUsers();
        } else {
            $this->loadDatabases();
        }
    }

    protected function loadDatabases(): void
    {
        try {
            $result = $this->getAgent()->postgresListDatabases($this->getUsername());
            $this->databases = $result['databases'] ?? [];
        } catch (Exception) {
            $this->databases = [];
        }
    }

    protected function loadUsers(): void
    {
        try {
            $result = $this->getAgent()->postgresListUsers($this->getUsername());
            $this->users = $result['users'] ?? [];
        } catch (Exception) {
            $this->users = [];
        }
    }

    protected function getUserOptions(): array
    {
        if (empty($this->users)) {
            $this->loadUsers();
        }

        $options = [];
        foreach ($this->users as $user) {
            $options[$user['username']] = $user['username'];
        }

        return $options;
    }

    public function table(Table $table): Table
    {
        if ($this->activeTab === 'users') {
            return $table
                ->records(fn () => $this->users)
                ->columns([
                    TextColumn::make('username')
                        ->label(__('User'))
                        ->searchable(),
                ])
                ->recordActions([
                    Action::make('delete')
                        ->label(__('Delete'))
                        ->icon('heroicon-o-trash')
                        ->color('danger')
                        ->requiresConfirmation()
                        ->action(function (array $record): void {
                            $result = $this->getAgent()->postgresDeleteUser($this->getUsername(), $record['username']);
                            if ($result['success'] ?? false) {
                                Notification::make()->title(__('User deleted'))->success()->send();
                                $this->loadUsers();
                                $this->resetTable();

                                return;
                            }

                            Notification::make()->title(__('Deletion failed'))->body($result['error'] ?? '')->danger()->send();
                        }),
                ])
                ->emptyStateHeading(__('No PostgreSQL users'))
                ->emptyStateDescription(__('Create a PostgreSQL user to manage databases'));
        }

        return $table
            ->records(fn () => $this->databases)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Database'))
                    ->searchable(),
                TextColumn::make('size_bytes')
                    ->label(__('Size'))
                    ->formatStateUsing(fn ($state) => $this->formatBytes((int) $state))
                    ->color('gray'),
            ])
            ->recordActions([
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function (array $record): void {
                        $result = $this->getAgent()->postgresDeleteDatabase($this->getUsername(), $record['name']);
                        if ($result['success'] ?? false) {
                            Notification::make()->title(__('Database deleted'))->success()->send();
                            $this->loadDatabases();
                            $this->resetTable();

                            return;
                        }

                        Notification::make()->title(__('Deletion failed'))->body($result['error'] ?? '')->danger()->send();
                    }),
            ])
            ->emptyStateHeading(__('No PostgreSQL databases'))
            ->emptyStateDescription(__('Create a PostgreSQL database to get started'));
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('createDatabase')
                ->label(__('Create Database'))
                ->icon('heroicon-o-circle-stack')
                ->color('primary')
                ->visible(fn () => $this->activeTab === 'databases')
                ->form([
                    TextInput::make('database')
                        ->label(__('Database Name'))
                        ->helperText(__('Use a name like :prefix_db', ['prefix' => $this->getUsername()]))
                        ->required(),
                    Select::make('owner')
                        ->label(__('Owner User'))
                        ->options($this->getUserOptions())
                        ->required(),
                ])
                ->action(function (array $data): void {
                    $result = $this->getAgent()->postgresCreateDatabase(
                        $this->getUsername(),
                        $data['database'],
                        $data['owner']
                    );

                    if ($result['success'] ?? false) {
                        Notification::make()->title(__('Database created'))->success()->send();
                        $this->loadDatabases();
                        $this->resetTable();

                        return;
                    }

                    Notification::make()->title(__('Creation failed'))->body($result['error'] ?? '')->danger()->send();
                }),
            Action::make('createUser')
                ->label(__('Create User'))
                ->icon('heroicon-o-user-plus')
                ->color('primary')
                ->visible(fn () => $this->activeTab === 'users')
                ->form([
                    TextInput::make('db_user')
                        ->label(__('Username'))
                        ->helperText(__('Use a name like :prefix_user', ['prefix' => $this->getUsername()]))
                        ->required(),
                    TextInput::make('password')
                        ->label(__('Password'))
                        ->password()
                        ->revealable()
                        ->default(fn () => Str::random(16))
                        ->required(),
                ])
                ->action(function (array $data): void {
                    $result = $this->getAgent()->postgresCreateUser(
                        $this->getUsername(),
                        $data['db_user'],
                        $data['password']
                    );

                    if ($result['success'] ?? false) {
                        Notification::make()->title(__('User created'))->success()->send();
                        $this->loadUsers();
                        $this->resetTable();

                        return;
                    }

                    Notification::make()->title(__('Creation failed'))->body($result['error'] ?? '')->danger()->send();
                }),
        ];
    }

    protected function formatBytes(int $bytes, int $precision = 2): string
    {
        $units = ['B', 'KB', 'MB', 'GB', 'TB'];
        $bytes = max($bytes, 0);
        $pow = floor(($bytes ? log($bytes) : 0) / log(1024));
        $pow = min($pow, count($units) - 1);
        $bytes /= pow(1024, $pow);

        return round($bytes, $precision).' '.$units[$pow];
    }
}
