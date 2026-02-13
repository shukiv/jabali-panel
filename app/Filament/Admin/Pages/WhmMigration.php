<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Jobs\RunWhmMigrationBatch;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Migration\WhmApiService;
use App\Services\Migration\WhmMigrationStatusStore;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Checkbox;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Infolists\Concerns\InteractsWithInfolists;
use Filament\Infolists\Contracts\HasInfolists;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Actions as FormActions;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\View;
use Filament\Schemas\Components\Wizard;
use Filament\Schemas\Components\Wizard\Step;
use Filament\Schemas\Schema;
use Filament\Tables;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Facades\Log;
use Livewire\Attributes\On;
use Livewire\Attributes\Url;

class WhmMigration extends Page implements HasActions, HasForms, HasInfolists, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithInfolists;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-server-stack';

    protected static ?string $navigationLabel = null;

    protected static bool $shouldRegisterNavigation = false;

    public static function getNavigationLabel(): string
    {
        return __('WHM Migration');
    }

    protected static ?int $navigationSort = 22;

    public static function getNavigationGroup(): string
    {
        return __('Server');
    }

    protected string $view = 'filament.admin.pages.whm-migration';

    // Track wizard step via URL
    #[Url(as: 'whm-step')]
    public ?string $wizardStep = null;

    // Track step completion
    public bool $step1Complete = false;

    public bool $step2Complete = false;

    // Connection form (Step 1)
    public ?string $hostname = null;

    public ?string $whmUsername = 'root';

    public ?string $apiToken = null;

    public int $port = 2087;

    public bool $useSSL = true;

    // Connection status
    public bool $isConnected = false;

    public array $serverInfo = [];

    // Account list from WHM (Step 2)
    public array $accounts = [];

    public array $selectedAccounts = [];

    // Migration configuration (Step 3)
    public bool $createLinuxUsers = true;

    public bool $sendWelcomeEmail = false;

    public bool $restoreFiles = true;

    public bool $restoreDatabases = true;

    public bool $restoreEmails = true;

    public bool $restoreSsl = true;

    // Per-account configuration
    public array $accountConfig = []; // user => ['jabali_username' => '', 'email' => '', 'password' => '']

    // Migration status (Step 4)
    public bool $isMigrating = false;

    public array $migrationStatus = []; // user => ['status' => 'pending|processing|completed|error', 'log' => [], 'progress' => 0]

    public int $currentAccountIndex = 0;

    public int $totalAccounts = 0;

    public array $statusLog = [];

    protected ?AgentClient $agent = null;

    protected ?WhmApiService $whm = null;

    public function getTitle(): string|Htmlable
    {
        return __('WHM Migration');
    }

    public function getSubheading(): ?string
    {
        return __('Migrate multiple cPanel accounts from a WHM server to Jabali');
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('startOver')
                ->label(__('Start Over'))
                ->icon('heroicon-o-arrow-path')
                ->color('gray')
                ->requiresConfirmation()
                ->modalHeading(__('Start Over'))
                ->modalDescription(__('This will reset all migration data. Are you sure?'))
                ->action('resetMigration'),
        ];
    }

    public function mount(): void
    {
        $this->restoreFromSession();
        $this->loadMigrationStatusFromStore();

        // Initialize accountConfig if we have selectedAccounts but no config
        // This handles direct URL navigation to step 3
        if (! empty($this->selectedAccounts) && empty($this->accountConfig)) {
            $this->initializeAccountConfig();
            $this->saveToSession();
        }
    }

    public function updatedHostname(): void
    {
        $this->resetConnection();
    }

    public function updatedWhmUsername(): void
    {
        $this->resetConnection();
    }

    public function updatedApiToken(): void
    {
        $this->resetConnection();
    }

    public function updatedPort(): void
    {
        $this->resetConnection();
    }

    public function updatedUseSSL(): void
    {
        $this->resetConnection();
    }

    protected function resetConnection(): void
    {
        $this->whm = null;
        $this->isConnected = false;
        $this->serverInfo = [];
        $this->accounts = [];
        $this->selectedAccounts = [];
    }

    public function getAgent(): AgentClient
    {
        return $this->agent ??= new AgentClient;
    }

    public function getMigrationCacheKey(): string
    {
        $userId = auth()->id() ?? 0;

        return 'whm_migration_status_'.$userId;
    }

    protected function getMigrationStatusStore(): WhmMigrationStatusStore
    {
        return new WhmMigrationStatusStore($this->getMigrationCacheKey());
    }

    protected function loadMigrationStatusFromStore(): void
    {
        $state = $this->getMigrationStatusStore()->get();
        if ($state === []) {
            return;
        }

        $this->migrationStatus = $state['migrationStatus'] ?? $this->migrationStatus;
        $this->isMigrating = (bool) ($state['isMigrating'] ?? $this->isMigrating);
        $this->selectedAccounts = $state['selectedAccounts'] ?? $this->selectedAccounts;
    }

    protected function getWhm(): ?WhmApiService
    {
        if (! $this->hostname || ! $this->whmUsername || ! $this->apiToken) {
            $this->restoreFromSession();
        }

        if (! $this->hostname || ! $this->whmUsername || ! $this->apiToken) {
            return null;
        }

        return $this->whm ??= new WhmApiService(
            trim($this->hostname),
            trim($this->whmUsername),
            trim($this->apiToken),
            $this->port,
            $this->useSSL
        );
    }

    protected function saveToSession(): void
    {
        session()->put('whm_migration.hostname', $this->hostname);
        session()->put('whm_migration.username', $this->whmUsername);
        session()->put('whm_migration.token', $this->apiToken);
        session()->put('whm_migration.port', $this->port);
        session()->put('whm_migration.useSSL', $this->useSSL);
        session()->put('whm_migration.isConnected', $this->isConnected);
        session()->put('whm_migration.serverInfo', $this->serverInfo);
        session()->put('whm_migration.accounts', $this->accounts);
        session()->put('whm_migration.selectedAccounts', $this->selectedAccounts);
        session()->put('whm_migration.accountConfig', $this->accountConfig);
        session()->put('whm_migration.step1Complete', $this->step1Complete);
        session()->put('whm_migration.step2Complete', $this->step2Complete);
        session()->put('whm_migration.migrationStatus', $this->migrationStatus);
        session()->put('whm_migration.isMigrating', $this->isMigrating);
        session()->save();
    }

    protected function restoreFromSession(): void
    {
        if (session()->has('whm_migration.hostname')) {
            $this->hostname = session('whm_migration.hostname');
            $this->whmUsername = session('whm_migration.username', 'root');
            $this->apiToken = session('whm_migration.token');
            $this->port = session('whm_migration.port', 2087);
            $this->useSSL = session('whm_migration.useSSL', true);
            $this->isConnected = session('whm_migration.isConnected', false);
            $this->serverInfo = session('whm_migration.serverInfo', []);
            $this->accounts = session('whm_migration.accounts', []);
            $this->selectedAccounts = session('whm_migration.selectedAccounts', []);
            $this->accountConfig = session('whm_migration.accountConfig', []);
            $this->step1Complete = session('whm_migration.step1Complete', false);
            $this->step2Complete = session('whm_migration.step2Complete', false);
            $this->migrationStatus = session('whm_migration.migrationStatus', []);
        }
    }

    protected function clearSession(): void
    {
        session()->forget([
            'whm_migration.hostname',
            'whm_migration.username',
            'whm_migration.token',
            'whm_migration.port',
            'whm_migration.useSSL',
            'whm_migration.isConnected',
            'whm_migration.serverInfo',
            'whm_migration.accounts',
            'whm_migration.selectedAccounts',
            'whm_migration.accountConfig',
            'whm_migration.step1Complete',
            'whm_migration.step2Complete',
            'whm_migration.migrationStatus',
        ]);
    }

    protected function getBackupDestPath(): string
    {
        return '/var/backups/jabali/whm-migrations';
    }

    protected function getJabaliPublicIp(): string
    {
        $ip = trim(shell_exec('curl -s ifconfig.me 2>/dev/null') ?? '');

        if (empty($ip)) {
            $ip = gethostbyname(gethostname());
        }

        return $ip;
    }

    protected function getSshKeyName(): string
    {
        return 'jabali-system-key';
    }

    protected function getForms(): array
    {
        return ['migrationForm'];
    }

    public function migrationForm(Schema $schema): Schema
    {
        return $schema->schema([
            Wizard::make([
                $this->getConnectStep(),
                $this->getSelectAccountsStep(),
                $this->getConfigureStep(),
                $this->getMigrateStep(),
            ])
                ->nextAction(
                    fn (Action $action) => $action
                        ->disabled(fn () => $this->isNextStepDisabled())
                        ->hidden(fn () => $this->isNextButtonHidden())
                )
                ->persistStepInQueryString('whm-step'),
        ]);
    }

    protected function isNextButtonHidden(): bool
    {
        return $this->isMigrating;
    }

    protected function isNextStepDisabled(): bool
    {
        // Step 1: Must be connected
        if (! $this->step1Complete) {
            return ! $this->isConnected;
        }

        // Step 2: Must have selected accounts
        if (! $this->step2Complete) {
            return empty($this->selectedAccounts);
        }

        return false;
    }

    protected function getConnectStep(): Step
    {
        return Step::make(__('Connect'))
            ->id('connect')
            ->icon('heroicon-o-link')
            ->description(__('Connect to WHM server'))
            ->schema([
                Section::make(__('WHM Server Credentials'))
                    ->description(__('Enter the WHM (WebHost Manager) server connection details'))
                    ->icon('heroicon-o-server-stack')
                    ->schema([
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            TextInput::make('hostname')
                                ->label(__('WHM Hostname'))
                                ->placeholder(__('whm.example.com'))
                                ->required()
                                ->helperText(__('Your WHM server hostname or IP address')),
                            TextInput::make('port')
                                ->label(__('Port'))
                                ->numeric()
                                ->default(2087)
                                ->required()
                                ->helperText(__('Usually 2087 for SSL or 2086 without')),
                        ]),
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            TextInput::make('whmUsername')
                                ->label(__('WHM Username'))
                                ->default('root')
                                ->required()
                                ->helperText(__('Usually "root" for WHM')),
                            TextInput::make('apiToken')
                                ->label(__('API Token'))
                                ->password()
                                ->required()
                                ->revealable()
                                ->helperText(__('Generate from WHM → Manage API Tokens')),
                        ]),
                        Checkbox::make('useSSL')
                            ->label(__('Use SSL (HTTPS)'))
                            ->default(true)
                            ->helperText(__('Recommended. Disable only if your WHM does not support SSL.')),
                    ]),

                FormActions::make([
                    Action::make('testConnection')
                        ->label(fn () => $this->isConnected ? __('Connected') : __('Test Connection'))
                        ->icon(fn () => $this->isConnected ? 'heroicon-o-check-circle' : 'heroicon-o-signal')
                        ->color(fn () => $this->isConnected ? 'success' : 'primary')
                        ->action('testConnection'),
                ])->alignEnd(),

                Section::make(__('Connection Successful'))
                    ->icon('heroicon-o-check-circle')
                    ->iconColor('success')
                    ->visible(fn () => $this->isConnected)
                    ->schema([
                        Grid::make(['default' => 2, 'sm' => 4])->schema([
                            Text::make(fn () => __('Host: :host', ['host' => $this->hostname ?? '-'])),
                            Text::make(fn () => __('Version: :version', ['version' => $this->serverInfo['version'] ?? '-'])),
                            Text::make(fn () => __('Accounts: :count', ['count' => $this->serverInfo['account_count'] ?? 0])),
                            Text::make(fn () => __('User: :user', ['user' => $this->whmUsername ?? '-'])),
                        ]),
                        Text::make(__('You can proceed to select accounts for migration.'))->color('success'),
                    ]),
            ])
            ->afterValidation(function () {
                if (! $this->isConnected) {
                    Notification::make()
                        ->title(__('Connection required'))
                        ->body(__('Please test the connection first'))
                        ->danger()
                        ->send();
                    throw new Exception(__('Please test the connection first'));
                }

                $this->step1Complete = true;
                $this->saveToSession();
            });
    }

    protected function getSelectAccountsStep(): Step
    {
        return Step::make(__('Select Accounts'))
            ->id('accounts')
            ->icon('heroicon-o-users')
            ->description(__('Choose which accounts to migrate'))
            ->schema([
                Section::make(__('cPanel Accounts'))
                    ->description(fn () => ! empty($this->selectedAccounts)
                        ? __(':selected of :count accounts selected', ['selected' => count($this->selectedAccounts), 'count' => count($this->accounts)])
                        : __(':count accounts found on server', ['count' => count($this->accounts)]))
                    ->icon('heroicon-o-user-group')
                    ->headerActions([
                        Action::make('refreshAccounts')
                            ->label(__('Refresh'))
                            ->icon('heroicon-o-arrow-path')
                            ->color('gray')
                            ->action('refreshAccounts'),
                        Action::make('selectAll')
                            ->label(__('Select All'))
                            ->icon('heroicon-o-check')
                            ->color('primary')
                            ->action('selectAllAccounts')
                            ->visible(fn () => count($this->selectedAccounts) < count($this->accounts)),
                        Action::make('deselectAll')
                            ->label(__('Deselect All'))
                            ->icon('heroicon-o-x-mark')
                            ->color('gray')
                            ->action('deselectAllAccounts')
                            ->visible(fn () => count($this->selectedAccounts) > 0),
                    ])
                    ->schema([
                        View::make('filament.admin.pages.whm-accounts-table'),
                    ]),
            ])
            ->afterValidation(function () {
                if (empty($this->selectedAccounts)) {
                    Notification::make()
                        ->title(__('No accounts selected'))
                        ->body(__('Please select at least one account to migrate'))
                        ->danger()
                        ->send();
                    throw new Exception(__('Please select at least one account'));
                }

                // Initialize account configuration for selected accounts
                $this->initializeAccountConfig();

                $this->step2Complete = true;
                $this->saveToSession();

                // Notify the config table widget to refresh
                $this->dispatch('whm-config-updated');
            });
    }

    protected function getConfigureStep(): Step
    {
        return Step::make(__('Configure'))
            ->id('configure')
            ->icon('heroicon-o-cog')
            ->description(__('Configure migration options'))
            ->schema([
                Section::make(__('Global Options'))
                    ->description(__('These options apply to all selected accounts'))
                    ->icon('heroicon-o-adjustments-horizontal')
                    ->schema([
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            Checkbox::make('createLinuxUsers')
                                ->label(__('Create Linux system users'))
                                ->helperText(__('Creates Linux user accounts on this server'))
                                ->default(true),
                            Checkbox::make('sendWelcomeEmail')
                                ->label(__('Send welcome email to users'))
                                ->helperText(__('Notify users after their account is migrated'))
                                ->default(false),
                        ]),
                    ]),

                Section::make(__('What to Restore'))
                    ->description(__('Select which parts of each account to restore'))
                    ->icon('heroicon-o-check-circle')
                    ->schema([
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            Checkbox::make('restoreFiles')
                                ->label(__('Website Files'))
                                ->helperText(__('Restore all website files'))
                                ->default(true),
                            Checkbox::make('restoreDatabases')
                                ->label(__('MySQL Databases'))
                                ->helperText(__('Restore databases with all data'))
                                ->default(true),
                            Checkbox::make('restoreEmails')
                                ->label(__('Email Mailboxes'))
                                ->helperText(__('Restore email accounts and messages'))
                                ->default(true),
                            Checkbox::make('restoreSsl')
                                ->label(__('SSL Certificates'))
                                ->helperText(__('Restore SSL certificates for domains'))
                                ->default(true),
                        ]),
                    ]),

                Section::make(__('Account Mappings'))
                    ->description(fn () => __(':count accounts to migrate', ['count' => count($this->selectedAccounts)]))
                    ->icon('heroicon-o-arrow-right')
                    ->schema([
                        View::make('filament.admin.pages.whm-account-config-table'),
                    ]),
            ]);
    }

    protected function getMigrateStep(): Step
    {
        return Step::make(__('Migrate'))
            ->id('migrate')
            ->icon('heroicon-o-play')
            ->description(__('Migration progress'))
            ->schema([
                FormActions::make([
                    Action::make('startMigration')
                        ->label(__('Start Migration'))
                        ->icon('heroicon-o-play')
                        ->color('success')
                        ->visible(fn () => ! $this->isMigrating && empty($this->migrationStatus))
                        ->requiresConfirmation()
                        ->modalHeading(__('Start Migration'))
                        ->modalDescription(__('This will migrate :count account(s). Existing data may be overwritten. Continue?', ['count' => count($this->selectedAccounts)]))
                        ->action('startMigration'),

                    Action::make('newMigration')
                        ->label(__('New Migration'))
                        ->icon('heroicon-o-plus')
                        ->color('primary')
                        ->visible(fn () => ! $this->isMigrating && ! empty($this->migrationStatus))
                        ->action('resetMigration'),
                ])->alignEnd(),

                Section::make(__('Overall Progress'))
                    ->icon($this->isMigrating ? 'heroicon-o-arrow-path' : ($this->getMigrationCompletedCount() === count($this->selectedAccounts) && ! empty($this->selectedAccounts) ? 'heroicon-o-check-circle' : 'heroicon-o-clock'))
                    ->iconColor($this->isMigrating ? 'warning' : ($this->getMigrationCompletedCount() === count($this->selectedAccounts) && ! empty($this->selectedAccounts) ? 'success' : 'gray'))
                    ->schema($this->getOverallProgressSchema())
                    ->extraAttributes($this->isMigrating ? ['wire:poll.5s' => 'pollMigrationStatus'] : []),

                Section::make(__('Account Status'))
                    ->icon('heroicon-o-queue-list')
                    ->schema([
                        View::make('filament.admin.pages.whm-migration-status-table'),
                    ])
                    ->visible(fn () => ! empty($this->migrationStatus)),
            ]);
    }

    protected function getOverallProgressSchema(): array
    {
        if (empty($this->selectedAccounts)) {
            return [
                Text::make(__('No accounts selected for migration.'))->color('gray'),
            ];
        }

        if (empty($this->migrationStatus)) {
            return [
                Text::make(__('Click "Start Migration" to begin.'))->color('gray'),
                Text::make(__(':count account(s) will be migrated.', ['count' => count($this->selectedAccounts)]))->color('primary'),
            ];
        }

        $total = count($this->selectedAccounts);
        $completed = $this->getMigrationCompletedCount();
        $errors = $this->getMigrationErrorCount();
        $percent = $total > 0 ? round(($completed / $total) * 100) : 0;

        return [
            Grid::make(['default' => 2, 'sm' => 4])->schema([
                Section::make((string) $total)
                    ->description(__('Total'))
                    ->icon('heroicon-o-users')
                    ->iconColor('primary')
                    ->compact(),
                Section::make((string) $completed)
                    ->description(__('Completed'))
                    ->icon('heroicon-o-check-circle')
                    ->iconColor('success')
                    ->compact(),
                Section::make((string) $errors)
                    ->description(__('Errors'))
                    ->icon('heroicon-o-x-circle')
                    ->iconColor($errors > 0 ? 'danger' : 'gray')
                    ->compact(),
                Section::make("{$percent}%")
                    ->description(__('Progress'))
                    ->icon('heroicon-o-chart-pie')
                    ->iconColor('info')
                    ->compact(),
            ]),
        ];
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getAccountConfigRecords())
            ->columns([
                Tables\Columns\IconColumn::make('exists')
                    ->label(__('Status'))
                    ->boolean()
                    ->trueIcon('heroicon-o-exclamation-triangle')
                    ->falseIcon('heroicon-o-user-plus')
                    ->trueColor('warning')
                    ->falseColor('success')
                    ->tooltip(fn (array $record): string => $record['exists']
                        ? __('User exists - will restore to existing account')
                        : __('New user will be created')),
                Tables\Columns\TextColumn::make('user')
                    ->label(__('Username'))
                    ->weight('bold')
                    ->searchable(),
                Tables\Columns\TextColumn::make('domain')
                    ->label(__('Domain'))
                    ->searchable(),
                Tables\Columns\TextColumn::make('email')
                    ->label(__('Email'))
                    ->icon('heroicon-o-envelope'),
                Tables\Columns\TextColumn::make('diskused')
                    ->label(__('Size')),
            ])
            ->striped()
            ->paginated([10, 25, 50])
            ->defaultPaginationPageOption(10)
            ->emptyStateHeading(__('No accounts selected'))
            ->emptyStateDescription(__('Go back to Step 2 and select accounts to migrate'))
            ->emptyStateIcon('heroicon-o-user-group');
    }

    protected function getAccountConfigRecords(): array
    {
        $records = [];

        foreach ($this->selectedAccounts as $cpanelUser) {
            $account = collect($this->accounts)->firstWhere('user', $cpanelUser);
            $config = $this->accountConfig[$cpanelUser] ?? [];

            $domain = $account['domain'] ?? '';
            $email = $config['email'] ?? $account['email'] ?? "{$cpanelUser}@{$domain}";

            $existingUser = User::where('username', $cpanelUser)->first();

            $records[] = [
                'user' => $cpanelUser,
                'domain' => $domain,
                'email' => $email,
                'exists' => $existingUser !== null,
                'diskused' => $account['diskused'] ?? '',
            ];
        }

        return $records;
    }

    public function testConnection(): void
    {
        if (empty($this->hostname) || empty($this->whmUsername) || empty($this->apiToken)) {
            Notification::make()
                ->title(__('Missing credentials'))
                ->body(__('Please fill in all required fields'))
                ->danger()
                ->send();

            return;
        }

        try {
            $whm = $this->getWhm();
            $result = $whm->testConnection();

            if ($result['success']) {
                // Get account list
                $accountsResult = $whm->listAccounts();
                if ($accountsResult['success']) {
                    $this->accounts = $accountsResult['accounts'];
                }

                $this->isConnected = true;
                $this->serverInfo = [
                    'version' => $result['version'] ?? 'Unknown',
                    'account_count' => count($this->accounts),
                ];

                $this->saveToSession();
                $this->dispatch('whm-accounts-updated');

                Notification::make()
                    ->title(__('Connection successful'))
                    ->body(__('Connected to WHM server. Found :count cPanel accounts.', [
                        'count' => count($this->accounts),
                    ]))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['message'] ?? __('Connection failed'));
            }
        } catch (Exception $e) {
            $this->isConnected = false;
            Log::error('WHM connection failed', ['error' => $e->getMessage()]);

            Notification::make()
                ->title(__('Connection failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function refreshAccounts(): void
    {
        $whm = $this->getWhm();
        if (! $whm) {
            return;
        }

        try {
            $result = $whm->listAccounts();
            if ($result['success']) {
                $this->accounts = $result['accounts'];
                $this->serverInfo['account_count'] = count($this->accounts);
                $this->saveToSession();
                $this->dispatch('whm-accounts-updated');

                Notification::make()
                    ->title(__('Accounts refreshed'))
                    ->body(__('Found :count cPanel accounts.', ['count' => count($this->accounts)]))
                    ->success()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Refresh failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function toggleAccountSelection(string $user): void
    {
        if (in_array($user, $this->selectedAccounts)) {
            $this->selectedAccounts = array_values(array_diff($this->selectedAccounts, [$user]));
        } else {
            $this->selectedAccounts[] = $user;
        }

        $this->saveToSession();
    }

    #[On('whm-selection-updated')]
    public function handleSelectionUpdated(array $selectedAccounts): void
    {
        $this->selectedAccounts = $selectedAccounts;
        $this->saveToSession();
    }

    public function selectAllAccounts(): void
    {
        $this->selectedAccounts = array_map(fn ($a) => $a['user'], $this->accounts);
        $this->saveToSession();
        $this->dispatch('whm-accounts-updated');
    }

    public function deselectAllAccounts(): void
    {
        $this->selectedAccounts = [];
        $this->saveToSession();
        $this->dispatch('whm-accounts-updated');
    }

    protected function initializeAccountConfig(): void
    {
        foreach ($this->selectedAccounts as $cpanelUser) {
            if (isset($this->accountConfig[$cpanelUser])) {
                continue;
            }

            $account = collect($this->accounts)->firstWhere('user', $cpanelUser);
            $domain = $account['domain'] ?? '';
            $email = $account['email'] ?? "{$cpanelUser}@{$domain}";

            $this->accountConfig[$cpanelUser] = [
                'jabali_username' => $cpanelUser,
                'email' => $email,
                'password' => bin2hex(random_bytes(8)), // Generate random password
            ];
        }
    }

    public function startMigration(): void
    {
        if (empty($this->selectedAccounts)) {
            return;
        }

        $this->isMigrating = true;
        $this->currentAccountIndex = 0;
        $this->totalAccounts = count($this->selectedAccounts);

        $store = $this->getMigrationStatusStore();
        $state = $store->initialize($this->selectedAccounts);
        $this->migrationStatus = $state['migrationStatus'] ?? [];

        $this->saveToSession();

        RunWhmMigrationBatch::dispatch(
            cacheKey: $this->getMigrationCacheKey(),
            hostname: $this->hostname ?? '',
            whmUsername: $this->whmUsername ?? 'root',
            apiToken: $this->apiToken ?? '',
            port: $this->port,
            useSSL: $this->useSSL,
            accounts: $this->accounts,
            selectedAccounts: $this->selectedAccounts,
            restoreFiles: $this->restoreFiles,
            restoreDatabases: $this->restoreDatabases,
            restoreEmails: $this->restoreEmails,
            restoreSsl: $this->restoreSsl,
            createLinuxUsers: $this->createLinuxUsers,
        );
    }

    #[\Livewire\Attributes\On('process-next-account')]
    public function handleProcessNextAccount(): void
    {
        $this->processNextAccount();
    }

    public function processNextAccount(): void
    {
        if ($this->currentAccountIndex >= $this->totalAccounts) {
            $this->isMigrating = false;
            $this->saveToSession();

            $completed = $this->getMigrationCompletedCount();
            $errors = $this->getMigrationErrorCount();

            Notification::make()
                ->title(__('Migration complete'))
                ->body(__(':completed of :total accounts migrated successfully. :errors errors.', [
                    'completed' => $completed,
                    'total' => $this->totalAccounts,
                    'errors' => $errors,
                ]))
                ->success()
                ->send();

            $this->resetMigration();

            return;
        }

        $cpanelUser = $this->selectedAccounts[$this->currentAccountIndex];
        $this->migrateAccount($cpanelUser);
    }

    protected function migrateAccount(string $cpanelUser): void
    {
        $this->updateAccountStatus($cpanelUser, 'processing', __('Starting migration...'));

        try {
            $whm = $this->getWhm();
            if (! $whm) {
                throw new Exception(__('WHM connection lost'));
            }

            $account = collect($this->accounts)->firstWhere('user', $cpanelUser);
            $domain = $account['domain'] ?? '';
            $email = $account['email'] ?? "{$cpanelUser}@{$domain}";

            // Step 1: Create or get Jabali user
            $user = $this->createOrGetUser($cpanelUser, $email);
            if (! $user) {
                throw new Exception(__('Failed to create user'));
            }

            $this->addAccountLog($cpanelUser, __('User ready: :username', ['username' => $user->username]), 'success');

            // Step 2: Set up SSH key for SCP transfer (same as cPanel migration)
            $this->updateAccountStatus($cpanelUser, 'backup_creating', __('Setting up backup transfer...'));

            $keyName = $this->getSshKeyName();
            $destPath = $this->getBackupDestPath();

            // Ensure destination directory exists
            if (! is_dir($destPath)) {
                mkdir($destPath, 0755, true);
            }

            // Ensure Jabali SSH key exists (via agent which runs as root)
            $this->getAgent()->send('jabali_ssh.ensure_exists', []);

            // Get Jabali's public key and add to authorized_keys
            $publicKeyResult = $this->getAgent()->send('jabali_ssh.get_public_key', []);
            if (! ($publicKeyResult['success'] ?? false) || ! ($publicKeyResult['exists'] ?? false)) {
                throw new Exception(__('Failed to get Jabali public key'));
            }
            $publicKey = $publicKeyResult['public_key'] ?? null;

            // Add to authorized_keys so cPanel can SCP to us
            $this->getAgent()->send('jabali_ssh.add_to_authorized_keys', [
                'public_key' => $publicKey,
                'comment' => 'whm-migration-'.$cpanelUser,
            ]);

            // Read Jabali's SSH private key via agent (runs as root)
            $privateKeyResult = $this->getAgent()->send('jabali_ssh.get_private_key', []);
            if (! ($privateKeyResult['success'] ?? false) || ! ($privateKeyResult['exists'] ?? false)) {
                throw new Exception(__('Failed to read Jabali private key'));
            }

            $privateKey = $privateKeyResult['private_key'] ?? null;
            if (empty($privateKey)) {
                throw new Exception(__('Private key is empty'));
            }

            $this->addAccountLog($cpanelUser, __('Importing SSH key to cPanel...'), 'pending');

            // Import SSH key to the cPanel user via WHM
            $importResult = $whm->importSshPrivateKey($cpanelUser, $keyName, $privateKey);
            if (! ($importResult['success'] ?? false)) {
                throw new Exception($importResult['message'] ?? __('Failed to import SSH key'));
            }

            // Use actual key name if it was different (key already existed under different name)
            $actualKeyName = $importResult['actual_key_name'] ?? $keyName;
            $this->addAccountLog($cpanelUser, __('SSH key imported'), 'success');

            // Authorize the key
            $authResult = $whm->authorizeSshKey($cpanelUser, $actualKeyName);
            if (! ($authResult['success'] ?? false)) {
                $this->addAccountLog($cpanelUser, __('SSH key authorization skipped'), 'info');
            } else {
                $this->addAccountLog($cpanelUser, __('SSH key authorized'), 'success');
            }

            // Step 3: Initiate backup with SCP transfer to Jabali
            $this->addAccountLog($cpanelUser, __('Initiating backup transfer...'), 'pending');

            $jabaliIp = $this->getJabaliPublicIp();

            $backupResult = $whm->createBackupToScpWithKey(
                $cpanelUser,
                $jabaliIp,
                'root',
                $destPath,
                $actualKeyName,
                22
            );

            if (! ($backupResult['success'] ?? false)) {
                throw new Exception($backupResult['message'] ?? __('Failed to start backup'));
            }

            $this->addAccountLog($cpanelUser, __('Backup initiated, transferring via SCP...'), 'success');

            // Step 4: Wait for backup to arrive
            $this->updateAccountStatus($cpanelUser, 'backup_downloading', __('Waiting for backup file...'));

            $backupPath = $this->waitForBackupFile($cpanelUser, $destPath);
            if (! $backupPath) {
                throw new Exception(__('Backup file did not arrive'));
            }

            $this->addAccountLog($cpanelUser, __('Backup received: :size', ['size' => $this->formatBytes(filesize($backupPath))]), 'success');

            // Step 5: Get migration summary for this user
            $summary = $whm->getUserMigrationSummary($cpanelUser);
            $discoveredData = $whm->convertApiDataToAgentFormat($summary);

            // Step 6: Restore backup
            $this->updateAccountStatus($cpanelUser, 'restoring', __('Restoring data...'));

            $result = $this->getAgent()->send('cpanel.restore_backup', [
                'backup_path' => $backupPath,
                'username' => $user->username,
                'restore_files' => $this->restoreFiles,
                'restore_databases' => $this->restoreDatabases,
                'restore_emails' => $this->restoreEmails,
                'restore_ssl' => $this->restoreSsl,
                'discovered_data' => $discoveredData,
            ]);

            if ($result['success'] ?? false) {
                foreach ($result['log'] ?? [] as $entry) {
                    $this->addAccountLog($cpanelUser, $entry['message'], $entry['status'] ?? 'info');
                }

                $this->updateAccountStatus($cpanelUser, 'completed', __('Migration completed'));

                // Clean up backup file
                @unlink($backupPath);
            } else {
                throw new Exception($result['error'] ?? __('Restore failed'));
            }
        } catch (Exception $e) {
            Log::error('WHM migration failed for user', ['user' => $cpanelUser, 'error' => $e->getMessage()]);
            $this->updateAccountStatus($cpanelUser, 'error', $e->getMessage());
        }

        // Move to next account
        $this->currentAccountIndex++;
        $this->saveToSession();

        // Process next account - dispatch to allow UI update
        $this->dispatch('process-next-account');
    }

    protected function createOrGetUser(string $cpanelUser, string $email): ?User
    {
        // Check if user already exists
        $existingUser = User::where('username', $cpanelUser)->first();
        if ($existingUser) {
            return $existingUser;
        }

        // Check if email already exists
        if (User::where('email', $email)->exists()) {
            $email = "{$cpanelUser}.".time().'@'.explode('@', $email)[1];
        }

        $password = bin2hex(random_bytes(12));

        try {
            if ($this->createLinuxUsers) {
                // Check if Linux user already exists
                exec('id '.escapeshellarg($cpanelUser).' 2>/dev/null', $output, $exitCode);

                if ($exitCode !== 0) {
                    $result = $this->getAgent()->send('user.create', [
                        'username' => $cpanelUser,
                        'password' => $password,
                    ]);

                    if (! ($result['success'] ?? false)) {
                        throw new Exception($result['error'] ?? __('Failed to create system user'));
                    }
                }
            }

            // Create panel user record
            $user = User::create([
                'name' => ucfirst($cpanelUser),
                'username' => $cpanelUser,
                'email' => $email,
                'password' => Hash::make($password),
                'home_directory' => '/home/'.$cpanelUser,
                'disk_quota_mb' => null,
                'is_active' => true,
                'is_admin' => false,
            ]);

            return $user;
        } catch (Exception $e) {
            Log::error('Failed to create user', ['username' => $cpanelUser, 'error' => $e->getMessage()]);

            return null;
        }
    }

    /**
     * Wait for backup file to arrive via SCP transfer
     */
    protected function waitForBackupFile(string $cpanelUser, string $destPath): ?string
    {
        $maxAttempts = 120; // 10 minutes (5s interval)
        $attempt = 0;
        $lastSeenSize = 0;
        $sizeStableCount = 0;

        while ($attempt < $maxAttempts) {
            $attempt++;
            sleep(5);

            // Look for backup files matching this user
            $pattern = "{$destPath}/backup-*_{$cpanelUser}.tar.gz";
            $files = glob($pattern);

            // Also check for cpmove format
            if (empty($files)) {
                $pattern = "{$destPath}/cpmove-{$cpanelUser}.tar.gz";
                $files = glob($pattern);
            }

            if (empty($files)) {
                if ($attempt % 6 === 0) { // Log every 30 seconds
                    $this->addAccountLog($cpanelUser, __('Waiting for backup file... (:count s)', ['count' => $attempt * 5]), 'pending');
                }

                continue;
            }

            // Sort by modification time, get newest
            usort($files, fn ($a, $b) => filemtime($b) - filemtime($a));
            $backupFile = $files[0];
            $currentSize = filesize($backupFile);

            // Check if size is stable (transfer finished)
            if ($currentSize > 0 && $currentSize === $lastSeenSize) {
                $sizeStableCount++;
            } else {
                $sizeStableCount = 0;
            }
            $lastSeenSize = $currentSize;

            // Require size to be stable for 3 checks (15 seconds) and at least 10KB
            if ($sizeStableCount >= 3 && $currentSize >= 10 * 1024) {
                // Fix permissions - file arrives as root via SCP
                $this->getAgent()->send('file.chown', [
                    'path' => $backupFile,
                    'owner' => 'www-data',
                    'group' => 'www-data',
                ]);

                // Verify it's a valid gzip
                $handle = fopen($backupFile, 'rb');
                $magic = $handle ? fread($handle, 2) : '';
                if ($handle) {
                    fclose($handle);
                }

                if ($magic === "\x1f\x8b") {
                    return $backupFile;
                } else {
                    $this->addAccountLog($cpanelUser, __('Invalid backup file format, waiting...'), 'warning');
                    $sizeStableCount = 0;
                }
            }

            if ($attempt % 6 === 0) { // Log every 30 seconds
                $this->addAccountLog($cpanelUser, __('Receiving backup... :size', [
                    'size' => $this->formatBytes($currentSize),
                ]), 'pending');
            }
        }

        return null;
    }

    protected function updateAccountStatus(string $user, string $status, string $message): void
    {
        $this->migrationStatus[$user]['status'] = $status;
        $this->addAccountLog($user, $message, $status === 'error' ? 'error' : 'info');
        $this->saveToSession();
        $this->dispatch('whm-migration-status-updated');
    }

    protected function addAccountLog(string $user, string $message, string $status = 'info'): void
    {
        $this->migrationStatus[$user]['log'][] = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];
        $this->saveToSession();
    }

    public function pollMigrationStatus(): void
    {
        $this->loadMigrationStatusFromStore();
        $this->dispatch('whm-migration-status-updated');
    }

    protected function getMigrationCompletedCount(): int
    {
        return count(array_filter($this->migrationStatus, fn ($s) => $s['status'] === 'completed'));
    }

    protected function getMigrationErrorCount(): int
    {
        return count(array_filter($this->migrationStatus, fn ($s) => $s['status'] === 'error'));
    }

    public function resetMigration(): void
    {
        $this->wizardStep = null;
        $this->hostname = null;
        $this->whmUsername = 'root';
        $this->apiToken = null;
        $this->port = 2087;
        $this->useSSL = true;
        $this->isConnected = false;
        $this->serverInfo = [];
        $this->accounts = [];
        $this->selectedAccounts = [];
        $this->accountConfig = [];
        $this->step1Complete = false;
        $this->step2Complete = false;
        $this->isMigrating = false;
        $this->migrationStatus = [];
        $this->currentAccountIndex = 0;
        $this->totalAccounts = 0;
        $this->statusLog = [];
        $this->whm = null;

        $this->getMigrationStatusStore()->clear();
        $this->clearSession();
        $this->redirect(static::getUrl());
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
