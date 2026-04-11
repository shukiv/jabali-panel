<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\MysqlCredential;
use App\Services\Agent\InteractsWithAgent;
use App\Support\Formatter;
use App\Support\PasswordGenerator;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\CheckboxList;
use Filament\Forms\Components\FileUpload;
use Filament\Forms\Components\Placeholder;
use Filament\Forms\Components\Radio;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Infolists\Components\TextEntry;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Crypt;
use Livewire\Attributes\Url;

class Databases extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithAgent;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-circle-stack';

    protected static ?int $navigationSort = 8;

    public static function getNavigationLabel(): string
    {
        return __('Databases');
    }

    protected string $view = 'filament.jabali.pages.databases';

    #[Url(as: 'tab')]
    public string $activeTab = 'mysql';

    public string $pgSubTab = 'databases';

    public array $databases = [];

    public array $users = [];

    public array $userGrants = [];

    public ?string $selectedUser = null;

    public string $credDatabase = '';

    public string $credUser = '';

    public string $credPassword = '';

    public array $pgDatabases = [];

    public array $pgUsers = [];

    public bool $postgresAvailable = false;

    public function getTitle(): string|Htmlable
    {
        return __('Databases');
    }

    public function getUsername(): string
    {
        return Auth::user()->username;
    }

    public function mount(): void
    {
        $this->postgresAvailable = $this->checkPostgresAvailable();
        $this->activeTab = $this->normalizeTab($this->activeTab);
        $this->ensureAdminUserExists();
        $this->loadData();
    }

    protected function checkPostgresAvailable(): bool
    {
        if ((bool) config('jabali.demo')) {
            return true;
        }

        return (bool) \App\Models\DnsSetting::get('postgres_enabled', false);
    }

    public function updatedActiveTab(): void
    {
        $this->activeTab = $this->normalizeTab($this->activeTab);
        $this->loadData();
        $this->resetTable();
    }

    protected function normalizeTab(string $tab): string
    {
        return in_array($tab, ['mysql', 'postgresql'], true) ? $tab : 'mysql';
    }

    protected function getForms(): array
    {
        return ['databasesForm'];
    }

    public function databasesForm(Schema $schema): Schema
    {
        return $schema->schema([
            Tabs::make(__('Database Engines'))
                ->contained()
                ->livewireProperty('activeTab')
                ->tabs([
                    'mysql' => Tab::make(__('MySQL'))
                        ->icon('heroicon-o-circle-stack')
                        ->schema([
                            View::make('filament.jabali.pages.databases-mysql-tab'),
                        ]),
                    'postgresql' => Tab::make(__('PostgreSQL'))
                        ->icon('heroicon-o-server-stack')
                        ->schema([
                            $this->postgresAvailable
                                ? View::make('filament.jabali.pages.databases-postgresql-tab')
                                : View::make('filament.jabali.pages.databases-postgresql-unavailable'),
                        ]),
                ]),
        ]);
    }

    /**
     * Ensure the master admin MySQL user exists for this user.
     * This user has access to all {username}_* databases and is used for phpMyAdmin SSO.
     */
    protected function ensureAdminUserExists(): void
    {
        $adminUsername = $this->getUsername().'_admin';

        // Check if we already have stored credentials for the admin user
        $credential = MysqlCredential::where('user_id', Auth::id())
            ->where('mysql_username', $adminUsername)
            ->first();

        if ($credential) {
            return; // Admin user credentials exist
        }

        // Generate secure password
        $password = $this->generateSecurePassword(24);

        try {
            // Try to create the admin user
            $this->agent()->mysqlCreateUser($this->getUsername(), $adminUsername, $password);
        } catch (Exception $e) {
            // User might already exist, try to change password instead
            try {
                $this->agent()->mysqlChangePassword($this->getUsername(), $adminUsername, $password);
            } catch (Exception $e2) {
                // Can't create or update user
                return;
            }
        }

        try {
            // Grant privileges on all user's databases (using wildcard pattern)
            $wildcardDb = $this->getUsername().'_%';
            $this->agent()->mysqlGrantPrivileges($this->getUsername(), $adminUsername, $wildcardDb, ['ALL']);

            // Store credentials
            MysqlCredential::updateOrCreate(
                [
                    'user_id' => Auth::id(),
                    'mysql_username' => $adminUsername,
                ],
                [
                    'mysql_password_encrypted' => Crypt::encryptString($password),
                ]
            );
        } catch (Exception $e) {
            // Grant failed
        }
    }

    public function loadData(): void
    {
        if ($this->activeTab === 'postgresql') {
            $this->loadPgDatabases();
            $this->loadPgUsers();

            return;
        }

        try {
            $result = $this->agent()->mysqlListDatabases($this->getUsername());
            $this->databases = $result['databases'] ?? [];
        } catch (Exception $e) {
            $this->databases = [];
            Notification::make()
                ->title(__('Error loading databases'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }

        try {
            $result = $this->agent()->mysqlListUsers($this->getUsername());
            $this->users = $result['users'] ?? [];

            // Filter out the master admin user from display
            $this->users = array_filter($this->users, function ($user) {
                return $user['user'] !== $this->getUsername().'_admin';
            });

            $this->userGrants = [];
            foreach ($this->users as $user) {
                $this->loadUserGrants($user['user'], $user['host']);
            }
        } catch (Exception $e) {
            $this->users = [];
        }
    }

    protected function loadUserGrants(string $user, string $host): void
    {
        try {
            $result = $this->agent()->mysqlGetPrivileges($this->getUsername(), $user, $host);
            $this->userGrants["$user@$host"] = $result['parsed'] ?? [];
        } catch (Exception $e) {
            $this->userGrants["$user@$host"] = [];
        }
    }

    public function getUserGrantsForDisplay(string $user, string $host): array
    {
        return $this->userGrants["$user@$host"] ?? [];
    }

    // ── PostgreSQL data loading ─────────────────────────────────────

    protected function loadPgDatabases(): void
    {
        try {
            $result = $this->agent()->postgresListDatabases($this->getUsername());
            $this->pgDatabases = $result['databases'] ?? [];
        } catch (Exception) {
            $this->pgDatabases = [];
        }
    }

    protected function loadPgUsers(): void
    {
        try {
            $result = $this->agent()->postgresListUsers($this->getUsername());
            $this->pgUsers = $result['users'] ?? [];
        } catch (Exception) {
            $this->pgUsers = [];
        }
    }

    protected function getPgUserOptions(): array
    {
        if (empty($this->pgUsers)) {
            $this->loadPgUsers();
        }

        $options = [];
        foreach ($this->pgUsers as $user) {
            $options[$user['username']] = $user['username'];
        }

        return $options;
    }

    public function setPgSubTab(string $tab): void
    {
        $this->pgSubTab = in_array($tab, ['databases', 'users'], true) ? $tab : 'databases';

        if ($this->pgSubTab === 'users') {
            $this->loadPgUsers();
        } else {
            $this->loadPgDatabases();
        }

        $this->resetTable();
    }

    // ── Table ───────────────────────────────────────────────────────

    public function table(Table $table): Table
    {
        return match ($this->activeTab) {
            'postgresql' => $this->postgresqlTable($table),
            default => $this->mysqlTable($table),
        };
    }

    protected function mysqlTable(Table $table): Table
    {
        return $table
            ->records(fn () => $this->databases)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Database Name'))
                    ->icon('heroicon-o-circle-stack')
                    ->iconColor('warning')
                    ->weight('medium')
                    ->searchable(),
                TextColumn::make('size_human')
                    ->label(__('Size'))
                    ->badge()
                    ->color(fn (array $record): string => match (true) {
                        ($record['size_bytes'] ?? 0) > 1073741824 => 'danger', // > 1GB
                        ($record['size_bytes'] ?? 0) > 104857600 => 'warning', // > 100MB
                        default => 'gray',
                    })
                    ->sortable(query: fn ($query, $direction) => $query),
            ])
            ->recordActions([
                Action::make('phpMyAdmin')
                    ->label(__('phpMyAdmin'))
                    ->icon('heroicon-o-circle-stack')
                    ->color('info')
                    ->url(fn (array $record): string => route('phpmyadmin.redirect', ['database' => $record['name']]))
                    ->openUrlInNewTab(),
                Action::make('backup')
                    ->label(__('Backup'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('success')
                    ->modalHeading(__('Backup Database'))
                    ->modalDescription(fn (array $record): string => __("Create a backup of ':database'", ['database' => $record['name']]))
                    ->modalIcon('heroicon-o-arrow-down-tray')
                    ->modalIconColor('success')
                    ->modalSubmitActionLabel(__('Create Backup'))
                    ->form([
                        Radio::make('format')
                            ->label(__('Backup Format'))
                            ->options([
                                'gz' => __('Gzip (.sql.gz) - Recommended'),
                                'zip' => __('Zip (.zip)'),
                                'none' => __('Plain SQL (.sql)'),
                            ])
                            ->default('gz')
                            ->required(),
                    ])
                    ->action(function (array $record, array $data): void {
                        $this->backupDatabase($record['name'], $data['format'] ?? 'gz');
                    }),
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-up-tray')
                    ->color('warning')
                    ->requiresConfirmation()
                    ->modalHeading(__('Restore Database'))
                    ->modalDescription(fn (array $record): string => __("This will overwrite all data in ':database'. Make sure you have a backup.", ['database' => $record['name']]))
                    ->modalIcon('heroicon-o-exclamation-triangle')
                    ->modalIconColor('warning')
                    ->modalSubmitActionLabel(__('Restore'))
                    ->form([
                        FileUpload::make('sql_file')
                            ->label(__('Backup File'))
                            ->required()
                            ->acceptedFileTypes(['text/plain', 'text/sql', 'application/sql', 'application/x-sql', 'application/gzip', 'application/x-gzip', 'application/zip', 'application/x-zip-compressed', 'application/octet-stream'])
                            ->maxSize(512000) // 500MB (compressed files can be larger)
                            ->disk('local')
                            ->directory('temp/sql-uploads')
                            ->helperText(__('Supported formats: .sql, .sql.gz, .gz, .zip (max 500MB)')),
                    ])
                    ->action(function (array $record, array $data): void {
                        $this->restoreDatabase($record['name'], $data['sql_file']);
                    }),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Database'))
                    ->modalDescription(fn (array $record): string => __("Delete ':database'? All data will be permanently lost.", ['database' => $record['name']]))
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->modalSubmitActionLabel(__('Delete Database'))
                    ->action(function (array $record): void {
                        try {
                            $this->agent()->mysqlDeleteDatabase($this->getUsername(), $record['name']);
                            Notification::make()->title(__('Database deleted'))->success()->send();
                            $this->loadData();
                            $this->resetTable();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
            ])
            ->emptyStateHeading(__('No databases yet'))
            ->emptyStateDescription(__('Click "Quick Setup" or "New Database" to create one'))
            ->emptyStateIcon('heroicon-o-circle-stack')
            ->striped();
    }

    protected function postgresqlTable(Table $table): Table
    {
        if ($this->pgSubTab === 'users') {
            return $table
                ->records(fn () => $this->pgUsers)
                ->columns([
                    TextColumn::make('username')
                        ->label(__('User'))
                        ->searchable(),
                ])
                ->recordActions([
                    Action::make('changePassword')
                        ->label(__('Password'))
                        ->icon('heroicon-o-key')
                        ->color('gray')
                        ->form([
                            TextInput::make('password')
                                ->label(__('New Password'))
                                ->password()
                                ->required()
                                ->minLength(8)
                                ->suffixActions([
                                    Action::make('generate')
                                        ->icon('heroicon-o-arrow-path')
                                        ->action(fn ($set) => $set('password', \Illuminate\Support\Str::random(16))),
                                ]),
                        ])
                        ->action(function (array $record, array $data): void {
                            try {
                                $result = $this->agent()->postgresChangePassword(
                                    $this->getUsername(),
                                    $record['username'],
                                    $data['password']
                                );
                                if ($result['success'] ?? false) {
                                    Notification::make()->title(__('Password changed'))->success()->send();
                                } else {
                                    throw new Exception($result['error'] ?? 'Failed');
                                }
                            } catch (Exception $e) {
                                Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                            }
                        }),
                    Action::make('delete')
                        ->label(__('Delete'))
                        ->icon('heroicon-o-trash')
                        ->color('danger')
                        ->requiresConfirmation()
                        ->action(function (array $record): void {
                            $result = $this->agent()->postgresDeleteUser($this->getUsername(), $record['username']);
                            if ($result['success'] ?? false) {
                                Notification::make()->title(__('User deleted'))->success()->send();
                                $this->loadPgUsers();
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
            ->records(fn () => $this->pgDatabases)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Database'))
                    ->icon('heroicon-o-circle-stack')
                    ->iconColor('info')
                    ->weight('medium')
                    ->searchable(),
                TextColumn::make('size_bytes')
                    ->label(__('Size'))
                    ->badge()
                    ->formatStateUsing(fn ($state) => Formatter::bytes((int) $state))
                    ->color(fn (array $record): string => match (true) {
                        ($record['size_bytes'] ?? 0) > 1073741824 => 'danger',
                        ($record['size_bytes'] ?? 0) > 104857600 => 'warning',
                        default => 'gray',
                    }),
            ])
            ->recordActions([
                Action::make('privileges')
                    ->label(__('Privileges'))
                    ->icon('heroicon-o-shield-check')
                    ->color('info')
                    ->form(function (array $record): array {
                        $userOptions = $this->getPgUserOptions();
                        if (empty($userOptions)) {
                            return [
                                Placeholder::make('no_users')
                                    ->content(__('Create a PostgreSQL user first.')),
                            ];
                        }

                        return [
                            Select::make('db_user')
                                ->label(__('User'))
                                ->options($userOptions)
                                ->required()
                                ->live(),
                            CheckboxList::make('privileges')
                                ->label(__('Privileges'))
                                ->options([
                                    'ALL' => __('All Privileges'),
                                    'CREATE' => __('Create'),
                                    'CONNECT' => __('Connect'),
                                    'TEMPORARY' => __('Temporary'),
                                ])
                                ->default(['ALL']),
                        ];
                    })
                    ->action(function (array $record, array $data): void {
                        try {
                            $result = $this->agent()->postgresSetPrivileges(
                                $this->getUsername(),
                                $record['name'],
                                $data['db_user'],
                                $data['privileges'] ?? []
                            );
                            if ($result['success'] ?? false) {
                                Notification::make()->title(__('Privileges updated'))->success()->send();
                            } else {
                                throw new Exception($result['error'] ?? 'Failed');
                            }
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
                Action::make('backup')
                    ->label(__('Backup'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('success')
                    ->action(function (array $record): void {
                        try {
                            $timestamp = now()->format('Y-m-d_His');
                            $outputPath = "/home/{$this->getUsername()}/backups/{$record['name']}_{$timestamp}.sql.gz";
                            $result = $this->agent()->postgresExportDatabase(
                                $this->getUsername(),
                                $record['name'],
                                $outputPath
                            );
                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title(__('Backup created'))
                                    ->body(__('Saved to :path (:size)', [
                                        'path' => basename($outputPath),
                                        'size' => Formatter::bytes($result['size'] ?? 0),
                                    ]))
                                    ->success()
                                    ->send();
                            } else {
                                throw new Exception($result['error'] ?? 'Export failed');
                            }
                        } catch (Exception $e) {
                            Notification::make()->title(__('Backup failed'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-up-tray')
                    ->color('warning')
                    ->requiresConfirmation()
                    ->modalHeading(__('Restore PostgreSQL Database'))
                    ->modalDescription(fn (array $record): string => __("This will overwrite all data in ':database'. Make sure you have a backup.", ['database' => $record['name']]))
                    ->modalIcon('heroicon-o-exclamation-triangle')
                    ->modalIconColor('warning')
                    ->form([
                        FileUpload::make('sql_file')
                            ->label(__('Backup File'))
                            ->required()
                            ->acceptedFileTypes(['text/plain', 'text/sql', 'application/sql', 'application/gzip', 'application/x-gzip', 'application/octet-stream'])
                            ->maxSize(512000)
                            ->disk('local')
                            ->directory('temp/sql-uploads')
                            ->helperText(__('Supported: .sql, .sql.gz (max 500MB)')),
                    ])
                    ->action(function (array $record, array $data): void {
                        try {
                            $file = $data['sql_file'];
                            $storagePath = storage_path('app/private/temp/sql-uploads/'.$file);
                            if (! file_exists($storagePath)) {
                                $storagePath = storage_path('app/temp/sql-uploads/'.$file);
                            }

                            $username = $this->getUsername();
                            $importDir = "/home/{$username}/tmp";
                            $importPath = "{$importDir}/{$file}";

                            $this->agent()->send('file.ensure_dir', ['path' => $importDir, 'username' => $username]);
                            $this->agent()->send('file.write', [
                                'path' => $importPath,
                                'content' => base64_encode(file_get_contents($storagePath)),
                                'encoding' => 'base64',
                                'username' => $username,
                            ]);

                            $result = $this->agent()->postgresImportDatabase($username, $record['name'], $importPath);

                            $this->agent()->send('file.delete', ['path' => $importPath, 'username' => $username]);
                            @unlink($storagePath);

                            if ($result['success'] ?? false) {
                                Notification::make()->title(__('Database restored'))->success()->send();
                            } else {
                                throw new Exception($result['error'] ?? 'Import failed');
                            }
                        } catch (Exception $e) {
                            Notification::make()->title(__('Restore failed'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Database'))
                    ->modalDescription(fn (array $record): string => __("Delete ':database'? All data will be permanently lost.", ['database' => $record['name']]))
                    ->action(function (array $record): void {
                        $result = $this->agent()->postgresDeleteDatabase($this->getUsername(), $record['name']);
                        if ($result['success'] ?? false) {
                            Notification::make()->title(__('Database deleted'))->success()->send();
                            $this->loadPgDatabases();
                            $this->resetTable();

                            return;
                        }

                        Notification::make()->title(__('Deletion failed'))->body($result['error'] ?? '')->danger()->send();
                    }),
            ])
            ->emptyStateHeading(__('No PostgreSQL databases'))
            ->emptyStateDescription(__('Create a PostgreSQL database to get started'));
    }

    public function getTableRecordKey(Model|array $record): string
    {
        if (is_array($record)) {
            return $record['name'] ?? $record['username'] ?? '';
        }

        return $record->getKey();
    }

    public function generateSecurePassword(int $length = 16): string
    {
        return PasswordGenerator::generate($length);
    }

    /**
     * Generate phpMyAdmin URL for a specific database
     */
    public function getPhpMyAdminUrl(string $database): ?string
    {
        try {
            $adminUsername = $this->getUsername().'_admin';

            // Get the master admin user credential
            $credential = MysqlCredential::where('user_id', Auth::id())
                ->where('mysql_username', $adminUsername)
                ->first();

            // Fallback to any credential if admin not found
            if (! $credential) {
                $credential = MysqlCredential::where('user_id', Auth::id())->first();
            }

            if (! $credential) {
                // Try to create the admin user if it doesn't exist
                $this->ensureAdminUserExists();
                $credential = MysqlCredential::where('user_id', Auth::id())
                    ->where('mysql_username', $adminUsername)
                    ->first();
            }

            if (! $credential) {
                return null;
            }

            // Generate token
            $token = bin2hex(random_bytes(32));

            // Store token data in cache for 5 minutes
            Cache::put('phpmyadmin_token_'.$token, [
                'username' => $credential->mysql_username,
                'password' => Crypt::decryptString($credential->mysql_password_encrypted),
                'database' => $database,
            ], now()->addMinutes(5));

            // phpMyAdmin is served by nginx on port 443, not FrankenPHP panel port
            $scheme = request()->getScheme();
            $host = request()->getHost();

            return "{$scheme}://{$host}/phpmyadmin/jabali-signon.php?token={$token}&db=".urlencode($database);

        } catch (Exception $e) {
            return null;
        }
    }

    protected function getHeaderActions(): array
    {
        return [
            $this->quickSetupAction(),
            $this->createDatabaseAction(),
            $this->createUserAction(),
            $this->showCredentialsAction(),
            $this->pgCreateDatabaseAction(),
            $this->pgCreateUserAction(),
        ];
    }

    protected function showCredentialsAction(): Action
    {
        return Action::make('showCredentials')
            ->label(__('Credentials'))
            ->hidden()
            ->modalHeading(__('Database Credentials'))
            ->modalDescription(__('Save these credentials! The password won\'t be shown again.'))
            ->modalIcon('heroicon-o-check-circle')
            ->modalIconColor('success')
            ->modalSubmitAction(false)
            ->modalCancelActionLabel(__('Done'))
            ->infolist([
                Section::make(__('Database'))
                    ->hidden(fn () => empty($this->credDatabase))
                    ->schema([
                        TextEntry::make('database')
                            ->hiddenLabel()
                            ->state(fn () => $this->credDatabase)
                            ->copyable()
                            ->fontFamily('mono'),
                    ]),
                Section::make(__('Username'))
                    ->hidden(fn () => empty($this->credUser))
                    ->schema([
                        TextEntry::make('username')
                            ->hiddenLabel()
                            ->state(fn () => $this->credUser)
                            ->copyable()
                            ->fontFamily('mono'),
                    ]),
                Section::make(__('Password'))
                    ->schema([
                        TextEntry::make('password')
                            ->hiddenLabel()
                            ->state(fn () => $this->credPassword)
                            ->copyable()
                            ->fontFamily('mono'),
                    ]),
            ]);
    }

    protected function quickSetupAction(): Action
    {
        return Action::make('quickSetup')
            ->label(__('Quick Setup'))
            ->icon('heroicon-o-bolt')
            ->color('warning')
            ->visible(fn () => $this->activeTab === 'mysql')
            ->modalHeading(__('Quick Database Setup'))
            ->modalDescription(__('Create a database and user with full access in one step'))
            ->modalIcon('heroicon-o-bolt')
            ->modalIconColor('warning')
            ->modalSubmitActionLabel(__('Create Database & User'))
            ->form([
                TextInput::make('name')
                    ->label(__('Database & User Name'))
                    ->required()
                    ->alphaNum()
                    ->maxLength(20)
                    ->prefix($this->getUsername().'_')
                    ->helperText(__('This name will be used for both the database and user')),
            ])
            ->action(function (array $data): void {
                $limit = Auth::user()?->hostingPackage?->databases_limit;
                if ($limit && count($this->databases) >= $limit) {
                    Notification::make()
                        ->title(__('Database limit reached'))
                        ->body(__('Your hosting package allows up to :limit databases.', ['limit' => $limit]))
                        ->warning()
                        ->send();

                    return;
                }

                $name = $this->getUsername().'_'.$data['name'];
                $password = $this->generateSecurePassword();

                try {
                    // Create database
                    $this->agent()->mysqlCreateDatabase($this->getUsername(), $name);

                    // Create user with same name
                    $result = $this->agent()->mysqlCreateUser($this->getUsername(), $name, $password);

                    // Grant all privileges
                    $this->agent()->mysqlGrantPrivileges($this->getUsername(), $name, $name, ['ALL']);

                    // Store credentials
                    MysqlCredential::updateOrCreate(
                        [
                            'user_id' => Auth::id(),
                            'mysql_username' => $name,
                        ],
                        [
                            'mysql_password_encrypted' => Crypt::encryptString($password),
                        ]
                    );

                    $this->credDatabase = $name;
                    $this->credUser = $name;
                    $this->credPassword = $password;

                    Notification::make()->title(__('Database & User Created!'))->success()->send();
                    $this->loadData();
                    $this->resetTable();
                    $this->dispatch('refresh-database-users');

                    $this->mountAction('showCredentials');
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    protected function createDatabaseAction(): Action
    {
        return Action::make('createDatabase')
            ->label(__('New Database'))
            ->icon('heroicon-o-plus-circle')
            ->color('success')
            ->visible(fn () => $this->activeTab === 'mysql')
            ->modalHeading(__('Create New Database'))
            ->modalDescription(__('Create a new MySQL database'))
            ->modalIcon('heroicon-o-circle-stack')
            ->modalIconColor('success')
            ->modalSubmitActionLabel(__('Create Database'))
            ->form([
                TextInput::make('name')
                    ->label(__('Database Name'))
                    ->required()
                    ->alphaNum()
                    ->maxLength(32)
                    ->prefix($this->getUsername().'_')
                    ->helperText(__('Only alphanumeric characters allowed')),
            ])
            ->action(function (array $data): void {
                $limit = Auth::user()?->hostingPackage?->databases_limit;
                if ($limit && count($this->databases) >= $limit) {
                    Notification::make()
                        ->title(__('Database limit reached'))
                        ->body(__('Your hosting package allows up to :limit databases.', ['limit' => $limit]))
                        ->warning()
                        ->send();

                    return;
                }

                $name = $this->getUsername().'_'.$data['name'];
                try {
                    $this->agent()->mysqlCreateDatabase($this->getUsername(), $name);
                    Notification::make()->title(__('Database created'))->success()->send();
                    $this->loadData();
                    $this->resetTable();
                    $this->dispatch('refresh-database-users');
                } catch (Exception $e) {
                    Notification::make()->title(__('Error creating database'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    protected function createUserAction(): Action
    {
        return Action::make('createUser')
            ->label(__('New User'))
            ->icon('heroicon-o-user-plus')
            ->color('primary')
            ->visible(fn () => $this->activeTab === 'mysql')
            ->modalHeading(__('Create New Database User'))
            ->modalDescription(__('Create a new MySQL user for database access'))
            ->modalIcon('heroicon-o-user-plus')
            ->modalIconColor('primary')
            ->modalSubmitActionLabel(__('Create User'))
            ->form([
                TextInput::make('username')
                    ->label(__('Username'))
                    ->required()
                    ->alphaNum()
                    ->maxLength(20)
                    ->prefix($this->getUsername().'_')
                    ->helperText(__('Only alphanumeric characters allowed')),
                TextInput::make('password')
                    ->label(__('Password'))
                    ->password()
                    ->revealable()
                    ->required()
                    ->minLength(8)
                    ->rules([
                        'regex:/[a-z]/',      // lowercase
                        'regex:/[A-Z]/',      // uppercase
                        'regex:/[0-9]/',      // number
                    ])
                    ->default(fn () => $this->generateSecurePassword())
                    ->suffixActions([
                        Action::make('generatePassword')
                            ->icon('heroicon-o-arrow-path')
                            ->tooltip(__('Generate secure password'))
                            ->action(fn ($set) => $set('password', $this->generateSecurePassword())),
                        Action::make('copyPassword')
                            ->icon('heroicon-o-clipboard-document')
                            ->tooltip(__('Copy to clipboard'))
                            ->action(function ($state, $livewire) {
                                if ($state) {
                                    $livewire->js('navigator.clipboard.writeText('.json_encode($state, JSON_HEX_TAG).')');
                                    Notification::make()
                                        ->title(__('Copied to clipboard'))
                                        ->success()
                                        ->duration(2000)
                                        ->send();
                                }
                            }),
                    ])
                    ->helperText(__('Minimum 8 characters with uppercase, lowercase, and numbers')),
            ])
            ->action(function (array $data): void {
                try {
                    $prefixedUsername = $this->getUsername().'_'.$data['username'];
                    $result = $this->agent()->mysqlCreateUser(
                        $this->getUsername(),
                        $prefixedUsername,
                        $data['password']
                    );

                    // Store credentials
                    MysqlCredential::updateOrCreate(
                        [
                            'user_id' => Auth::id(),
                            'mysql_username' => $result['db_user'],
                        ],
                        [
                            'mysql_password_encrypted' => Crypt::encryptString($data['password']),
                        ]
                    );

                    $this->credDatabase = '';
                    $this->credUser = $result['db_user'];
                    $this->credPassword = $data['password'];

                    Notification::make()->title(__('User created'))->success()->send();
                    $this->loadData();
                    $this->resetTable();
                    $this->dispatch('refresh-database-users');

                    $this->mountAction('showCredentials');
                } catch (Exception $e) {
                    Notification::make()->title(__('Error creating user'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    // ── PostgreSQL header actions ───────────────────────────────────

    protected function pgCreateDatabaseAction(): Action
    {
        return Action::make('pgCreateDatabase')
            ->label(__('Create Database'))
            ->icon('heroicon-o-circle-stack')
            ->color('primary')
            ->visible(fn () => $this->activeTab === 'postgresql' && $this->pgSubTab === 'databases')
            ->form([
                TextInput::make('database')
                    ->label(__('Database Name'))
                    ->required()
                    ->alphaNum()
                    ->maxLength(32)
                    ->prefix($this->getUsername().'_')
                    ->helperText(__('Only alphanumeric characters allowed')),
                Select::make('owner')
                    ->label(__('Owner User'))
                    ->options($this->getPgUserOptions())
                    ->required(),
            ])
            ->action(function (array $data): void {
                $name = $this->getUsername().'_'.$data['database'];
                $result = $this->agent()->postgresCreateDatabase(
                    $this->getUsername(),
                    $name,
                    $data['owner']
                );

                if ($result['success'] ?? false) {
                    Notification::make()->title(__('Database created'))->success()->send();
                    $this->loadPgDatabases();
                    $this->resetTable();

                    return;
                }

                Notification::make()->title(__('Creation failed'))->body($result['error'] ?? '')->danger()->send();
            });
    }

    protected function pgCreateUserAction(): Action
    {
        return Action::make('pgCreateUser')
            ->label(__('Create User'))
            ->icon('heroicon-o-user-plus')
            ->color('primary')
            ->visible(fn () => $this->activeTab === 'postgresql' && $this->pgSubTab === 'users')
            ->form([
                TextInput::make('db_user')
                    ->label(__('Username'))
                    ->required()
                    ->alphaNum()
                    ->maxLength(20)
                    ->prefix($this->getUsername().'_')
                    ->helperText(__('Only alphanumeric characters allowed')),
                TextInput::make('password')
                    ->label(__('Password'))
                    ->password()
                    ->revealable()
                    ->required()
                    ->minLength(8)
                    ->rules([
                        'regex:/[a-z]/',
                        'regex:/[A-Z]/',
                        'regex:/[0-9]/',
                    ])
                    ->default(fn () => $this->generateSecurePassword())
                    ->helperText(__('Minimum 8 characters with uppercase, lowercase, and numbers')),
            ])
            ->action(function (array $data): void {
                $username = $this->getUsername().'_'.$data['db_user'];
                $result = $this->agent()->postgresCreateUser(
                    $this->getUsername(),
                    $username,
                    $data['password']
                );

                if ($result['success'] ?? false) {
                    $this->credDatabase = '';
                    $this->credUser = $username;
                    $this->credPassword = $data['password'];

                    Notification::make()->title(__('User created'))->success()->send();
                    $this->loadPgUsers();
                    $this->resetTable();

                    $this->mountAction('showCredentials');

                    return;
                }

                Notification::make()->title(__('Creation failed'))->body($result['error'] ?? '')->danger()->send();
            });
    }

    // ── MySQL user management (unchanged) ───────────────────────────

    public function deleteUser(string $user, string $host): void
    {
        $this->selectedUser = "$user@$host";
        $this->mountAction('deleteUserAction');
    }

    public function deleteUserAction(): Action
    {
        return Action::make('deleteUserAction')
            ->requiresConfirmation()
            ->modalHeading(__('Delete User'))
            ->modalDescription(fn () => __("Delete user ':user'? This action cannot be undone.", ['user' => $this->selectedUser]))
            ->modalIcon('heroicon-o-trash')
            ->modalIconColor('danger')
            ->modalSubmitActionLabel(__('Delete User'))
            ->color('danger')
            ->action(function (): void {
                [$user, $host] = explode('@', $this->selectedUser);
                try {
                    $this->agent()->mysqlDeleteUser($this->getUsername(), $user, $host);

                    // Delete stored credentials
                    MysqlCredential::where('user_id', Auth::id())
                        ->where('mysql_username', $user)
                        ->delete();

                    Notification::make()->title(__('User deleted'))->success()->send();
                    $this->loadData();
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    public function changePassword(string $user, string $host): void
    {
        $this->selectedUser = "$user@$host";
        $this->mountAction('changePasswordAction');
    }

    public function changePasswordAction(): Action
    {
        return Action::make('changePasswordAction')
            ->modalHeading(__('Change Password'))
            ->modalDescription(fn () => $this->selectedUser)
            ->modalIcon('heroicon-o-key')
            ->modalIconColor('warning')
            ->modalSubmitActionLabel(__('Change Password'))
            ->form([
                TextInput::make('password')
                    ->label(__('New Password'))
                    ->password()
                    ->revealable()
                    ->required()
                    ->minLength(8)
                    ->rules([
                        'regex:/[a-z]/',      // lowercase
                        'regex:/[A-Z]/',      // uppercase
                        'regex:/[0-9]/',      // number
                    ])
                    ->default(fn () => $this->generateSecurePassword())
                    ->suffixActions([
                        Action::make('generatePassword')
                            ->icon('heroicon-o-arrow-path')
                            ->tooltip(__('Generate secure password'))
                            ->action(fn ($set) => $set('password', $this->generateSecurePassword())),
                        Action::make('copyPassword')
                            ->icon('heroicon-o-clipboard-document')
                            ->tooltip(__('Copy to clipboard'))
                            ->action(function ($state, $livewire) {
                                if ($state) {
                                    $livewire->js('navigator.clipboard.writeText('.json_encode($state, JSON_HEX_TAG).')');
                                    Notification::make()
                                        ->title(__('Copied to clipboard'))
                                        ->success()
                                        ->duration(2000)
                                        ->send();
                                }
                            }),
                    ])
                    ->helperText(__('Minimum 8 characters with uppercase, lowercase, and numbers')),
            ])
            ->action(function (array $data): void {
                [$user, $host] = explode('@', $this->selectedUser);
                try {
                    $this->agent()->mysqlChangePassword($this->getUsername(), $user, $data['password'], $host);

                    // Update stored MySQL credentials
                    MysqlCredential::updateOrCreate(
                        [
                            'user_id' => Auth::id(),
                            'mysql_username' => $user,
                        ],
                        [
                            'mysql_password_encrypted' => Crypt::encryptString($data['password']),
                        ]
                    );

                    $this->credDatabase = '';
                    $this->credUser = $user;
                    $this->credPassword = $data['password'];

                    Notification::make()->title(__('Password changed'))->success()->send();

                    $this->mountAction('showCredentials');
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    public function addPrivileges(string $user, string $host): void
    {
        $this->selectedUser = "$user@$host";
        $this->mountAction('addPrivilegesAction');
    }

    public function addPrivilegesAction(): Action
    {
        $dbOptions = [];
        foreach ($this->databases as $db) {
            $dbOptions[$db['name']] = $db['name'];
        }

        return Action::make('addPrivilegesAction')
            ->modalHeading(__('Add Database Access'))
            ->modalDescription(fn () => __('Grant privileges to :user', ['user' => $this->selectedUser]))
            ->modalIcon('heroicon-o-shield-check')
            ->modalIconColor('success')
            ->modalWidth('lg')
            ->modalSubmitActionLabel(__('Grant Access'))
            ->form([
                Select::make('database')
                    ->label(__('Database'))
                    ->options($dbOptions)
                    ->required()
                    ->searchable()
                    ->placeholder(__('Select a database...'))
                    ->helperText(__('Choose which database to grant access to'))
                    ->live(),

                Radio::make('privilege_type')
                    ->label(__('Privilege Type'))
                    ->options([
                        'all' => __('ALL PRIVILEGES (full access)'),
                        'specific' => __('Specific privileges'),
                    ])
                    ->default('all')
                    ->required()
                    ->live()
                    ->disabled(fn (callable $get): bool => empty($get('database')))
                    ->helperText(__('ALL PRIVILEGES grants complete control over the database')),

                CheckboxList::make('specific_privileges')
                    ->label(__('Select Privileges'))
                    ->options([
                        'SELECT' => __('SELECT - Read data'),
                        'INSERT' => __('INSERT - Add new data'),
                        'UPDATE' => __('UPDATE - Modify existing data'),
                        'DELETE' => __('DELETE - Remove data'),
                        'CREATE' => __('CREATE - Create tables'),
                        'DROP' => __('DROP - Delete tables'),
                        'INDEX' => __('INDEX - Manage indexes'),
                        'ALTER' => __('ALTER - Modify table structure'),
                    ])
                    ->columns(2)
                    ->visible(fn (callable $get): bool => $get('privilege_type') === 'specific' && ! empty($get('database'))),
            ])
            ->action(function (array $data): void {
                [$user, $host] = explode('@', $this->selectedUser);

                $privilegeType = $data['privilege_type'] ?? 'all';

                if ($privilegeType === 'specific' && ! empty($data['specific_privileges'])) {
                    $privs = $data['specific_privileges'];
                } else {
                    $privs = ['ALL'];
                }

                try {
                    $this->agent()->mysqlGrantPrivileges($this->getUsername(), $user, $data['database'], $privs, $host);

                    $privDisplay = ($privilegeType === 'all') ? __('ALL PRIVILEGES') : implode(', ', $privs);
                    Notification::make()
                        ->title(__('Privileges granted'))
                        ->body(__('Granted :privileges on :database', ['privileges' => $privDisplay, 'database' => $data['database']]))
                        ->success()
                        ->send();
                    $this->loadData();
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                }
            });
    }

    public function revokePrivileges(string $user, string $host, string $database): void
    {
        try {
            $this->agent()->mysqlRevokePrivileges($this->getUsername(), $user, $database, $host);
            Notification::make()->title(__('Access revoked'))->success()->send();
            $this->loadData();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function backupDatabase(string $database, string $compress = 'gz'): void
    {
        try {
            // Determine file extension based on compression type
            $extension = match ($compress) {
                'gz' => '.sql.gz',
                'zip' => '.zip',
                default => '.sql',
            };

            $filename = $database.'_'.date('Y-m-d_His').$extension;
            $outputPath = '/home/'.$this->getUsername().'/backups/'.$filename;

            $result = $this->agent()->mysqlExportDatabase($this->getUsername(), $database, $outputPath, $compress);

            if ($result['success'] ?? false) {
                // Store the backup path for download
                $this->lastBackupPath = 'backups/'.$filename;

                Notification::make()
                    ->title(__('Backup created'))
                    ->body(__('File: backups/:filename', ['filename' => $filename]))
                    ->success()
                    ->actions([
                        \Filament\Actions\Action::make('download')
                            ->label(__('Download'))
                            ->icon('heroicon-o-arrow-down-tray')
                            ->button()
                            ->dispatch('download-backup', ['path' => 'backups/'.$filename]),
                        \Filament\Actions\Action::make('view_files')
                            ->label(__('Open in Files'))
                            ->icon('heroicon-o-folder-open')
                            ->url(route('filament.jabali.pages.files').'?path=backups')
                            ->openUrlInNewTab(),
                    ])
                    ->persistent()
                    ->send();
            } else {
                throw new Exception($result['error'] ?? __('Unknown error'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Backup failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public string $lastBackupPath = '';

    #[\Livewire\Attributes\On('download-backup')]
    public function onDownloadBackup(string $path): void
    {
        $this->downloadBackup($path);
    }

    public function downloadBackup(string $path): void
    {
        try {
            $result = $this->agent()->fileRead($this->getUsername(), $path);
            $this->dispatch('download-backup-file',
                content: $result['content'],
                filename: basename($path)
            );
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Download failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function restoreDatabase(string $database, $uploadedFile): void
    {
        try {
            // Handle array or string file path from FileUpload
            $relativePath = is_array($uploadedFile) ? ($uploadedFile[0] ?? '') : $uploadedFile;

            if (empty($relativePath)) {
                throw new Exception(__('No file uploaded'));
            }

            // Get the full path using Storage facade (handles Laravel 11+ private storage)
            $storage = \Illuminate\Support\Facades\Storage::disk('local');

            if ($storage->exists($relativePath)) {
                $filePath = $storage->path($relativePath);
            } else {
                // Try direct path
                $filePath = storage_path('app/'.$relativePath);
                if (! file_exists($filePath)) {
                    $filePath = storage_path('app/private/'.$relativePath);
                }
            }

            if (! file_exists($filePath)) {
                throw new Exception(__('Uploaded file not found'));
            }

            // Validate file extension - allow .sql, .sql.gz, .gz, .zip
            $lowerPath = strtolower($relativePath);
            $validExtensions = ['.sql', '.sql.gz', '.gz', '.zip'];
            $isValid = false;
            foreach ($validExtensions as $ext) {
                if (str_ends_with($lowerPath, $ext)) {
                    $isValid = true;
                    break;
                }
            }

            if (! $isValid) {
                $storage->delete($relativePath);
                throw new Exception(__('Invalid file type. Supported: .sql, .sql.gz, .gz, .zip'));
            }

            $result = $this->agent()->mysqlImportDatabase($this->getUsername(), $database, $filePath);

            // Clean up the uploaded file
            $storage->delete($relativePath);

            if ($result['success'] ?? false) {
                Notification::make()
                    ->title(__('Database restored'))
                    ->body(__('Successfully restored :database', ['database' => $database]))
                    ->success()
                    ->send();

                $this->loadData();
                $this->resetTable();
            } else {
                throw new Exception($result['error'] ?? __('Unknown error'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Restore failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }
}
