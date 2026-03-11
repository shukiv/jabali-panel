<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Jobs\RunCpanelRestore;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Migration\CpanelApiService;
use App\Support\ServerFacts;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Checkbox;
use Filament\Forms\Components\Radio;
use Filament\Forms\Components\Select;
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
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Components\Wizard;
use Filament\Schemas\Components\Wizard\Step;
use Filament\Schemas\Schema;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Livewire\Attributes\Url;

class CpanelMigration extends Page implements HasActions, HasForms, HasInfolists, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithInfolists;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-arrow-down-tray';

    protected static ?string $navigationLabel = null;

    protected static bool $shouldRegisterNavigation = false;

    public static function getNavigationLabel(): string
    {
        return __('cPanel Migration');
    }

    protected static ?int $navigationSort = 21;

    public static function getNavigationGroup(): string
    {
        return __('Server');
    }

    protected string $view = 'filament.admin.pages.cpanel-migration';

    // Track wizard step via URL (synced with Filament's persistStepInQueryString)
    #[Url(as: 'cpanel-step')]
    public ?string $wizardStep = null;

    // Track step completion for proper Next button state
    public bool $step1Complete = false;

    // Current active tab for data display
    public string $activeDataTab = 'domains';

    // User mode: 'create' (new user from backup) or 'existing' (select existing user)
    public string $userMode = 'create';

    // User selection (admin-only feature, used when userMode is 'existing')
    public ?int $targetUserId = null;

    // Source type: 'remote' (cPanel API) or 'local' (file system)
    public string $sourceType = 'remote';

    // Local backup path (when sourceType is 'local')
    public ?string $localBackupPath = null;

    // Connection form (Step 1 - remote mode)
    public ?string $hostname = null;

    public ?string $cpanelUsername = null;

    public ?string $apiToken = null;

    public int $port = 2083;

    public bool $useSSL = true;

    // Connection status
    public bool $isConnected = false;

    public array $connectionInfo = [];

    // Full API summary data (for skipping backup analysis)
    public array $apiSummary = [];

    // Backup status (Step 2)
    public bool $backupInitiated = false;

    public ?string $backupPid = null;

    public ?string $backupFilename = null;

    public ?string $backupPath = null;

    public int $backupSize = 0;

    public string $backupMethod = 'download';  // 'download' or 'scp'

    public ?int $backupInitiatedAt = null;  // Unix timestamp when backup was started

    public ?int $lastSeenBackupSize = null;  // Track file size between polls

    public int $pollCount = 0;

    // Discovered data from backup
    public array $discoveredData = [];

    // Restore options (Step 3)
    public bool $restoreFiles = true;

    public bool $restoreDatabases = true;

    public bool $restoreEmails = true;

    public bool $restoreSsl = true;

    // Status
    public bool $isProcessing = false;

    public bool $isAnalyzing = false;

    public array $statusLog = [];

    public array $analysisLog = [];

    public array $migrationLog = [];

    public ?string $restoreJobId = null;

    public ?string $restoreLogPath = null;

    public ?string $restoreStatus = null;

    protected ?AgentClient $agent = null;

    protected ?CpanelApiService $cpanel = null;

    public function getTitle(): string|Htmlable
    {
        return __('cPanel Migration');
    }

    public function getSubheading(): ?string
    {
        return __('Migrate complete cPanel accounts to Jabali users');
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
        // Restore credentials from session if page was reloaded (e.g., after auto-advance)
        $this->restoreCredentialsFromSession();
        $this->restoreMigrationStateFromSession();
    }

    public function updatedHostname(): void
    {
        $this->resetConnection();
    }

    public function updatedCpanelUsername(): void
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
        $this->cpanel = null;
        $this->isConnected = false;
        $this->connectionInfo = [];
    }

    public function getAgent(): AgentClient
    {
        return $this->agent ??= new AgentClient;
    }

    protected function getTargetUser(): ?User
    {
        if ($this->userMode === 'existing') {
            return $this->targetUserId ? User::find($this->targetUserId) : null;
        }

        // 'create' mode - return null here, user will be created in startRestore()
        return null;
    }

    /**
     * Create a new user from the cPanel backup.
     */
    protected function createUserFromBackup(): ?User
    {
        if (empty($this->cpanelUsername)) {
            $this->addLog(__('No cPanel username available'), 'error');

            return null;
        }

        // Check if panel user already exists
        $existingUser = User::where('username', $this->cpanelUsername)->first();
        if ($existingUser) {
            $this->addLog(__('User :username already exists, using existing user', ['username' => $this->cpanelUsername]), 'info');
            $this->targetUserId = $existingUser->id;

            return $existingUser;
        }

        // Get the main domain from discovered data for email
        $mainDomain = null;
        foreach ($this->discoveredData['domains'] ?? [] as $domain) {
            if (($domain['type'] ?? '') === 'main') {
                $mainDomain = $domain['name'] ?? null;
                break;
            }
        }
        // Fallback to first domain or generate placeholder
        if (! $mainDomain && ! empty($this->discoveredData['domains'])) {
            $mainDomain = $this->discoveredData['domains'][0]['name'] ?? null;
        }
        $emailDomain = $mainDomain ?? 'example.com';
        $userEmail = $this->cpanelUsername.'@'.$emailDomain;

        // Check if email already exists (must be unique)
        if (User::where('email', $userEmail)->exists()) {
            $userEmail = $this->cpanelUsername.'.'.time().'@'.$emailDomain;
        }

        // Generate a secure random password
        $password = bin2hex(random_bytes(12));

        try {
            // Check if Linux user already exists (avoid shelling out).
            $linuxUserExists = false;
            if (function_exists('posix_getpwnam')) {
                $linuxUserExists = posix_getpwnam($this->cpanelUsername) !== false;
            } else {
                $linuxUserExists = is_dir('/home/'.$this->cpanelUsername);
            }

            if (! $linuxUserExists) {
                // Create Linux user via agent
                $this->addLog(__('Creating system user: :username', ['username' => $this->cpanelUsername]), 'pending');

                $result = $this->getAgent()->send('user.create', [
                    'username' => $this->cpanelUsername,
                    'password' => $password,
                ]);

                if (! ($result['success'] ?? false)) {
                    throw new Exception($result['error'] ?? __('Failed to create system user'));
                }

                $this->addLog(__('System user created: :username', ['username' => $this->cpanelUsername]), 'success');
            } else {
                $this->addLog(__('System user already exists: :username', ['username' => $this->cpanelUsername]), 'info');
            }

            // Create panel user record
            $user = User::create([
                'name' => ucfirst($this->cpanelUsername),
                'username' => $this->cpanelUsername,
                'email' => $userEmail,
                'password' => Hash::make($password),
                'home_directory' => '/home/'.$this->cpanelUsername,
                'disk_quota_mb' => null,  // Unlimited
                'is_active' => true,
                'is_admin' => false,
            ]);

            $this->targetUserId = $user->id;
            $this->addLog(__('Created panel user: :username (email: :email)', ['username' => $user->username, 'email' => $userEmail]), 'success');

            return $user;
        } catch (Exception $e) {
            Log::error('Failed to create user from backup', [
                'username' => $this->cpanelUsername,
                'error' => $e->getMessage(),
                'trace' => $e->getTraceAsString(),
            ]);
            $this->addLog(__('Failed to create user: :error', ['error' => $e->getMessage()]), 'error');

            return null;
        }
    }

    protected function getCpanel(): ?CpanelApiService
    {
        // Try to restore credentials from session if not set (page was reloaded)
        if (! $this->hostname || ! $this->cpanelUsername || ! $this->apiToken) {
            $this->restoreCredentialsFromSession();
        }

        if (! $this->hostname || ! $this->cpanelUsername || ! $this->apiToken) {
            return null;
        }

        return $this->cpanel ??= new CpanelApiService(
            trim($this->hostname),
            trim($this->cpanelUsername),
            trim($this->apiToken),
            $this->port,
            $this->useSSL
        );
    }

    /**
     * Store cPanel credentials in session to survive page reloads.
     */
    protected function storeCredentialsInSession(): void
    {
        session()->put('cpanel_migration.hostname', $this->hostname);
        session()->put('cpanel_migration.username', $this->cpanelUsername);
        session()->put('cpanel_migration.token', $this->apiToken);
        session()->put('cpanel_migration.port', $this->port);
        session()->put('cpanel_migration.useSSL', $this->useSSL);
        session()->put('cpanel_migration.targetUserId', $this->targetUserId);
        session()->put('cpanel_migration.isConnected', $this->isConnected);
        session()->put('cpanel_migration.connectionInfo', $this->connectionInfo);
        session()->put('cpanel_migration.apiSummary', $this->apiSummary);
        session()->put('cpanel_migration.sourceType', $this->sourceType);
        session()->put('cpanel_migration.localBackupPath', $this->localBackupPath);
        session()->put('cpanel_migration.backupPath', $this->backupPath);
        session()->put('cpanel_migration.backupFilename', $this->backupFilename);
        session()->put('cpanel_migration.backupSize', $this->backupSize);
        session()->put('cpanel_migration.backupInitiated', $this->backupInitiated);
        session()->put('cpanel_migration.backupMethod', $this->backupMethod);
        session()->put('cpanel_migration.backupInitiatedAt', $this->backupInitiatedAt);
        session()->put('cpanel_migration.discoveredData', $this->discoveredData);
        session()->put('cpanel_migration.step1Complete', $this->step1Complete);

        // Ensure session is saved before any redirect
        session()->save();
    }

    /**
     * Restore cPanel credentials from session after page reload.
     */
    protected function restoreCredentialsFromSession(): void
    {
        if (session()->has('cpanel_migration.hostname')) {
            $this->hostname = session('cpanel_migration.hostname');
            $this->cpanelUsername = session('cpanel_migration.username');
            $this->apiToken = session('cpanel_migration.token');
            $this->port = session('cpanel_migration.port', 2083);
            $this->useSSL = session('cpanel_migration.useSSL', true);
            $this->targetUserId = session('cpanel_migration.targetUserId');
            $this->isConnected = session('cpanel_migration.isConnected', false);
            $this->connectionInfo = session('cpanel_migration.connectionInfo', []);
            $this->apiSummary = session('cpanel_migration.apiSummary', []);
            $this->sourceType = session('cpanel_migration.sourceType', 'remote');
            $this->localBackupPath = session('cpanel_migration.localBackupPath');
            $this->backupPath = session('cpanel_migration.backupPath');
            $this->backupFilename = session('cpanel_migration.backupFilename');
            $this->backupSize = session('cpanel_migration.backupSize', 0);
            $this->backupInitiated = session('cpanel_migration.backupInitiated', false);
            $this->backupMethod = session('cpanel_migration.backupMethod', 'download');
            $this->backupInitiatedAt = session('cpanel_migration.backupInitiatedAt');
            $this->discoveredData = session('cpanel_migration.discoveredData', []);
            $this->step1Complete = session('cpanel_migration.step1Complete', false);
        }
    }

    /**
     * Clear stored session credentials.
     */
    protected function clearSessionCredentials(): void
    {
        session()->forget([
            'cpanel_migration.hostname',
            'cpanel_migration.username',
            'cpanel_migration.token',
            'cpanel_migration.port',
            'cpanel_migration.useSSL',
            'cpanel_migration.targetUserId',
            'cpanel_migration.isConnected',
            'cpanel_migration.connectionInfo',
            'cpanel_migration.apiSummary',
            'cpanel_migration.sourceType',
            'cpanel_migration.localBackupPath',
            'cpanel_migration.backupPath',
            'cpanel_migration.backupFilename',
            'cpanel_migration.backupSize',
            'cpanel_migration.backupInitiated',
            'cpanel_migration.backupMethod',
            'cpanel_migration.backupInitiatedAt',
            'cpanel_migration.discoveredData',
            'cpanel_migration.step1Complete',
        ]);
    }

    protected function getBackupDestPath(): string
    {
        return '/var/backups/jabali/cpanel-migrations';
    }

    protected function getJabaliPublicIp(): string
    {
        $ip = ServerFacts::serverIp('');
        if ($ip !== '') {
            return $ip;
        }

        $fallback = gethostbyname(gethostname() ?: 'localhost');

        return is_string($fallback) ? $fallback : '';
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
                $this->getBackupStep(),
                $this->getReviewStep(),
                $this->getRestoreStep(),
            ])
                ->nextAction(
                    fn (Action $action) => $action
                        ->disabled(fn () => $this->isNextStepDisabled())
                        ->hidden(fn () => $this->isNextButtonHidden())
                )
                ->persistStepInQueryString('cpanel-step'),
        ]);
    }

    /**
     * Check if Next button should be hidden (during backup transfer).
     */
    protected function isNextButtonHidden(): bool
    {
        // Hide Next during backup transfer on step 2 (backup started but not complete)
        // We know we're on step 2 if step1Complete is true
        return $this->step1Complete && $this->backupInitiated && ! $this->backupPath;
    }

    /**
     * Get normalized current step name from query string.
     */
    protected function getCurrentStepName(): string
    {
        // Use Livewire URL-synced property (works in both initial load and Livewire requests)
        $step = $this->wizardStep ?? 'connect';

        // Handle full wizard step IDs like "migrationForm.connect::wizard-step"
        if (str_contains($step, '.')) {
            // Extract step name after the dot and before ::
            if (preg_match('/\.(\w+)(?:::|$)/', $step, $matches)) {
                return $matches[1];
            }
        }

        return $step ?: 'connect';
    }

    protected function getConnectStep(): Step
    {
        return Step::make(__('Connect'))
            ->id('connect')
            ->icon('heroicon-o-link')
            ->description($this->sourceType === 'local' ? __('Select local backup file') : __('Enter cPanel credentials'))
            ->schema([
                Section::make(__('Target User'))
                    ->description(__('Choose how to handle the user account'))
                    ->icon('heroicon-o-user')
                    ->iconColor('primary')
                    ->schema([
                        Radio::make('userMode')
                            ->label(__('User Account'))
                            ->options([
                                'create' => __('Create new user from backup'),
                                'existing' => __('Restore to existing user'),
                            ])
                            ->descriptions([
                                'create' => __('Creates a new user with the cPanel username and password from backup (unlimited disk space)'),
                                'existing' => __('Restore to an existing user account'),
                            ])
                            ->default('create')
                            ->live()
                            ->required(),
                        Select::make('targetUserId')
                            ->label(__('Select User'))
                            ->options(fn () => User::where('is_active', true)
                                ->orderBy('username')
                                ->pluck('username', 'id')
                                ->mapWithKeys(fn ($username, $id) => [
                                    $id => User::find($id)->name.' ('.$username.')',
                                ])
                            )
                            ->searchable()
                            ->required(fn (Get $get) => $get('userMode') === 'existing')
                            ->visible(fn (Get $get) => $get('userMode') === 'existing')
                            ->helperText(__('All migrated domains, emails, and databases will be assigned to this user')),
                    ]),

                Section::make(__('Backup Source'))
                    ->description(__('Choose where to get the cPanel backup from'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->iconColor('primary')
                    ->schema([
                        Select::make('sourceType')
                            ->label(__('Source Type'))
                            ->options([
                                'remote' => __('Remote cPanel Server (Create & Transfer Backup)'),
                                'local' => __('Local File (Already on this server)'),
                            ])
                            ->default('remote')
                            ->live()
                            ->afterStateUpdated(fn () => $this->resetConnection())
                            ->helperText(__('Select "Local File" if you already have a cPanel backup on this server')),
                    ]),

                // Remote cPanel credentials (shown when sourceType is 'remote')
                Section::make(__('cPanel Credentials'))
                    ->description(__('Enter the cPanel server connection details'))
                    ->icon('heroicon-o-server')
                    ->visible(fn () => $this->sourceType === 'remote')
                    ->schema([
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            TextInput::make('hostname')
                                ->label(__('cPanel Hostname'))
                                ->placeholder(__('cpanel.example.com'))
                                ->required(fn () => $this->sourceType === 'remote')
                                ->helperText(__('Your cPanel server hostname or IP address')),
                            TextInput::make('port')
                                ->label(__('Port'))
                                ->numeric()
                                ->default(2083)
                                ->required(fn () => $this->sourceType === 'remote')
                                ->helperText(__('Usually 2083 for SSL or 2082 without')),
                        ]),
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            TextInput::make('cpanelUsername')
                                ->label(__('cPanel Username'))
                                ->required(fn () => $this->sourceType === 'remote')
                                ->helperText(__('Your cPanel account username')),
                            TextInput::make('apiToken')
                                ->label(__('API Token'))
                                ->password()
                                ->required(fn () => $this->sourceType === 'remote')
                                ->revealable()
                                ->helperText(__('Generate from cPanel → Security → Manage API Tokens')),
                        ]),
                        Checkbox::make('useSSL')
                            ->label(__('Use SSL (HTTPS)'))
                            ->default(true)
                            ->helperText(__('Recommended. Disable only if your cPanel does not support SSL.')),
                    ]),

                // Local file selection (shown when sourceType is 'local')
                Section::make(__('Local Backup File'))
                    ->description(__('Enter the path to the cPanel backup file on this server'))
                    ->icon('heroicon-o-folder')
                    ->visible(fn () => $this->sourceType === 'local')
                    ->schema([
                        TextInput::make('localBackupPath')
                            ->label(__('Backup File Path'))
                            ->placeholder(__('/home/user/backups/backup-date_username.tar.gz'))
                            ->required(fn () => $this->sourceType === 'local')
                            ->helperText(__('Full path to the cPanel backup file (e.g., /var/backups/backup.tar.gz)')),
                        Text::make(__('Supported formats: .tar.gz, .tgz'))->color('gray'),
                        Text::make(__('Tip: Upload backups to /var/backups/jabali/cpanel-migrations/'))->color('gray'),
                    ]),

                // Test Connection button (remote mode only)
                FormActions::make([
                    Action::make('testConnection')
                        ->label(fn () => $this->isConnected ? __('Connected') : __('Test Connection'))
                        ->icon(fn () => $this->isConnected ? 'heroicon-o-check-circle' : 'heroicon-o-signal')
                        ->color(fn () => $this->isConnected ? 'success' : 'primary')
                        ->disabled(fn () => $this->userMode === 'existing' && ! $this->targetUserId)
                        ->tooltip(fn () => $this->userMode === 'existing' && ! $this->targetUserId ? __('Please select a user first') : null)
                        ->action('testConnection'),
                ])
                    ->alignEnd()
                    ->visible(fn () => $this->sourceType === 'remote'),

                // Validate local file button (local mode only)
                FormActions::make([
                    Action::make('validateLocalFile')
                        ->label(fn () => $this->isConnected ? __('File Validated') : __('Validate Backup File'))
                        ->icon(fn () => $this->isConnected ? 'heroicon-o-check-circle' : 'heroicon-o-document-magnifying-glass')
                        ->color(fn () => $this->isConnected ? 'success' : 'primary')
                        ->disabled(fn () => $this->userMode === 'existing' && ! $this->targetUserId)
                        ->tooltip(fn () => $this->userMode === 'existing' && ! $this->targetUserId ? __('Please select a user first') : null)
                        ->action('validateLocalFile'),
                ])
                    ->alignEnd()
                    ->visible(fn () => $this->sourceType === 'local'),

                Section::make(__('Connection Successful'))
                    ->icon('heroicon-o-check-circle')
                    ->iconColor('success')
                    ->visible(fn () => $this->isConnected && $this->sourceType === 'remote')
                    ->schema([
                        Grid::make(['default' => 2, 'sm' => 5])->schema([
                            Text::make(fn () => __('Host: :host', ['host' => $this->hostname ?? '-'])),
                            Text::make(fn () => __('Domains: :count', ['count' => $this->connectionInfo['domains'] ?? 0])),
                            Text::make(fn () => __('Databases: :count', ['count' => $this->connectionInfo['databases'] ?? 0])),
                            Text::make(fn () => __('Emails: :count', ['count' => $this->connectionInfo['emails'] ?? 0])),
                            Text::make(fn () => __('SSL: :count', ['count' => $this->connectionInfo['ssl'] ?? 0])),
                        ]),
                        Text::make(__('You can proceed to the next step.'))->color('success'),
                    ]),

                Section::make(__('File Validated'))
                    ->icon('heroicon-o-check-circle')
                    ->iconColor('success')
                    ->visible(fn () => $this->isConnected && $this->sourceType === 'local')
                    ->schema([
                        Text::make(fn () => __('File: :path', ['path' => basename($this->localBackupPath ?? '')])),
                        Text::make(fn () => __('Size: :size', ['size' => $this->formatBytes(filesize($this->localBackupPath ?? '') ?: 0)])),
                        Text::make(__('You can proceed to the next step.'))->color('success'),
                    ]),
            ])
            ->afterValidation(function () {
                // Only require target user if 'existing' mode is selected
                if ($this->userMode === 'existing' && ! $this->targetUserId) {
                    Notification::make()
                        ->title(__('User required'))
                        ->body(__('Please select a target user'))
                        ->danger()
                        ->send();
                    throw new Exception(__('Please select a target user first'));
                }

                if (! $this->isConnected) {
                    $message = $this->sourceType === 'local'
                        ? __('Please validate the backup file before proceeding')
                        : __('Please test the connection first');
                    Notification::make()
                        ->title($this->sourceType === 'local' ? __('Validation required') : __('Connection required'))
                        ->body($message)
                        ->danger()
                        ->send();
                    throw new Exception($message);
                }

                // Mark step 1 as complete - user is moving to step 2
                $this->step1Complete = true;
            });
    }

    protected function getBackupStep(): Step
    {
        // For local files - analyze backup
        if ($this->sourceType === 'local') {
            return Step::make(__('Backup'))
                ->id('backup')
                ->icon('heroicon-o-folder-open')
                ->description(__('Analyzing local backup'))
                ->schema([
                    Section::make(__('Local Backup'))
                        ->description(__('Click the button below to analyze the backup contents'))
                        ->icon('heroicon-o-folder-open')
                        ->iconColor('primary')
                        ->headerActions([
                            Action::make('analyzeBackup')
                                ->label(__('Analyze Backup'))
                                ->icon('heroicon-o-magnifying-glass')
                                ->color('primary')
                                ->disabled(fn () => $this->isAnalyzing || ! empty($this->discoveredData))
                                ->action('analyzeLocalBackup'),
                        ])
                        ->schema([
                            Text::make(__('Target User: :name (:username)', [
                                'name' => $this->getTargetUser()?->name ?? '-',
                                'username' => $this->getTargetUser()?->username ?? '-',
                            ])),
                            Text::make(__('File: :name', ['name' => $this->backupFilename ?? basename($this->localBackupPath ?? '')])),
                            Text::make(__('Size: :size', ['size' => $this->formatBytes($this->backupSize)])),
                        ]),

                    Section::make(__('Analysis Progress'))
                        ->icon($this->getAnalysisStatusIcon())
                        ->iconColor($this->getAnalysisStatusColor())
                        ->schema($this->getLocalBackupStatusSchema())
                        ->extraAttributes($this->isAnalyzing ? ['wire:poll.1s' => 'pollAnalysisStatus'] : []),
                ])
                ->afterValidation(function () {
                    if (empty($this->discoveredData)) {
                        Notification::make()
                            ->title(__('Analysis required'))
                            ->body(__('Please analyze the backup file before proceeding'))
                            ->danger()
                            ->send();
                        $this->halt();
                    }
                });
        }

        // Remote cPanel - create and transfer backup
        return Step::make(__('Backup'))
            ->id('backup')
            ->icon('heroicon-o-cloud-arrow-down')
            ->description(__('Create and transfer backup'))
            ->schema([
                Section::make(__('Backup Transfer'))
                    ->description(__('Click the button below to create and transfer the backup from cPanel'))
                    ->icon('heroicon-o-server')
                    ->headerActions([
                        Action::make('startBackup')
                            ->label(__('Start Backup Transfer'))
                            ->icon('heroicon-o-cloud-arrow-down')
                            ->color('primary')
                            ->disabled(fn () => $this->backupInitiated || (bool) $this->backupPath)
                            ->action('startBackupTransfer'),
                    ])
                    ->schema([
                        Text::make(__('Target User: :name (:username)', [
                            'name' => $this->getTargetUser()?->name ?? '-',
                            'username' => $this->getTargetUser()?->username ?? '-',
                        ])),
                        Text::make(__('Note: Large accounts may take several minutes.'))->color('warning'),
                    ]),

                Section::make(__('Transfer Status'))
                    ->icon($this->backupPath ? 'heroicon-o-check-circle' : 'heroicon-o-clock')
                    ->iconColor($this->backupPath ? 'success' : 'gray')
                    ->schema($this->getStatusLogSchema())
                    ->extraAttributes($this->backupInitiated && ! $this->backupPath ? ['wire:poll.5s' => 'checkBackupStatus'] : []),
            ])
            ->afterValidation(function () {
                if (! $this->backupPath) {
                    Notification::make()
                        ->title(__('Backup required'))
                        ->body(__('Please complete the backup transfer first'))
                        ->danger()
                        ->send();
                    $this->halt();
                }
            });
    }

    protected function getStatusLogSchema(): array
    {
        if (empty($this->statusLog)) {
            return [
                Text::make(__('Click "Start Backup Transfer" to begin.'))->color('gray'),
            ];
        }

        $items = [];
        foreach ($this->statusLog as $entry) {
            $color = match ($entry['status']) {
                'success' => 'success',
                'error' => 'danger',
                'pending' => 'warning',
                default => 'gray',
            };

            $prefix = match ($entry['status']) {
                'success' => '✓ ',
                'error' => '✗ ',
                'pending' => '○ ',
                default => '• ',
            };

            $items[] = Text::make($prefix.$entry['message'])
                ->color($color);
        }

        if ($this->backupPath) {
            $items[] = Section::make(__('Backup Complete'))
                ->icon('heroicon-o-check-circle')
                ->iconColor('success')
                ->schema([
                    Text::make(__('File: :name', ['name' => $this->backupFilename ?? basename($this->backupPath)])),
                    Text::make(__('Size: :size', ['size' => $this->formatBytes($this->backupSize)])),
                ])
                ->compact();
        }

        return $items;
    }

    protected function getLocalBackupStatusSchema(): array
    {
        $items = [];

        // Show analysis log entries
        if (! empty($this->analysisLog)) {
            foreach ($this->analysisLog as $entry) {
                $prefix = match ($entry['status']) {
                    'success' => '✓ ',
                    'error' => '✗ ',
                    'pending' => '○ ',
                    default => '• ',
                };
                $color = match ($entry['status']) {
                    'success' => 'success',
                    'error' => 'danger',
                    'pending' => 'warning',
                    default => 'gray',
                };

                $items[] = Text::make($prefix.$entry['message'])->color($color);
            }
        }

        // Show initial message if no log and not analyzing
        if (empty($this->analysisLog) && ! $this->isAnalyzing && empty($this->discoveredData)) {
            return [
                Text::make(__('Click "Analyze Backup" to discover the backup contents.'))->color('gray'),
            ];
        }

        // Show results if analysis complete
        if (! empty($this->discoveredData)) {
            $domainCount = count($this->discoveredData['domains'] ?? []);
            $dbCount = count($this->discoveredData['databases'] ?? []);
            $mailCount = count($this->discoveredData['mailboxes'] ?? []);
            $forwarderCount = count($this->discoveredData['forwarders'] ?? []);
            $sslCount = count($this->discoveredData['ssl_certificates'] ?? []);

            $items[] = Grid::make(['default' => 2, 'sm' => 5])->schema([
                Section::make((string) $domainCount)
                    ->description(__('Domains'))
                    ->icon('heroicon-o-globe-alt')
                    ->iconColor('primary')
                    ->compact(),
                Section::make((string) $dbCount)
                    ->description(__('Databases'))
                    ->icon('heroicon-o-circle-stack')
                    ->iconColor('warning')
                    ->compact(),
                Section::make((string) $mailCount)
                    ->description(__('Mailboxes'))
                    ->icon('heroicon-o-envelope')
                    ->iconColor('success')
                    ->compact(),
                Section::make((string) $forwarderCount)
                    ->description(__('Forwarders'))
                    ->icon('heroicon-o-arrow-uturn-right')
                    ->iconColor('gray')
                    ->compact(),
                Section::make((string) $sslCount)
                    ->description(__('SSL Certs'))
                    ->icon('heroicon-o-lock-closed')
                    ->iconColor('info')
                    ->compact(),
            ]);
            $items[] = Text::make(__('You can proceed to the next step.'))->color('success');
        }

        return $items;
    }

    protected function getAnalyzeButtonLabel(): string
    {
        if ($this->isAnalyzing) {
            return __('Analyzing...');
        }
        if (! empty($this->discoveredData)) {
            return __('Analysis Complete');
        }

        return __('Analyze Backup');
    }

    protected function getAnalyzeButtonIcon(): string
    {
        if ($this->isAnalyzing) {
            return 'heroicon-o-arrow-path';
        }
        if (! empty($this->discoveredData)) {
            return 'heroicon-o-check-circle';
        }

        return 'heroicon-o-magnifying-glass';
    }

    protected function getAnalyzeButtonColor(): string
    {
        if ($this->isAnalyzing) {
            return 'warning';
        }
        if (! empty($this->discoveredData)) {
            return 'success';
        }

        return 'primary';
    }

    protected function getAnalysisStatusIcon(): string
    {
        if (! empty($this->discoveredData)) {
            return 'heroicon-o-check-circle';
        }
        if ($this->isAnalyzing) {
            return 'heroicon-o-arrow-path';
        }

        return 'heroicon-o-clock';
    }

    protected function getAnalysisStatusColor(): string
    {
        if (! empty($this->discoveredData)) {
            return 'success';
        }
        if ($this->isAnalyzing) {
            return 'warning';
        }

        return 'gray';
    }

    protected function addAnalysisLog(string $message, string $status = 'info'): void
    {
        // Update the last pending entry if this is a completion
        $lastIndex = count($this->analysisLog) - 1;
        if ($lastIndex >= 0 && $this->analysisLog[$lastIndex]['status'] === 'pending' && $status !== 'pending') {
            $this->analysisLog[$lastIndex] = [
                'message' => $message,
                'status' => $status,
                'time' => now()->format('H:i:s'),
            ];

            return;
        }

        $this->analysisLog[] = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];
    }

    public function pollAnalysisStatus(): void
    {
        // This method is called by wire:poll to refresh the UI during analysis
        // The actual work is done in analyzeLocalBackup
    }

    protected function getReviewStep(): Step
    {
        return Step::make(__('Review'))
            ->id('review')
            ->icon('heroicon-o-clipboard-document-check')
            ->description(__('Review discovered data'))
            ->schema($this->getReviewStepSchema());
    }

    protected function getReviewStepSchema(): array
    {
        if (empty($this->discoveredData)) {
            return [
                Section::make(__('Waiting for Backup'))
                    ->icon('heroicon-o-clock')
                    ->iconColor('warning')
                    ->schema([
                        Text::make(__('Please complete the backup transfer in the previous step.')),
                    ]),
            ];
        }

        $user = $this->getTargetUser();
        $domainCount = count($this->discoveredData['domains'] ?? []);
        $dbCount = count($this->discoveredData['databases'] ?? []);
        $mailCount = count($this->discoveredData['mailboxes'] ?? []);
        $forwarderCount = count($this->discoveredData['forwarders'] ?? []);
        $sslCount = count($this->discoveredData['ssl_certificates'] ?? []);

        return [
            Section::make(__('Migration Summary'))
                ->icon('heroicon-o-clipboard-document-list')
                ->iconColor('primary')
                ->schema([
                    Text::make(__('Target User: :name (:username)', [
                        'name' => $user?->name ?? '-',
                        'username' => $user?->username ?? '-',
                    ])),
                    Grid::make(['default' => 2, 'sm' => 5])->schema([
                        Section::make((string) $domainCount)
                            ->description(__('Domains'))
                            ->icon('heroicon-o-globe-alt')
                            ->iconColor('primary')
                            ->compact(),
                        Section::make((string) $dbCount)
                            ->description(__('Databases'))
                            ->icon('heroicon-o-circle-stack')
                            ->iconColor('warning')
                            ->compact(),
                        Section::make((string) $mailCount)
                            ->description(__('Mailboxes'))
                            ->icon('heroicon-o-envelope')
                            ->iconColor('success')
                            ->compact(),
                        Section::make((string) $forwarderCount)
                            ->description(__('Forwarders'))
                            ->icon('heroicon-o-arrow-uturn-right')
                            ->iconColor('gray')
                            ->compact(),
                        Section::make((string) $sslCount)
                            ->description(__('SSL Certs'))
                            ->icon('heroicon-o-lock-closed')
                            ->iconColor('info')
                            ->compact(),
                    ]),
                ]),

            Section::make(__('What to Restore'))
                ->description(__('Select which parts of the backup to restore'))
                ->icon('heroicon-o-check-circle')
                ->schema([
                    Grid::make(['default' => 1, 'sm' => 2])->schema([
                        Checkbox::make('restoreFiles')
                            ->label(__('Website Files'))
                            ->helperText(__('Restore all website files to the user\'s domains folder'))
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

            Section::make(__('Discovered Data'))
                ->icon('heroicon-o-magnifying-glass')
                ->schema([
                    Tabs::make('DataTabs')
                        ->tabs([
                            Tabs\Tab::make(__('Domains'))
                                ->icon('heroicon-o-globe-alt')
                                ->badge((string) $domainCount)
                                ->schema($this->getDomainsTabContent()),
                            Tabs\Tab::make(__('Databases'))
                                ->icon('heroicon-o-circle-stack')
                                ->badge((string) $dbCount)
                                ->schema($this->getDatabasesTabContent()),
                            Tabs\Tab::make(__('Mailboxes'))
                                ->icon('heroicon-o-envelope')
                                ->badge((string) $mailCount)
                                ->schema($this->getMailboxesTabContent()),
                            Tabs\Tab::make(__('Forwarders'))
                                ->icon('heroicon-o-arrow-uturn-right')
                                ->badge((string) $forwarderCount)
                                ->schema($this->getForwardersTabContent()),
                            Tabs\Tab::make(__('SSL'))
                                ->icon('heroicon-o-lock-closed')
                                ->badge((string) $sslCount)
                                ->schema($this->getSslTabContent()),
                        ]),
                ]),
        ];
    }

    protected function getDomainsTabContent(): array
    {
        $domains = $this->discoveredData['domains'] ?? [];
        if (empty($domains)) {
            return [Text::make(__('No domains found in backup.'))];
        }

        $items = [];
        foreach ($domains as $domain) {
            $typePrefix = match ($domain['type'] ?? 'addon') {
                'main' => '★ ',
                'addon' => '● ',
                'sub' => '◦ ',
                default => '• ',
            };
            $typeColor = match ($domain['type'] ?? 'addon') {
                'main' => 'success',
                'addon' => 'primary',
                'sub' => 'warning',
                default => 'gray',
            };

            $items[] = Text::make($typePrefix.$domain['name'].' ('.$domain['type'].')')
                ->color($typeColor);
        }

        return $items;
    }

    protected function getDatabasesTabContent(): array
    {
        $databases = $this->discoveredData['databases'] ?? [];
        if (empty($databases)) {
            return [Text::make(__('No databases found in backup.'))];
        }

        $user = $this->getTargetUser();
        $userPrefix = $user?->username ?? 'user';

        $items = [];
        foreach ($databases as $db) {
            $oldName = $db['name'];
            $newName = $userPrefix.'_'.preg_replace('/^[^_]+_/', '', $oldName);
            $newName = substr($newName, 0, 64);

            $items[] = Text::make("→ {$oldName} → {$newName}")
                ->color('primary');
        }

        return $items;
    }

    protected function getMailboxesTabContent(): array
    {
        $mailboxes = $this->discoveredData['mailboxes'] ?? [];
        if (empty($mailboxes)) {
            return [Text::make(__('No mailboxes found in backup.'))];
        }

        $items = [];
        foreach ($mailboxes as $mailbox) {
            $items[] = Text::make('✉ '.$mailbox['email'])
                ->color('success');
        }

        return $items;
    }

    protected function getSslTabContent(): array
    {
        $sslCerts = $this->discoveredData['ssl_certificates'] ?? [];
        if (empty($sslCerts)) {
            return [Text::make(__('No SSL certificates found in backup.'))];
        }

        $items = [];
        foreach ($sslCerts as $cert) {
            $hasComplete = ($cert['has_key'] ?? false) && ($cert['has_cert'] ?? false);
            $prefix = $hasComplete ? '🔒 ' : '⚠ ';
            $items[] = Text::make($prefix.$cert['domain'])
                ->color($hasComplete ? 'success' : 'warning');
        }

        return $items;
    }

    protected function getForwardersTabContent(): array
    {
        $forwarders = $this->discoveredData['forwarders'] ?? [];
        if (empty($forwarders)) {
            return [Text::make(__('No forwarders found in backup.'))];
        }

        $items = [];
        foreach ($forwarders as $forwarder) {
            $email = $forwarder['email'] ?? '';
            $destinations = $forwarder['destinations'] ?? '';
            $destPreview = is_array($destinations) ? implode(', ', $destinations) : $destinations;
            $destPreview = strlen($destPreview) > 40 ? substr($destPreview, 0, 37).'...' : $destPreview;
            $items[] = Text::make('↪ '.$email.' → '.$destPreview)
                ->color('gray');
        }

        return $items;
    }

    protected function getRestoreStep(): Step
    {
        return Step::make(__('Restore'))
            ->id('restore')
            ->icon('heroicon-o-play')
            ->description(__('Migration progress'))
            ->schema([
                FormActions::make([
                    Action::make('startRestore')
                        ->label(__('Start Restore'))
                        ->icon('heroicon-o-play')
                        ->color('success')
                        ->visible(fn () => ! $this->isProcessing && empty($this->migrationLog))
                        ->requiresConfirmation()
                        ->modalHeading(__('Start Restore'))
                        ->modalDescription(__('This will restore the selected items. Existing data may be overwritten.'))
                        ->action('startRestore'),

                    Action::make('newMigration')
                        ->label(__('New Migration'))
                        ->icon('heroicon-o-plus')
                        ->color('primary')
                        ->visible(fn () => ! $this->isProcessing && ! empty($this->migrationLog))
                        ->action('resetMigration'),
                ])->alignEnd(),

                Section::make(__('Migration Progress'))
                    ->icon($this->isProcessing ? 'heroicon-o-arrow-path' : ($this->migrationLog ? 'heroicon-o-check-circle' : 'heroicon-o-clock'))
                    ->iconColor($this->isProcessing ? 'warning' : ($this->migrationLog ? 'success' : 'gray'))
                    ->schema($this->getMigrationLogSchema())
                    ->extraAttributes($this->isProcessing ? ['wire:poll.1s' => 'pollMigrationLog'] : []),
            ]);
    }

    protected function getMigrationLogSchema(): array
    {
        if (empty($this->migrationLog)) {
            return [
                Text::make(__('Click "Start Restore" to begin the migration.'))->color('gray'),
            ];
        }

        $items = [];
        foreach ($this->migrationLog as $entry) {
            $status = $entry['status'] ?? 'info';
            $message = $entry['message'] ?? '';

            $color = match ($status) {
                'success' => 'success',
                'error' => 'danger',
                'warning' => 'warning',
                'pending' => 'warning',
                default => 'gray',
            };

            $prefix = match ($status) {
                'success' => '✓ ',
                'error' => '✗ ',
                'warning' => '• ',
                'pending' => '○ ',
                default => '• ',
            };

            $items[] = Text::make($prefix.$message)->color($color);
        }

        return $items;
    }

    public function table(Table $table): Table
    {
        // Empty table - data is displayed via schema components
        return $table
            ->query(User::query()->whereRaw('1 = 0'))
            ->columns([])
            ->paginated(false);
    }

    public function testConnection(): void
    {
        if ($this->userMode === 'existing' && ! $this->targetUserId) {
            Notification::make()
                ->title(__('User required'))
                ->body(__('Please select a target user first'))
                ->danger()
                ->send();

            return;
        }

        if (empty($this->hostname) || empty($this->cpanelUsername) || empty($this->apiToken)) {
            Notification::make()
                ->title(__('Missing credentials'))
                ->body(__('Please fill in all required fields'))
                ->danger()
                ->send();

            return;
        }

        try {
            $cpanel = $this->getCpanel();
            $result = $cpanel->testConnection();

            if ($result['success']) {
                $this->isConnected = true;

                $summary = $cpanel->getMigrationSummary();

                // Store full API summary for use during restore (skips expensive backup analysis)
                $this->apiSummary = $summary;

                // Immediately populate discoveredData from API - no need to wait for backup analysis
                $this->discoveredData = $this->convertApiDataToAgentFormat($summary);

                $this->connectionInfo = [
                    'domains' => count($this->discoveredData['domains'] ?? []),
                    'emails' => count($this->discoveredData['mailboxes'] ?? []),
                    'databases' => count($this->discoveredData['databases'] ?? []),
                    'ssl' => count($this->discoveredData['ssl_certificates'] ?? []),
                ];

                Notification::make()
                    ->title(__('Connection successful'))
                    ->body(__('Found :domains domains, :emails email accounts, :dbs databases, :ssl SSL certificates', [
                        'domains' => $this->connectionInfo['domains'],
                        'emails' => $this->connectionInfo['emails'],
                        'dbs' => $this->connectionInfo['databases'],
                        'ssl' => $this->connectionInfo['ssl'],
                    ]))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['message'] ?? __('Connection failed'));
            }
        } catch (Exception $e) {
            $this->isConnected = false;
            Log::error('cPanel connection failed', ['error' => $e->getMessage()]);

            Notification::make()
                ->title(__('Connection failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    /**
     * Check if the Next button should be disabled based on current wizard step.
     * Uses step1Complete flag to reliably track wizard progress.
     */
    protected function isNextStepDisabled(): bool
    {
        // If step 1 is not complete, we're on step 1
        if (! $this->step1Complete) {
            // Step 1: Must have connection (and target user if 'existing' mode)
            return ! $this->isConnected || ($this->userMode === 'existing' && ! $this->targetUserId);
        }

        // Step 1 is complete, we're on step 2 or beyond
        // Check step 2 prerequisites (backup ready)
        if ($this->sourceType === 'local') {
            // Local: need analysis complete
            if (empty($this->discoveredData)) {
                return true;
            }
        } else {
            // Remote: need backup downloaded
            if (! $this->backupPath) {
                return true;
            }
        }

        // All prerequisites met (step 3 and beyond)
        return false;
    }

    protected function countDomains(array $domains): int
    {
        $count = 0;
        if (! empty($domains['main'])) {
            $count++;
        }
        $count += count($domains['addon'] ?? []);
        $count += count($domains['sub'] ?? []);

        return $count;
    }

    /**
     * Convert API migration summary to backup analysis format for the agent.
     * API format: ['domains' => ['main' => 'x', 'addon' => [...]], 'databases' => [...], 'email_accounts' => [...], 'ssl_certificates' => [...]]
     * Agent format: ['domains' => [['name' => 'x', 'type' => 'main']], 'databases' => [...], 'mailboxes' => [...], 'ssl_certificates' => [...]]
     */
    protected function convertApiDataToAgentFormat(array $apiData): array
    {
        $result = [
            'domains' => [],
            'databases' => [],
            'mailboxes' => [],
            'ssl_certificates' => [],
        ];

        // Convert domains
        $domains = $apiData['domains'] ?? [];
        if (! empty($domains['main'])) {
            $result['domains'][] = ['name' => $domains['main'], 'type' => 'main'];
        }
        foreach ($domains['addon'] ?? [] as $domain) {
            $result['domains'][] = ['name' => $domain, 'type' => 'addon'];
        }
        foreach ($domains['sub'] ?? [] as $domain) {
            $result['domains'][] = ['name' => $domain, 'type' => 'sub'];
        }
        foreach ($domains['parked'] ?? [] as $domain) {
            $result['domains'][] = ['name' => $domain, 'type' => 'parked'];
        }

        // Convert databases (API returns array of database names or objects)
        foreach ($apiData['databases'] ?? [] as $db) {
            $dbName = is_array($db) ? ($db['database'] ?? $db['name'] ?? '') : $db;
            if ($dbName) {
                $result['databases'][] = ['name' => $dbName, 'file' => "mysql/{$dbName}.sql"];
            }
        }

        // Convert email accounts to mailboxes format
        foreach ($apiData['email_accounts'] ?? [] as $email) {
            $emailAddr = is_array($email) ? ($email['email'] ?? '') : $email;
            if ($emailAddr && str_contains($emailAddr, '@')) {
                [$localPart, $domain] = explode('@', $emailAddr, 2);
                $result['mailboxes'][] = [
                    'email' => $emailAddr,
                    'local_part' => $localPart,
                    'domain' => $domain,
                ];
            }
        }

        // Convert SSL certificates - handle various cPanel API response formats
        foreach ($apiData['ssl_certificates'] ?? [] as $cert) {
            if (is_array($cert)) {
                // cPanel API may return 'domain', 'domains' (array), or 'friendly_name'
                $domain = $cert['domain'] ?? $cert['friendly_name'] ?? null;
                if (! $domain && ! empty($cert['domains'])) {
                    // 'domains' can be array or comma-separated string
                    $domains = is_array($cert['domains']) ? $cert['domains'] : explode(',', $cert['domains']);
                    $domain = trim($domains[0] ?? '');
                }
                if ($domain) {
                    $result['ssl_certificates'][] = [
                        'domain' => $domain,
                        'has_key' => true,  // API only lists valid certs
                        'has_cert' => true,
                    ];
                }
            } elseif (is_string($cert) && ! empty($cert)) {
                $result['ssl_certificates'][] = [
                    'domain' => $cert,
                    'has_key' => true,
                    'has_cert' => true,
                ];
            }
        }

        return $result;
    }

    public function validateLocalFile(): void
    {
        if (empty($this->localBackupPath)) {
            Notification::make()
                ->title(__('Missing path'))
                ->body(__('Please enter the backup file path'))
                ->danger()
                ->send();

            return;
        }

        $path = trim($this->localBackupPath);

        // Validate file exists
        if (! file_exists($path)) {
            Notification::make()
                ->title(__('File not found'))
                ->body(__('The specified file does not exist: :path', ['path' => $path]))
                ->danger()
                ->send();

            return;
        }

        // Validate it's a file (not directory)
        if (! is_file($path)) {
            Notification::make()
                ->title(__('Invalid path'))
                ->body(__('The specified path is not a file'))
                ->danger()
                ->send();

            return;
        }

        // Validate extension
        if (! preg_match('/\.(tar\.gz|tgz)$/i', $path)) {
            Notification::make()
                ->title(__('Invalid format'))
                ->body(__('Backup must be a .tar.gz or .tgz file'))
                ->danger()
                ->send();

            return;
        }

        // Validate it's readable
        if (! is_readable($path)) {
            Notification::make()
                ->title(__('File not readable'))
                ->body(__('Cannot read the backup file. Check permissions.'))
                ->danger()
                ->send();

            return;
        }

        // Quick validation - try to list contents (via agent)
        try {
            $result = $this->getAgent()->send('cpanel.validate_backup', [
                'backup_path' => $path,
            ]);
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Invalid backup'))
                ->body($e->getMessage())
                ->danger()
                ->send();

            return;
        }

        if (! ($result['valid'] ?? false)) {
            Notification::make()
                ->title(__('Invalid backup'))
                ->body(__('The file does not appear to be a valid cPanel backup archive'))
                ->danger()
                ->send();

            return;
        }

        // File is valid
        $this->localBackupPath = $path;
        $this->isConnected = true;

        // Set backup path immediately for local files
        $this->backupPath = $path;
        $this->backupFilename = basename($path);
        $this->backupSize = filesize($path) ?: 0;

        Notification::make()
            ->title(__('File validated'))
            ->body(__('Backup file is valid. Size: :size', ['size' => $this->formatBytes($this->backupSize)]))
            ->success()
            ->send();
    }

    public function startBackupTransfer(): void
    {
        if ($this->backupPath) {
            return;
        }

        if ($this->backupInitiated) {
            $this->checkBackupStatus();

            return;
        }

        $this->statusLog = [];
        $this->addStatusLog(__('Starting backup transfer process...'), 'pending');

        $cpanel = $this->getCpanel();
        if (! $cpanel) {
            $this->addStatusLog(__('Error: cPanel credentials not available. Please go back and reconnect.'), 'error');
            Notification::make()
                ->title(__('Connection lost'))
                ->body(__('Please go back to the Connect step and test the connection again.'))
                ->danger()
                ->send();

            return;
        }

        $destPath = $this->getBackupDestPath();

        if (! is_dir($destPath)) {
            mkdir($destPath, 0755, true);
        }

        // Method 1: Try to create backup to homedir and download via HTTP (more reliable)
        try {
            $this->addStatusLog(__('Initiating backup on cPanel (homedir method)...'), 'pending');

            $backupResult = $cpanel->createBackup();
            if (! ($backupResult['success'] ?? false)) {
                throw new Exception($backupResult['message'] ?? __('Failed to start backup'));
            }

            $this->backupInitiated = true;
            $this->backupPid = $backupResult['pid'] ?? null;
            $this->backupMethod = 'download';  // Track which method we're using
            $this->backupInitiatedAt = time();  // Track when backup started
            $this->lastSeenBackupSize = null;  // Reset size tracking
            $this->pollCount = 0;

            $this->addStatusLog(__('Backup initiated on cPanel'), 'success');
            $this->addStatusLog(__('Waiting for backup to complete on cPanel...'), 'pending');

            Notification::make()
                ->title(__('Backup started'))
                ->body(__('cPanel is creating the backup. Once complete, it will be downloaded.'))
                ->info()
                ->send();

            return;
        } catch (Exception $e) {
            Log::warning('Homedir backup failed, trying SCP method', ['error' => $e->getMessage()]);
            $this->addStatusLog(__('Homedir backup failed, trying SCP transfer...'), 'warning');
        }

        // Method 2: Fall back to SCP transfer (requires SSH access on cPanel)
        $this->startScpBackupTransfer();
    }

    protected function startScpBackupTransfer(): void
    {
        $cpanel = $this->getCpanel();
        if (! $cpanel) {
            $this->addStatusLog(__('Error: cPanel credentials not available. Please go back and reconnect.'), 'error');

            return;
        }

        $destPath = $this->getBackupDestPath();

        if (! is_dir($destPath)) {
            mkdir($destPath, 0755, true);
        }

        try {
            $this->addStatusLog(__('Checking Jabali SSH key...'), 'pending');

            $sshKeyResult = $this->getAgent()->send('jabali_ssh.ensure_exists', []);
            if (! ($sshKeyResult['success'] ?? false)) {
                throw new Exception($sshKeyResult['error'] ?? __('Failed to generate Jabali SSH key'));
            }

            $publicKey = $sshKeyResult['public_key'] ?? null;
            $keyName = $sshKeyResult['key_name'] ?? 'jabali-system-key';

            if (! $publicKey) {
                throw new Exception(__('Failed to read Jabali public key'));
            }

            $this->addStatusLog(__('Jabali SSH key ready'), 'success');

            $this->addStatusLog(__('Configuring SSH access on Jabali...'), 'pending');

            $privateKeyResult = $this->getAgent()->send('jabali_ssh.get_private_key', []);
            if (! ($privateKeyResult['success'] ?? false) || ! ($privateKeyResult['exists'] ?? false)) {
                throw new Exception(__('Failed to read Jabali private key'));
            }

            $privateKey = $privateKeyResult['private_key'] ?? null;
            if (! $privateKey) {
                throw new Exception(__('Private key is empty'));
            }

            $authKeysResult = $this->getAgent()->send('jabali_ssh.add_to_authorized_keys', [
                'public_key' => $publicKey,
                'comment' => 'cpanel-migration-'.$this->cpanelUsername,
            ]);
            if (! ($authKeysResult['success'] ?? false)) {
                throw new Exception($authKeysResult['error'] ?? __('Failed to add key to authorized_keys'));
            }

            $this->addStatusLog(__('SSH access configured on Jabali'), 'success');

            $this->addStatusLog(__('Preparing SSH key on cPanel...'), 'pending');

            $cpanel->deleteSshKey($keyName, 'key');
            $cpanel->deleteSshKey($keyName, 'key.pub');

            $this->addStatusLog(__('Importing SSH key to cPanel...'), 'pending');

            $importResult = $cpanel->importSshPrivateKey($keyName, $privateKey);
            if (! ($importResult['success'] ?? false)) {
                throw new Exception($importResult['message'] ?? __('Failed to import SSH key'));
            }
            $this->addStatusLog(__('SSH key imported to cPanel'), 'success');

            $this->addStatusLog(__('Authorizing SSH key...'), 'pending');
            $authResult = $cpanel->authorizeSshKey($keyName);
            if (! ($authResult['success'] ?? false)) {
                $this->addStatusLog(__('SSH key authorization skipped'), 'info');
            } else {
                $this->addStatusLog(__('SSH key authorized'), 'success');
            }

            $this->addStatusLog(__('Initiating backup on cPanel (SCP method)...'), 'pending');

            $jabaliIp = $this->getJabaliPublicIp();

            $backupResult = $cpanel->createBackupToScpWithKey(
                $jabaliIp,
                'root',
                $destPath,
                $keyName,
                22
            );

            if (! ($backupResult['success'] ?? false)) {
                throw new Exception($backupResult['message'] ?? __('Failed to start backup'));
            }

            $this->backupInitiated = true;
            $this->backupPid = $backupResult['pid'] ?? null;
            $this->backupMethod = 'scp';
            $this->backupInitiatedAt = time();
            $this->lastSeenBackupSize = null;
            $this->pollCount = 0;

            $this->addStatusLog(__('Backup initiated on cPanel'), 'success');
            $this->addStatusLog(__('Waiting for backup file to arrive...'), 'pending');

            Notification::make()
                ->title(__('Backup transfer started'))
                ->body(__('cPanel is creating and transferring the backup. This may take several minutes.'))
                ->info()
                ->send();
        } catch (Exception $e) {
            Log::error('Backup transfer failed', ['error' => $e->getMessage()]);
            $this->addStatusLog(__('Error: :message', ['message' => $e->getMessage()]), 'error');

            Notification::make()
                ->title(__('Backup transfer failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function checkBackupStatus(): void
    {
        $this->pollCount++;
        $destPath = $this->getBackupDestPath();

        // For download method, we need to check cPanel for backup completion then download
        if ($this->backupMethod === 'download') {
            $this->checkAndDownloadBackup($destPath);

            return;
        }

        // For SCP method, check for file arrival
        $files = glob($destPath.'/backup-*.tar.gz');

        // Filter to only files created after backup was initiated
        if ($this->backupInitiatedAt) {
            $files = array_filter($files, function ($file) {
                return filemtime($file) >= ($this->backupInitiatedAt - 60);
            });
            $files = array_values($files);
        }

        if (! empty($files)) {
            usort($files, fn ($a, $b) => filemtime($b) - filemtime($a));

            $backupFile = $files[0];

            $size1 = filesize($backupFile);
            usleep(500000);
            clearstatcache(true, $backupFile);
            $size2 = filesize($backupFile);

            if ($size1 !== $size2) {
                $this->addStatusLog(__('Receiving backup file... (:size)', ['size' => $this->formatBytes($size2)]), 'pending');

                return;
            }

            // Fix file permissions (SCP creates files as root, need agent to fix)
            $this->getAgent()->send('cpanel.fix_backup_permissions', [
                'backup_path' => $backupFile,
            ]);

            // Verify the file is a valid gzip archive
            $handle = fopen($backupFile, 'rb');
            $magic = $handle ? fread($handle, 2) : '';
            if ($handle) {
                fclose($handle);
            }

            if ($magic !== "\x1f\x8b") {
                $this->addStatusLog(__('Received invalid backup file, waiting...'), 'pending');
                @unlink($backupFile);

                return;
            }

            $this->backupPath = $backupFile;
            $this->backupFilename = basename($backupFile);
            $this->backupSize = filesize($backupFile);

            $this->addStatusLog(__('Backup file received'), 'success');

            $this->cleanupCpanelSshKey();

            // Always analyze backup to get accurate data (API may not have all permissions)
            $this->analyzeBackup();

            Notification::make()
                ->title(__('Backup received'))
                ->body(__('Backup file :name (:size) is ready. Click Next to continue.', [
                    'name' => $this->backupFilename,
                    'size' => $this->formatBytes($this->backupSize),
                ]))
                ->success()
                ->send();
        } else {
            $this->addStatusLog(__('Waiting for backup file... (check :count)', ['count' => $this->pollCount]), 'pending');
        }
    }

    protected function checkAndDownloadBackup(string $destPath): void
    {
        try {
            $cpanel = $this->getCpanel();

            // Check backup status on cPanel
            $statusResult = $cpanel->getBackupStatus();

            if (! ($statusResult['success'] ?? false)) {
                $this->addStatusLog(__('Checking backup status... (attempt :count)', ['count' => $this->pollCount]), 'pending');

                return;
            }

            // Check if backup is still in progress
            if ($statusResult['in_progress'] ?? false) {
                $this->addStatusLog(__('Backup in progress on cPanel... (check :count)', ['count' => $this->pollCount]), 'pending');

                return;
            }

            // Look for completed backup files
            $backups = $statusResult['backups'] ?? [];
            if (empty($backups)) {
                // Also try listBackups for older cPanel versions
                $listResult = $cpanel->listBackups();
                $backups = $listResult['backups'] ?? [];
            }

            // Filter to only backups created AFTER we initiated the backup
            // This prevents picking up old backup files
            if ($this->backupInitiatedAt) {
                $backups = array_filter($backups, function ($backup) {
                    $mtime = $backup['mtime'] ?? 0;

                    // Allow 60 second buffer before initiation time
                    return $mtime >= ($this->backupInitiatedAt - 60);
                });
                $backups = array_values($backups);  // Re-index array
            }

            if (empty($backups)) {
                $this->addStatusLog(__('Waiting for backup to complete... (check :count)', ['count' => $this->pollCount]), 'pending');

                return;
            }

            // Get the most recent backup
            $latestBackup = $backups[0];
            $remoteFilename = $latestBackup['name'] ?? $latestBackup['file'] ?? '';
            $remotePath = $latestBackup['path'] ?? "/home/{$this->cpanelUsername}/{$remoteFilename}";
            $currentSize = (int) ($latestBackup['size'] ?? 0);

            if (empty($remoteFilename)) {
                $this->addStatusLog(__('Waiting for backup file... (check :count)', ['count' => $this->pollCount]), 'pending');

                return;
            }

            // Check if file size has stabilized (backup still being written)
            // Require size to be stable AND at least 100 KB (real backups are much larger)
            $minBackupSize = 100 * 1024;  // 100 KB minimum
            if ($this->lastSeenBackupSize !== $currentSize || $currentSize < $minBackupSize) {
                $this->lastSeenBackupSize = $currentSize;
                $this->addStatusLog(__('Backup in progress... :size (check :count)', [
                    'size' => $this->formatBytes($currentSize),
                    'count' => $this->pollCount,
                ]), 'pending');

                return;
            }

            // Size is stable and large enough - backup is complete
            $this->addStatusLog(__('Backup complete on cPanel: :name (:size)', [
                'name' => $remoteFilename,
                'size' => $this->formatBytes($currentSize),
            ]), 'success');
            $this->addStatusLog(__('Downloading backup file...'), 'pending');

            // Download the backup
            $localPath = $destPath.'/'.$remoteFilename;
            $downloadResult = $cpanel->downloadFileToPath($remotePath, $localPath, function ($downloaded, $total) {
                $percent = $total > 0 ? round(($downloaded / $total) * 100) : 0;
                $this->addStatusLog(__('Downloading... :percent% (:downloaded / :total)', [
                    'percent' => $percent,
                    'downloaded' => $this->formatBytes($downloaded),
                    'total' => $this->formatBytes($total),
                ]), 'pending');
            });

            if (! ($downloadResult['success'] ?? false)) {
                throw new Exception($downloadResult['message'] ?? __('Download failed'));
            }

            // Verify the downloaded file is actually a gzip archive (not an HTML error page)
            $handle = fopen($localPath, 'rb');
            $magic = $handle ? fread($handle, 2) : '';
            if ($handle) {
                fclose($handle);
            }

            // Gzip magic bytes: 0x1f 0x8b
            if ($magic !== "\x1f\x8b") {
                @unlink($localPath);  // Delete invalid file

                // Clean up any old/invalid backup files in the destination
                $oldFiles = glob($destPath.'/backup-*.tar.gz');
                foreach ($oldFiles as $oldFile) {
                    @unlink($oldFile);
                }

                $this->addStatusLog(__('HTTP download blocked (403 Forbidden). Switching to SCP...'), 'warning');

                // Reset state and switch to SCP method
                $this->backupInitiated = false;
                $this->backupMethod = 'scp';
                $this->lastSeenBackupSize = null;
                $this->pollCount = 0;

                // Trigger SCP transfer
                $this->startScpBackupTransfer();

                return;
            }

            $this->backupPath = $localPath;
            $this->backupFilename = $remoteFilename;
            $this->backupSize = filesize($localPath);

            $this->addStatusLog(__('Backup downloaded successfully'), 'success');

            // Always analyze backup to get accurate data (API may not have all permissions)
            $this->analyzeBackup();

            Notification::make()
                ->title(__('Backup downloaded'))
                ->body(__('Backup file :name (:size) is ready. Click Next to continue.', [
                    'name' => $this->backupFilename,
                    'size' => $this->formatBytes($this->backupSize),
                ]))
                ->success()
                ->send();
        } catch (Exception $e) {
            Log::error('Backup download failed', ['error' => $e->getMessage()]);
            $this->addStatusLog(__('Download error: :message', ['message' => $e->getMessage()]), 'error');

            Notification::make()
                ->title(__('Download failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    protected function addStatusLog(string $message, string $status = 'info'): void
    {
        $lastIndex = count($this->statusLog) - 1;
        if ($lastIndex >= 0 && $this->statusLog[$lastIndex]['status'] === 'pending' && $status !== 'pending') {
            $this->statusLog[$lastIndex] = [
                'message' => $message,
                'status' => $status,
                'time' => now()->format('H:i:s'),
            ];

            return;
        }

        if ($status === 'pending') {
            $this->statusLog = array_filter($this->statusLog, fn ($entry) => $entry['status'] !== 'pending' || ! str_contains($entry['message'], 'Waiting for backup'));
            $this->statusLog = array_values($this->statusLog);
        }

        $this->statusLog[] = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];
    }

    protected function cleanupCpanelSshKey(): void
    {
        try {
            $keyName = 'jabali-system-key';
            $cpanel = $this->getCpanel();

            $cpanel->deleteSshKey($keyName, 'key');
            $cpanel->deleteSshKey($keyName, 'key.pub');

            $this->addStatusLog(__('SSH key removed from cPanel'), 'success');
        } catch (Exception $e) {
            Log::warning('Failed to cleanup cPanel SSH key: '.$e->getMessage());
        }
    }

    public function analyzeBackup(): void
    {
        if (! $this->backupPath) {
            return;
        }

        $this->addStatusLog(__('Analyzing backup contents...'), 'pending');

        try {
            $result = $this->getAgent()->send('cpanel.analyze_backup', [
                'backup_path' => $this->backupPath,
            ]);

            if ($result['success'] ?? false) {
                $this->discoveredData = $result['data'] ?? [];
                $this->addStatusLog(__('Backup analyzed: :domains domains, :dbs databases, :mailboxes mailboxes', [
                    'domains' => count($this->discoveredData['domains'] ?? []),
                    'dbs' => count($this->discoveredData['databases'] ?? []),
                    'mailboxes' => count($this->discoveredData['mailboxes'] ?? []),
                ]), 'success');
            } else {
                throw new Exception($result['error'] ?? __('Failed to analyze backup'));
            }
        } catch (Exception $e) {
            Log::error('Backup analysis failed', ['error' => $e->getMessage()]);
            $this->addStatusLog(__('Analysis error: :message', ['message' => $e->getMessage()]), 'error');
        }
    }

    public function analyzeLocalBackup(): void
    {
        if (! $this->backupPath) {
            Notification::make()
                ->title(__('No backup file'))
                ->body(__('Please validate a backup file first'))
                ->danger()
                ->send();

            return;
        }

        // Reset and start analysis
        $this->analysisLog = [];
        $this->isAnalyzing = true;
        $this->discoveredData = [];

        $this->addAnalysisLog(__('Starting backup analysis...'), 'pending');

        try {
            // Step 1: Extracting backup
            $this->addAnalysisLog(__('Extracting backup archive...'), 'pending');

            $result = $this->getAgent()->send('cpanel.analyze_backup', [
                'backup_path' => $this->backupPath,
            ]);

            if ($result['success'] ?? false) {
                $this->addAnalysisLog(__('Backup archive extracted'), 'success');

                $this->discoveredData = $result['data'] ?? [];

                // Extract cPanel username from backup analysis (needed for user creation)
                if (! empty($this->discoveredData['cpanel_username'])) {
                    $this->cpanelUsername = $this->discoveredData['cpanel_username'];
                    $this->addAnalysisLog(__('cPanel user: :user', ['user' => $this->cpanelUsername]), 'success');
                }

                // Show what was found
                $domainCount = count($this->discoveredData['domains'] ?? []);
                $dbCount = count($this->discoveredData['databases'] ?? []);
                $mailCount = count($this->discoveredData['mailboxes'] ?? []);
                $sslCount = count($this->discoveredData['ssl_certificates'] ?? []);

                if ($domainCount > 0) {
                    $this->addAnalysisLog(__('Found :count domain(s)', ['count' => $domainCount]), 'success');
                }
                if ($dbCount > 0) {
                    $this->addAnalysisLog(__('Found :count database(s)', ['count' => $dbCount]), 'success');
                }
                if ($mailCount > 0) {
                    $this->addAnalysisLog(__('Found :count mailbox(es)', ['count' => $mailCount]), 'success');
                }
                if ($sslCount > 0) {
                    $this->addAnalysisLog(__('Found :count SSL certificate(s)', ['count' => $sslCount]), 'success');
                }

                $this->addAnalysisLog(__('Analysis complete'), 'success');

                Notification::make()
                    ->title(__('Analysis complete'))
                    ->body(__('Found :domains domains, :dbs databases, :mailboxes mailboxes. Click Next to continue.', [
                        'domains' => $domainCount,
                        'dbs' => $dbCount,
                        'mailboxes' => $mailCount,
                    ]))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to analyze backup'));
            }
        } catch (Exception $e) {
            Log::error('Local backup analysis failed', ['error' => $e->getMessage()]);
            $this->addAnalysisLog(__('Error: :message', ['message' => $e->getMessage()]), 'error');

            Notification::make()
                ->title(__('Analysis failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        } finally {
            $this->isAnalyzing = false;
        }
    }

    public function startRestore(): void
    {
        if (! $this->backupPath) {
            return;
        }

        $this->isProcessing = true;
        $this->migrationLog = [];
        $this->restoreStatus = 'queued';

        // Get or create the target user
        $user = null;
        if ($this->userMode === 'create') {
            $this->addLog(__('Creating new user from cPanel backup...'), 'pending');
            $user = $this->createUserFromBackup();
            if (! $user) {
                Notification::make()
                    ->title(__('User creation failed'))
                    ->body(__('Could not create user from backup'))
                    ->danger()
                    ->send();
                $this->isProcessing = false;

                return;
            }
        } else {
            $user = $this->getTargetUser();
            if (! $user) {
                Notification::make()
                    ->title(__('No user selected'))
                    ->body(__('Please select a target user'))
                    ->danger()
                    ->send();
                $this->isProcessing = false;

                return;
            }
        }

        $this->enqueueRestore($user);
    }

    protected function addLog(string $message, string $status = 'info'): void
    {
        $this->migrationLog[] = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];
    }

    protected function enqueueRestore(User $user): void
    {
        $this->restoreJobId = (string) Str::uuid();
        $logDir = storage_path('app/migrations/cpanel');
        File::ensureDirectoryExists($logDir);
        $this->restoreLogPath = $logDir.'/'.$this->restoreJobId.'.log';
        File::put($this->restoreLogPath, '');
        @chmod($this->restoreLogPath, 0644);

        $this->appendMigrationLog(__('Restore queued for user: :user', ['user' => $user->username]), 'pending');

        Cache::put($this->getRestoreCacheKey(), ['status' => 'queued'], now()->addHours(2));
        session()->put('cpanel_restore_job_id', $this->restoreJobId);
        session()->put('cpanel_restore_log_path', $this->restoreLogPath);
        session()->put('cpanel_restore_processing', true);
        session()->save();

        RunCpanelRestore::dispatch(
            jobId: $this->restoreJobId,
            logPath: $this->restoreLogPath,
            backupPath: $this->backupPath,
            username: $user->username,
            restoreFiles: $this->restoreFiles,
            restoreDatabases: $this->restoreDatabases,
            restoreEmails: $this->restoreEmails,
            restoreSsl: $this->restoreSsl,
            discoveredData: ! empty($this->discoveredData) ? $this->discoveredData : null,
        );
    }

    public function pollMigrationLog(): void
    {
        if (! $this->restoreJobId || ! $this->restoreLogPath) {
            return;
        }

        $this->migrationLog = $this->readMigrationLog($this->restoreLogPath);

        $status = Cache::get($this->getRestoreCacheKey());
        if (is_array($status)) {
            $this->restoreStatus = $status['status'] ?? $this->restoreStatus;
        }

        if (in_array($this->restoreStatus, ['completed', 'failed'], true)) {
            $this->isProcessing = false;
            session()->forget(['cpanel_restore_job_id', 'cpanel_restore_log_path', 'cpanel_restore_processing']);
            session()->save();
        }
    }

    protected function readMigrationLog(string $path): array
    {
        if (! file_exists($path)) {
            return [];
        }

        $entries = [];
        $lines = file($path, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES);
        foreach ($lines as $line) {
            $decoded = json_decode($line, true);
            if (is_array($decoded) && isset($decoded['message'], $decoded['status'])) {
                $entries[] = [
                    'message' => $decoded['message'],
                    'status' => $decoded['status'],
                    'time' => $decoded['time'] ?? now()->format('H:i:s'),
                ];
            }
        }

        return $entries;
    }

    protected function appendMigrationLog(string $message, string $status): void
    {
        $entry = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];

        $this->migrationLog[] = $entry;

        if ($this->restoreLogPath) {
            file_put_contents(
                $this->restoreLogPath,
                json_encode($entry).PHP_EOL,
                FILE_APPEND | LOCK_EX
            );
            @chmod($this->restoreLogPath, 0644);
        }
    }

    protected function getRestoreCacheKey(): string
    {
        return 'cpanel_restore_status_'.$this->restoreJobId;
    }

    protected function restoreMigrationStateFromSession(): void
    {
        $this->restoreJobId = session()->get('cpanel_restore_job_id');
        $this->restoreLogPath = session()->get('cpanel_restore_log_path');
        $this->isProcessing = (bool) session()->get('cpanel_restore_processing', false);

        if ($this->restoreJobId && $this->restoreLogPath) {
            $this->pollMigrationLog();
        }
    }

    public function resetMigration(): void
    {
        $this->userMode = 'create';
        $this->targetUserId = null;
        $this->sourceType = 'remote';
        $this->localBackupPath = null;
        $this->hostname = null;
        $this->cpanelUsername = null;
        $this->apiToken = null;
        $this->port = 2083;
        $this->useSSL = true;
        $this->isConnected = false;
        $this->connectionInfo = [];
        $this->backupInitiated = false;
        $this->backupPid = null;
        $this->backupFilename = null;
        $this->backupPath = null;
        $this->backupSize = 0;
        $this->pollCount = 0;
        $this->discoveredData = [];
        $this->restoreFiles = true;
        $this->restoreDatabases = true;
        $this->restoreEmails = true;
        $this->restoreSsl = true;
        $this->isProcessing = false;
        $this->isAnalyzing = false;
        $this->migrationLog = [];
        $this->statusLog = [];
        $this->analysisLog = [];
        $this->cpanel = null;
        $this->restoreJobId = null;
        $this->restoreLogPath = null;
        $this->restoreStatus = null;

        // Clear session credentials
        $this->clearSessionCredentials();
        session()->forget(['cpanel_restore_job_id', 'cpanel_restore_log_path', 'cpanel_restore_processing']);
        session()->save();

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
