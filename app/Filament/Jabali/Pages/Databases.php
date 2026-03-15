<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\MysqlCredential;
use App\Services\Agent\AgentClient;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\CheckboxList;
use Filament\Forms\Components\FileUpload;
use Filament\Forms\Components\Radio;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Infolists\Components\TextEntry;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Section;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Crypt;

class Databases extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-circle-stack';

    protected static ?int $navigationSort = 7;

    public static function getNavigationGroup(): ?string
    {
        return __('Databases');
    }

    public static function getNavigationLabel(): string
    {
        return __('MySQL');
    }

    protected string $view = 'filament.jabali.pages.databases';

    public array $databases = [];

    public array $users = [];

    public array $userGrants = [];

    public ?string $selectedUser = null;

    public ?string $selectedDatabase = null;

    public string $credDatabase = '';

    public string $credUser = '';

    public string $credPassword = '';

    protected ?AgentClient $agent = null;

    public function getTitle(): string|Htmlable
    {
        return __('MySQL Databases');
    }

    public function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient;
        }

        return $this->agent;
    }

    public function getUsername(): string
    {
        return Auth::user()->username;
    }

    public function mount(): void
    {
        $this->ensureAdminUserExists();
        $this->loadData();
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
            $this->getAgent()->mysqlCreateUser($this->getUsername(), $adminUsername, $password);
        } catch (Exception $e) {
            // User might already exist, try to change password instead
            try {
                $this->getAgent()->mysqlChangePassword($this->getUsername(), $adminUsername, $password);
            } catch (Exception $e2) {
                // Can't create or update user
                return;
            }
        }

        try {
            // Grant privileges on all user's databases (using wildcard pattern)
            $wildcardDb = $this->getUsername().'_%';
            $this->getAgent()->mysqlGrantPrivileges($this->getUsername(), $adminUsername, $wildcardDb, ['ALL']);

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
        try {
            $result = $this->getAgent()->mysqlListDatabases($this->getUsername());
            $this->databases = $result['databases'] ?? [];
        } catch (Exception $e) {
            $this->databases = [];
            Notification::make()
                ->title(__('Error loading databases'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }

        try {
            $result = $this->getAgent()->mysqlListUsers($this->getUsername());
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
            $result = $this->getAgent()->mysqlGetPrivileges($this->getUsername(), $user, $host);
            $this->userGrants["$user@$host"] = $result['parsed'] ?? [];
        } catch (Exception $e) {
            $this->userGrants["$user@$host"] = [];
        }
    }

    public function getUserGrantsForDisplay(string $user, string $host): array
    {
        return $this->userGrants["$user@$host"] ?? [];
    }

    public function table(Table $table): Table
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
                    ->action(function (array $record): void {
                        $url = $this->getPhpMyAdminUrl($record['name']);
                        if ($url) {
                            $this->js("window.open('".addslashes($url)."', '_blank')");
                        } else {
                            Notification::make()
                                ->title(__('Cannot open phpMyAdmin'))
                                ->body(__('No database credentials found. Create a user first.'))
                                ->warning()
                                ->send();
                        }
                    }),
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
                            $this->getAgent()->mysqlDeleteDatabase($this->getUsername(), $record['name']);
                            Notification::make()->title(__('Database deleted'))->success()->send();
                            $this->loadData();
                            $this->resetTable();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
                        }
                    }),
            ])
            ->emptyStateHeading(__('No databases yet'))
            ->emptyStateDescription(__('Click "Quick Setup" or "New Database" to create one'))
            ->emptyStateIcon('heroicon-o-circle-stack')
            ->striped();
    }

    public function getTableRecordKey(Model|array $record): string
    {
        return is_array($record) ? $record['name'] : $record->getKey();
    }

    public function generateSecurePassword(int $length = 16): string
    {
        $lowercase = 'abcdefghijklmnopqrstuvwxyz';
        $uppercase = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ';
        $numbers = '0123456789';
        $special = '!@#$%^&*';

        // Ensure at least one of each required type
        $password = $lowercase[random_int(0, strlen($lowercase) - 1)]
            .$uppercase[random_int(0, strlen($uppercase) - 1)]
            .$numbers[random_int(0, strlen($numbers) - 1)]
            .$special[random_int(0, strlen($special) - 1)];

        // Fill the rest with random characters from all types
        $allChars = $lowercase.$uppercase.$numbers.$special;
        for ($i = strlen($password); $i < $length; $i++) {
            $password .= $allChars[random_int(0, strlen($allChars) - 1)];
        }

        // Shuffle the password to randomize position of required characters
        return str_shuffle($password);
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

            return request()->getSchemeAndHttpHost().'/phpmyadmin/jabali-signon.php?token='.$token.'&db='.urlencode($database);

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
                    $this->getAgent()->mysqlCreateDatabase($this->getUsername(), $name);

                    // Create user with same name
                    $result = $this->getAgent()->mysqlCreateUser($this->getUsername(), $name, $password);

                    // Grant all privileges
                    $this->getAgent()->mysqlGrantPrivileges($this->getUsername(), $name, $name, ['ALL']);

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
                    Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
                }
            });
    }

    protected function createDatabaseAction(): Action
    {
        return Action::make('createDatabase')
            ->label(__('New Database'))
            ->icon('heroicon-o-plus-circle')
            ->color('success')
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
                    $this->getAgent()->mysqlCreateDatabase($this->getUsername(), $name);
                    Notification::make()->title(__('Database created'))->success()->send();
                    $this->loadData();
                    $this->resetTable();
                    $this->dispatch('refresh-database-users');
                } catch (Exception $e) {
                    Notification::make()->title(__('Error creating database'))->body($e->getMessage())->danger()->send();
                }
            });
    }

    protected function createUserAction(): Action
    {
        return Action::make('createUser')
            ->label(__('New User'))
            ->icon('heroicon-o-user-plus')
            ->color('primary')
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
                                    $escaped = addslashes($state);
                                    $livewire->js("navigator.clipboard.writeText('{$escaped}')");
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
                    $result = $this->getAgent()->mysqlCreateUser(
                        $this->getUsername(),
                        $data['username'],
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
                    Notification::make()->title(__('Error creating user'))->body($e->getMessage())->danger()->send();
                }
            });
    }

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
                    $this->getAgent()->mysqlDeleteUser($this->getUsername(), $user, $host);

                    // Delete stored credentials
                    MysqlCredential::where('user_id', Auth::id())
                        ->where('mysql_username', $user)
                        ->delete();

                    Notification::make()->title(__('User deleted'))->success()->send();
                    $this->loadData();
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
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
                                    $escaped = addslashes($state);
                                    $livewire->js("navigator.clipboard.writeText('{$escaped}')");
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
                    $this->getAgent()->mysqlChangePassword($this->getUsername(), $user, $data['password'], $host);

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
                    Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
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
                    $this->getAgent()->mysqlGrantPrivileges($this->getUsername(), $user, $data['database'], $privs, $host);

                    $privDisplay = ($privilegeType === 'all') ? __('ALL PRIVILEGES') : implode(', ', $privs);
                    Notification::make()
                        ->title(__('Privileges granted'))
                        ->body(__('Granted :privileges on :database', ['privileges' => $privDisplay, 'database' => $data['database']]))
                        ->success()
                        ->send();
                    $this->loadData();
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
                }
            });
    }

    public function revokePrivileges(string $user, string $host, string $database): void
    {
        try {
            $this->getAgent()->mysqlRevokePrivileges($this->getUsername(), $user, $database, $host);
            Notification::make()->title(__('Access revoked'))->success()->send();
            $this->loadData();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
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

            $result = $this->getAgent()->mysqlExportDatabase($this->getUsername(), $database, $outputPath, $compress);

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
                ->body($e->getMessage())
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
            $result = $this->getAgent()->fileRead($this->getUsername(), $path);
            $this->dispatch('download-backup-file',
                content: $result['content'],
                filename: basename($path)
            );
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Download failed'))
                ->body($e->getMessage())
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

            $result = $this->getAgent()->mysqlImportDatabase($this->getUsername(), $database, $filePath);

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
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }
}
