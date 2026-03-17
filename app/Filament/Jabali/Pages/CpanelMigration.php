<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Jobs\RunCpanelRestore;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Migration\CpanelApiService;
use App\Support\Formatter;
use App\Support\SafeError;
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
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Actions as FormActions;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\Wizard;
use Filament\Schemas\Components\Wizard\Step;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Livewire\Attributes\Url;

class CpanelMigration extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-arrow-down-tray';

    protected static ?string $navigationLabel = null;

    protected static bool $shouldRegisterNavigation = false;

    public static function getNavigationLabel(): string
    {
        return __('cPanel Migration');
    }

    protected static ?int $navigationSort = 15;

    protected string $view = 'filament.jabali.pages.cpanel-migration';

    #[Url(as: 'cpanel-step')]
    public ?string $wizardStep = null;

    public bool $step1Complete = false;

    public string $sourceType = 'remote';

    public ?string $localBackupPath = null;

    public array $availableBackups = [];

    // Connection form (Step 1)
    public ?string $hostname = null;

    public ?string $cpanelUsername = null;

    public ?string $apiToken = null;

    public int $port = 2083;

    public bool $useSSL = true;

    // Backup status (Step 2)
    public bool $isConnected = false;

    public array $connectionInfo = [];

    public bool $backupInitiated = false;

    public ?string $backupPid = null;

    public bool $backupInProgress = false;

    public ?string $remoteBackupPath = null;

    public ?string $backupFilename = null;

    public ?string $backupPath = null;

    public int $backupSize = 0;

    public int $downloadProgress = 0;

    // Discovered data from backup
    public array $discoveredData = [];

    public array $statusLog = [];

    public array $analysisLog = [];

    public bool $isAnalyzing = false;

    // Restore options (Step 3)
    public bool $restoreFiles = true;

    public bool $restoreDatabases = true;

    public bool $restoreEmails = true;

    public bool $restoreSsl = true;

    // Status
    public bool $isProcessing = false;

    public array $migrationLog = [];

    public ?string $restoreJobId = null;

    public ?string $restoreLogPath = null;

    public ?string $restoreStatus = null;

    public int $pollCount = 0;

    protected ?CpanelApiService $cpanel = null;

    public function getTitle(): string|Htmlable
    {
        return __('cPanel Migration');
    }

    public function getSubheading(): ?string
    {
        return __('Migrate your complete cPanel account including files, databases, and emails');
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

    public function updatedSourceType(): void
    {
        $this->backupInitiated = false;
        $this->backupPid = null;
        $this->backupInProgress = false;
        $this->remoteBackupPath = null;
        $this->backupFilename = null;
        $this->backupPath = null;
        $this->backupSize = 0;
        $this->downloadProgress = 0;
        $this->discoveredData = [];
        $this->statusLog = [];
        $this->analysisLog = [];
        $this->isAnalyzing = false;
        $this->step1Complete = false;
        $this->localBackupPath = null;

        if ($this->sourceType === 'local') {
            $this->loadLocalBackups();
        }

        $this->storeCredentialsInSession();
    }

    public function updatedLocalBackupPath(): void
    {
        if (! $this->localBackupPath) {
            $this->backupFilename = null;
            $this->backupPath = null;
            $this->backupSize = 0;
            $this->isConnected = false;
            $this->discoveredData = [];
            $this->statusLog = [];
            $this->analysisLog = [];
            $this->storeCredentialsInSession();

            return;
        }

        $this->selectLocalBackup();
    }

    public function getAgent(): AgentClient
    {
        return app(AgentClient::class);
    }

    protected function getUser(): User
    {
        return Auth::user();
    }

    protected function resetConnection(): void
    {
        $this->cpanel = null;
        $this->isConnected = false;
        $this->connectionInfo = [];
    }

    protected function getCpanel(): ?CpanelApiService
    {
        if (! $this->hostname || ! $this->cpanelUsername || ! $this->apiToken) {
            $this->restoreCredentialsFromSession();
        }

        if (! $this->hostname || ! $this->cpanelUsername || ! $this->apiToken) {
            return null;
        }

        return $this->cpanel ??= new CpanelApiService(
            $this->hostname,
            $this->cpanelUsername,
            $this->apiToken,
            $this->port,
            $this->useSSL
        );
    }

    protected function getBackupDestPath(): string
    {
        $user = $this->getUser();

        return "/home/{$user->username}/cpanel-migration";
    }

    protected function getLocalBackupRoot(): string
    {
        $user = $this->getUser();

        return "/home/{$user->username}/backups";
    }

    protected function loadLocalBackups(): void
    {
        $this->availableBackups = [];

        $result = $this->getAgent()->send('file.list', [
            'username' => $this->getUser()->username,
            'path' => 'backups',
        ]);

        if (! ($result['success'] ?? false)) {
            $this->getAgent()->send('file.mkdir', [
                'username' => $this->getUser()->username,
                'path' => 'backups',
            ]);

            $result = $this->getAgent()->send('file.list', [
                'username' => $this->getUser()->username,
                'path' => 'backups',
            ]);

            if (! ($result['success'] ?? false)) {
                return;
            }
        }

        $items = $result['items'] ?? [];
        foreach ($items as $item) {
            if (($item['is_dir'] ?? false) === true) {
                continue;
            }

            $name = (string) ($item['name'] ?? '');
            if (! preg_match('/\.(tar\.gz|tgz)$/i', $name)) {
                continue;
            }

            $this->availableBackups[] = $item;
        }
    }

    public function refreshLocalBackups(): void
    {
        $this->loadLocalBackups();

        Notification::make()
            ->title(__('Backup list refreshed'))
            ->success()
            ->send();
    }

    protected function getLocalBackupOptions(): array
    {
        $options = [];
        foreach ($this->availableBackups as $item) {
            $path = $item['path'] ?? null;
            $name = $item['name'] ?? null;
            if (! $path || ! $name) {
                continue;
            }

            $size = Formatter::bytes((int) ($item['size'] ?? 0));
            $options[$path] = "{$name} ({$size})";
        }

        return $options;
    }

    public function selectLocalBackup(): void
    {
        if (! $this->localBackupPath) {
            return;
        }

        $info = $this->getAgent()->send('file.info', [
            'username' => $this->getUser()->username,
            'path' => $this->localBackupPath,
        ]);

        if (! ($info['success'] ?? false)) {
            Notification::make()
                ->title(__('Backup file not found'))
                ->body($info['error'] ?? __('Unable to read backup file'))
                ->danger()
                ->send();

            return;
        }

        $details = $info['info'] ?? [];
        if (! ($details['is_file'] ?? false)) {
            Notification::make()
                ->title(__('Invalid backup selection'))
                ->body(__('Please select a backup file'))
                ->warning()
                ->send();

            return;
        }

        $this->backupFilename = $details['name'] ?? basename($this->localBackupPath);
        $this->backupSize = (int) ($details['size'] ?? 0);
        $this->backupPath = "/home/{$this->getUser()->username}/{$this->localBackupPath}";
        $this->isConnected = true;

        $this->statusLog = [];
        $this->analysisLog = [];
        $this->discoveredData = [];
        $this->addStatusLog(__('Selected local backup: :name', ['name' => $this->backupFilename]), 'success');

        $this->storeCredentialsInSession();
    }

    protected function storeCredentialsInSession(): void
    {
        session()->put('user_cpanel_migration.sourceType', $this->sourceType);
        session()->put('user_cpanel_migration.localBackupPath', $this->localBackupPath);
        session()->put('user_cpanel_migration.hostname', $this->hostname);
        session()->put('user_cpanel_migration.username', $this->cpanelUsername);
        session()->put('user_cpanel_migration.token', $this->apiToken);
        session()->put('user_cpanel_migration.port', $this->port);
        session()->put('user_cpanel_migration.useSSL', $this->useSSL);
        session()->put('user_cpanel_migration.isConnected', $this->isConnected);
        session()->put('user_cpanel_migration.connectionInfo', $this->connectionInfo);
        session()->put('user_cpanel_migration.backupInitiated', $this->backupInitiated);
        session()->put('user_cpanel_migration.backupPid', $this->backupPid);
        session()->put('user_cpanel_migration.backupInProgress', $this->backupInProgress);
        session()->put('user_cpanel_migration.remoteBackupPath', $this->remoteBackupPath);
        session()->put('user_cpanel_migration.backupFilename', $this->backupFilename);
        session()->put('user_cpanel_migration.backupPath', $this->backupPath);
        session()->put('user_cpanel_migration.backupSize', $this->backupSize);
        session()->put('user_cpanel_migration.downloadProgress', $this->downloadProgress);
        session()->put('user_cpanel_migration.discoveredData', $this->discoveredData);
        session()->put('user_cpanel_migration.statusLog', $this->statusLog);
        session()->put('user_cpanel_migration.analysisLog', $this->analysisLog);
        session()->put('user_cpanel_migration.isAnalyzing', $this->isAnalyzing);
        session()->put('user_cpanel_migration.step1Complete', $this->step1Complete);
        session()->put('user_cpanel_migration.pollCount', $this->pollCount);

        session()->save();
    }

    protected function restoreCredentialsFromSession(): void
    {
        if (! session()->has('user_cpanel_migration.sourceType') && ! session()->has('user_cpanel_migration.hostname')) {
            return;
        }

        $this->sourceType = session('user_cpanel_migration.sourceType', 'remote');
        $this->localBackupPath = session('user_cpanel_migration.localBackupPath');
        $this->hostname = session('user_cpanel_migration.hostname');
        $this->cpanelUsername = session('user_cpanel_migration.username');
        $this->apiToken = session('user_cpanel_migration.token');
        $this->port = session('user_cpanel_migration.port', 2083);
        $this->useSSL = session('user_cpanel_migration.useSSL', true);
        $this->isConnected = session('user_cpanel_migration.isConnected', false);
        $this->connectionInfo = session('user_cpanel_migration.connectionInfo', []);
        $this->backupInitiated = session('user_cpanel_migration.backupInitiated', false);
        $this->backupPid = session('user_cpanel_migration.backupPid');
        $this->backupInProgress = session('user_cpanel_migration.backupInProgress', false);
        $this->remoteBackupPath = session('user_cpanel_migration.remoteBackupPath');
        $this->backupFilename = session('user_cpanel_migration.backupFilename');
        $this->backupPath = session('user_cpanel_migration.backupPath');
        $this->backupSize = session('user_cpanel_migration.backupSize', 0);
        $this->downloadProgress = session('user_cpanel_migration.downloadProgress', 0);
        $this->discoveredData = session('user_cpanel_migration.discoveredData', []);
        $this->statusLog = session('user_cpanel_migration.statusLog', []);
        $this->analysisLog = session('user_cpanel_migration.analysisLog', []);
        $this->isAnalyzing = session('user_cpanel_migration.isAnalyzing', false);
        $this->step1Complete = session('user_cpanel_migration.step1Complete', false);
        $this->pollCount = session('user_cpanel_migration.pollCount', 0);
    }

    protected function clearSessionCredentials(): void
    {
        session()->forget([
            'user_cpanel_migration.sourceType',
            'user_cpanel_migration.localBackupPath',
            'user_cpanel_migration.hostname',
            'user_cpanel_migration.username',
            'user_cpanel_migration.token',
            'user_cpanel_migration.port',
            'user_cpanel_migration.useSSL',
            'user_cpanel_migration.isConnected',
            'user_cpanel_migration.connectionInfo',
            'user_cpanel_migration.backupInitiated',
            'user_cpanel_migration.backupPid',
            'user_cpanel_migration.backupInProgress',
            'user_cpanel_migration.remoteBackupPath',
            'user_cpanel_migration.backupFilename',
            'user_cpanel_migration.backupPath',
            'user_cpanel_migration.backupSize',
            'user_cpanel_migration.downloadProgress',
            'user_cpanel_migration.discoveredData',
            'user_cpanel_migration.statusLog',
            'user_cpanel_migration.analysisLog',
            'user_cpanel_migration.isAnalyzing',
            'user_cpanel_migration.step1Complete',
            'user_cpanel_migration.pollCount',
            'user_cpanel_restore_job_id',
            'user_cpanel_restore_log_path',
            'user_cpanel_restore_processing',
        ]);
    }

    protected function restoreMigrationStateFromSession(): void
    {
        $this->restoreCredentialsFromSession();

        if ($this->sourceType === 'local') {
            $this->loadLocalBackups();
        }

        $this->restoreJobId = session('user_cpanel_restore_job_id');
        $this->restoreLogPath = session('user_cpanel_restore_log_path');
        $this->isProcessing = (bool) session('user_cpanel_restore_processing', false);

        if ($this->restoreJobId && $this->restoreLogPath) {
            $this->migrationLog = $this->readMigrationLog($this->restoreLogPath);

            $status = Cache::get($this->getRestoreCacheKey());
            if (is_array($status)) {
                $this->restoreStatus = $status['status'] ?? $this->restoreStatus;
            }
        }
    }

    protected function getRestoreCacheKey(): string
    {
        return $this->restoreJobId ? 'cpanel_restore_status_'.$this->restoreJobId : '';
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
                ->nextAction(fn (Action $action) => $action->disabled(fn () => $this->isNextStepDisabled()))
                ->persistStepInQueryString('cpanel-step'),
        ]);
    }

    protected function isNextStepDisabled(): bool
    {
        if (! $this->step1Complete) {
            return ! $this->isConnected;
        }

        if (! $this->backupPath || empty($this->discoveredData)) {
            return true;
        }

        return false;
    }

    protected function getConnectStep(): Step
    {
        return Step::make(__('Connect'))
            ->id('connect')
            ->icon('heroicon-o-link')
            ->description(__('Enter cPanel credentials'))
            ->schema([
                Section::make(__('Migration Source'))
                    ->icon('heroicon-o-arrow-path')
                    ->schema([
                        Radio::make('sourceType')
                            ->label(__('Source Type'))
                            ->options([
                                'remote' => __('Remote cPanel Server'),
                                'local' => __('Local Backup File'),
                            ])
                            ->default('remote')
                            ->inline()
                            ->live(),
                    ]),

                Section::make(__('cPanel Credentials'))
                    ->description(__('Enter the cPanel server connection details'))
                    ->icon('heroicon-o-link')
                    ->visible(fn () => $this->sourceType === 'remote')
                    ->schema([
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            TextInput::make('hostname')
                                ->label(__('cPanel Hostname'))
                                ->placeholder(__('cpanel.example.com'))
                                ->required()
                                ->helperText(__('Your cPanel server hostname or IP address')),
                            TextInput::make('port')
                                ->label(__('Port'))
                                ->numeric()
                                ->default(2083)
                                ->required()
                                ->helperText(__('Usually 2083 for SSL or 2082 without')),
                        ]),
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            TextInput::make('cpanelUsername')
                                ->label(__('cPanel Username'))
                                ->required()
                                ->helperText(__('Your cPanel account username')),
                            TextInput::make('apiToken')
                                ->label(__('API Token'))
                                ->password()
                                ->required()
                                ->revealable()
                                ->helperText(__('Generate from cPanel → Security → Manage API Tokens')),
                        ]),
                        Checkbox::make('useSSL')
                            ->label(__('Use SSL (HTTPS)'))
                            ->default(true)
                            ->helperText(__('Recommended. Disable only if your cPanel does not support SSL.')),

                        FormActions::make([
                            Action::make('testConnection')
                                ->label(__('Test Connection'))
                                ->icon('heroicon-o-signal')
                                ->color($this->isConnected ? 'success' : 'primary')
                                ->action('testConnection'),
                        ])->alignEnd(),

                        Section::make(__('Connection Successful'))
                            ->icon('heroicon-o-check-circle')
                            ->iconColor('success')
                            ->visible(fn () => $this->isConnected)
                            ->schema([
                                Text::make(__('You can proceed to the next step.')),
                                Grid::make(['default' => 2, 'sm' => 4])->schema([
                                    Section::make((string) ($this->connectionInfo['domains'] ?? 0))
                                        ->description(__('Domains'))
                                        ->icon('heroicon-o-globe-alt')
                                        ->iconColor('primary')
                                        ->compact(),
                                    Section::make((string) ($this->connectionInfo['databases'] ?? 0))
                                        ->description(__('Databases'))
                                        ->icon('heroicon-o-circle-stack')
                                        ->iconColor('warning')
                                        ->compact(),
                                    Section::make((string) ($this->connectionInfo['emails'] ?? 0))
                                        ->description(__('Mailboxes'))
                                        ->icon('heroicon-o-envelope')
                                        ->iconColor('success')
                                        ->compact(),
                                    Section::make((string) ($this->connectionInfo['ssl'] ?? 0))
                                        ->description(__('SSL Certs'))
                                        ->icon('heroicon-o-lock-closed')
                                        ->iconColor('info')
                                        ->compact(),
                                ]),
                            ]),
                    ]),

                Section::make(__('Local Backup'))
                    ->description(__('Select a backup file from your home backups folder'))
                    ->icon('heroicon-o-folder-open')
                    ->visible(fn () => $this->sourceType === 'local')
                    ->headerActions([
                        Action::make('refreshLocalBackups')
                            ->label(__('Refresh'))
                            ->icon('heroicon-o-arrow-path')
                            ->color('gray')
                            ->action('refreshLocalBackups'),
                    ])
                    ->schema([
                        Text::make(__('Folder: :path', ['path' => $this->getLocalBackupRoot()])),
                        Select::make('localBackupPath')
                            ->label(__('Backup File'))
                            ->options($this->getLocalBackupOptions())
                            ->searchable()
                            ->placeholder(__('Select a backup file'))
                            ->required(),
                        Text::make(__('No backups found in the folder. Upload a cPanel backup file to continue.'))
                            ->color('gray')
                            ->visible(fn () => empty($this->availableBackups)),
                    ]),
            ])
            ->afterValidation(function () {
                if ($this->sourceType === 'local') {
                    if (! $this->backupPath) {
                        Notification::make()
                            ->title(__('Backup required'))
                            ->body(__('Please select a local backup file before proceeding'))
                            ->danger()
                            ->send();
                        throw new Exception(__('Please select a local backup file'));
                    }
                } elseif (! $this->isConnected) {
                    Notification::make()
                        ->title(__('Connection required'))
                        ->body(__('Please test the connection before proceeding'))
                        ->danger()
                        ->send();
                    throw new Exception(__('Please test the connection first'));
                }

                $this->step1Complete = true;
                $this->storeCredentialsInSession();
            });
    }

    protected function getBackupStep(): Step
    {
        if ($this->sourceType === 'local') {
            return Step::make(__('Backup'))
                ->id('backup')
                ->icon('heroicon-o-folder-open')
                ->description(__('Analyze local backup'))
                ->schema([
                    Section::make(__('Local Backup'))
                        ->description(__('Analyze the selected backup file before restoring'))
                        ->icon('heroicon-o-folder-open')
                        ->iconColor('primary')
                        ->headerActions([
                            Action::make('analyzeBackup')
                                ->label(__('Analyze Backup'))
                                ->icon('heroicon-o-magnifying-glass')
                                ->color('primary')
                                ->disabled(fn () => $this->isAnalyzing || ! $this->backupPath || ! empty($this->discoveredData))
                                ->action('analyzeBackup'),
                        ])
                        ->schema([
                            Text::make(__('File: :name', [
                                'name' => $this->backupFilename ?? '-',
                            ])),
                            Text::make(__('Size: :size', [
                                'size' => $this->backupSize ? Formatter::bytes($this->backupSize) : '-',
                            ])),
                        ]),

                    Section::make(__('Analysis Progress'))
                        ->icon($this->getAnalysisStatusIcon())
                        ->iconColor($this->getAnalysisStatusColor())
                        ->schema($this->getAnalysisStatusSchema())
                        ->extraAttributes($this->isAnalyzing ? ['wire:poll.1s' => 'pollAnalysisStatus'] : []),
                ])
                ->afterValidation(function () {
                    if (empty($this->discoveredData)) {
                        Notification::make()
                            ->title(__('Analysis required'))
                            ->body(__('Please analyze the backup file before proceeding'))
                            ->danger()
                            ->send();
                        throw new Exception(__('Please analyze the backup file'));
                    }
                });
        }

        return Step::make(__('Backup'))
            ->id('backup')
            ->icon('heroicon-o-cloud-arrow-down')
            ->description(__('Create and download backup'))
            ->schema([
                Section::make(__('Backup Transfer'))
                    ->description(__('Create a backup on cPanel and download it to this server'))
                    ->icon('heroicon-o-server')
                    ->schema([
                        Text::make(__('Local Path: :path', ['path' => $this->getBackupDestPath()])),
                        Text::make(__('Note: Large accounts may take several minutes.'))->color('warning'),
                    ]),

                FormActions::make([
                    Action::make('startBackup')
                        ->label(__('Start Backup'))
                        ->icon('heroicon-o-cloud-arrow-up')
                        ->color('success')
                        ->visible(fn () => ! $this->backupInitiated)
                        ->requiresConfirmation()
                        ->modalHeading(__('Start Backup'))
                        ->modalDescription(__('This will create a full backup on your cPanel account.'))
                        ->action('startBackup'),

                    Action::make('checkBackup')
                        ->label(__('Check Status'))
                        ->icon('heroicon-o-arrow-path')
                        ->color('info')
                        ->visible(fn () => $this->backupInitiated && ! $this->remoteBackupPath && ! $this->backupFilename)
                        ->action('checkBackupStatus'),

                    Action::make('downloadBackup')
                        ->label(__('Download Backup'))
                        ->icon('heroicon-o-cloud-arrow-down')
                        ->color('success')
                        ->visible(fn () => $this->remoteBackupPath && ! $this->backupFilename)
                        ->action('downloadBackup'),

                    Action::make('analyzeBackup')
                        ->label(__('Analyze Backup'))
                        ->icon('heroicon-o-magnifying-glass')
                        ->color('primary')
                        ->visible(fn () => $this->backupFilename && empty($this->discoveredData))
                        ->action('analyzeBackup'),
                ])->alignEnd(),

                Section::make(__('Transfer Status'))
                    ->icon($this->backupPath ? 'heroicon-o-check-circle' : 'heroicon-o-clock')
                    ->iconColor($this->backupPath ? 'success' : ($this->backupInitiated ? 'warning' : 'gray'))
                    ->schema($this->getStatusLogSchema())
                    ->extraAttributes($this->backupInitiated && ! $this->backupPath ? ['wire:poll.5s' => 'pollBackupStatus'] : []),

                Section::make(__('Analysis Progress'))
                    ->icon($this->getAnalysisStatusIcon())
                    ->iconColor($this->getAnalysisStatusColor())
                    ->visible(fn () => $this->backupFilename !== null || $this->isAnalyzing || ! empty($this->discoveredData))
                    ->schema($this->getAnalysisStatusSchema()),
            ])
            ->afterValidation(function () {
                if (! $this->backupPath || empty($this->discoveredData)) {
                    Notification::make()
                        ->title(__('Backup required'))
                        ->body(__('Please complete the backup and analysis before proceeding'))
                        ->danger()
                        ->send();
                    throw new Exception(__('Please complete the backup first'));
                }

                $this->storeCredentialsInSession();
            });
    }

    protected function getReviewStep(): Step
    {
        return Step::make(__('Review'))
            ->id('review')
            ->icon('heroicon-o-clipboard-document-check')
            ->description(__('Configure restore options'))
            ->schema($this->getReviewStepSchema());
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

    protected function getReviewStepSchema(): array
    {
        if (empty($this->discoveredData)) {
            return [
                Section::make(__('Waiting for Backup'))
                    ->icon('heroicon-o-clock')
                    ->iconColor('warning')
                    ->schema([
                        Text::make(__('Please complete the backup in the previous step.')),
                    ]),
            ];
        }

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
                            ->helperText(__('Restore all website files to your domains folder'))
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

    protected function addStatusLog(string $message, string $status = 'info'): void
    {
        $this->statusLog[] = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];
    }

    protected function getStatusLogSchema(): array
    {
        if (empty($this->statusLog)) {
            return [
                Text::make(__('Click "Start Backup" to begin.'))->color('gray'),
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
                    Text::make(__('Size: :size', ['size' => Formatter::bytes($this->backupSize)])),
                ])
                ->compact();
        }

        return $items;
    }

    protected function addAnalysisLog(string $message, string $status = 'info'): void
    {
        $this->analysisLog[] = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];
    }

    public function pollAnalysisStatus(): void
    {
        // UI polling refresh during backup analysis.
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

    protected function getAnalysisStatusSchema(): array
    {
        $items = [];

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

        if (empty($this->analysisLog) && ! $this->isAnalyzing && empty($this->discoveredData)) {
            return [
                Text::make(__('Click "Analyze Backup" to discover the backup contents.'))->color('gray'),
            ];
        }

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
        }

        return $items;
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

        $items = [];
        foreach ($databases as $db) {
            $items[] = Text::make('• '.$db['name'])
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

    public function testConnection(): void
    {
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
                $this->connectionInfo = [
                    'domains' => $this->countDomains($summary['domains'] ?? []),
                    'emails' => count($summary['email_accounts'] ?? []),
                    'databases' => count($summary['databases'] ?? []),
                    'ssl' => count($summary['ssl_certificates'] ?? []),
                ];

                Notification::make()
                    ->title(__('Connection successful'))
                    ->body(__('Found :domains domains, :emails email accounts, :dbs databases', [
                        'domains' => $this->connectionInfo['domains'],
                        'emails' => $this->connectionInfo['emails'],
                        'dbs' => $this->connectionInfo['databases'],
                    ]))
                    ->success()
                    ->send();

                $this->storeCredentialsInSession();
            } else {
                throw new Exception($result['message'] ?? __('Connection failed'));
            }
        } catch (Exception $e) {
            $this->isConnected = false;
            Log::error('cPanel connection failed', ['error' => $e->getMessage()]);

            Notification::make()
                ->title(__('Connection failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
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

    public function startBackup(): void
    {
        $user = $this->getUser();
        $destPath = $this->getBackupDestPath();

        try {
            $this->statusLog = [];
            $this->analysisLog = [];
            $this->discoveredData = [];
            $this->remoteBackupPath = null;
            $this->backupFilename = null;
            $this->backupPath = null;
            $this->backupSize = 0;
            $this->downloadProgress = 0;
            $this->addStatusLog(__('Starting backup on cPanel...'), 'pending');

            $this->getAgent()->send('file.mkdir', [
                'path' => $destPath,
                'username' => $user->username,
            ]);

            $cpanel = $this->getCpanel();
            $result = $cpanel->createBackup();

            if ($result['success']) {
                $this->backupInitiated = true;
                $this->backupInProgress = true;
                $this->backupPid = $result['pid'] ?? null;
                $this->pollCount = 0;
                $this->addStatusLog(__('Backup initiated on cPanel'), 'success');
                $this->addStatusLog(__('Waiting for backup to complete on cPanel...'), 'pending');

                Notification::make()
                    ->title(__('Backup started'))
                    ->body(__('cPanel is creating a full backup. This may take several minutes.'))
                    ->success()
                    ->send();
                $this->storeCredentialsInSession();
            } else {
                throw new Exception($result['message'] ?? __('Failed to start backup'));
            }
        } catch (Exception $e) {
            Log::error('cPanel backup initiation failed', ['error' => $e->getMessage()]);
            $this->addStatusLog(__('Backup failed: :message', ['message' => $e->getMessage()]), 'error');

            Notification::make()
                ->title(__('Backup failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function pollBackupStatus(): void
    {
        $this->checkBackupStatus(true);
    }

    public function checkBackupStatus(bool $quiet = false): void
    {
        $this->pollCount++;

        try {
            $cpanel = $this->getCpanel();
            $result = $cpanel->getBackupStatus();

            if ($result['success'] ?? false) {
                $backups = $result['backups'] ?? [];

                if (! empty($backups)) {
                    $latestBackup = $backups[0];
                    $this->remoteBackupPath = $latestBackup['path'];
                    $this->backupInProgress = false;
                    $this->addStatusLog(__('Backup ready on cPanel: :name', ['name' => $latestBackup['name']]), 'success');

                    if (! $quiet) {
                        Notification::make()
                            ->title(__('Backup ready'))
                            ->body(__('Backup file found: :name. Click "Download Backup" to continue.', [
                                'name' => $latestBackup['name'],
                            ]))
                            ->success()
                            ->send();
                    }

                    $this->storeCredentialsInSession();

                    return;
                }

                if ($result['in_progress'] ?? false) {
                    $this->backupInProgress = true;
                    $this->addStatusLog(__('Backup still in progress on cPanel...'), 'pending');

                    if (! $quiet) {
                        Notification::make()
                            ->title(__('Backup in progress'))
                            ->body(__('cPanel is still creating the backup. Please wait and check again.'))
                            ->info()
                            ->send();
                    }

                    $this->storeCredentialsInSession();

                    return;
                }
            }

            $this->addStatusLog(__('Backup not ready yet'), 'pending');

            if (! $quiet) {
                Notification::make()
                    ->title(__('Backup not ready'))
                    ->body(__('No backup files found yet. Please wait and check again.'))
                    ->info()
                    ->send();
            }
            $this->storeCredentialsInSession();
        } catch (Exception $e) {
            $this->addStatusLog(__('Error checking backup: :message', ['message' => $e->getMessage()]), 'error');
            if (! $quiet) {
                Notification::make()
                    ->title(__('Error checking backup'))
                    ->body(SafeError::message($e))
                    ->danger()
                    ->send();
            }
        }
    }

    public function downloadBackup(): void
    {
        if (! $this->remoteBackupPath) {
            Notification::make()
                ->title(__('No backup to download'))
                ->body(__('Please wait for the backup to be created first.'))
                ->warning()
                ->send();

            return;
        }

        $user = $this->getUser();
        $destPath = $this->getBackupDestPath();
        $filename = basename($this->remoteBackupPath);
        $localPath = $destPath.'/'.$filename;

        try {
            $this->addStatusLog(__('Downloading backup...'), 'pending');
            Notification::make()
                ->title(__('Download started'))
                ->body(__('Downloading backup from cPanel. This may take several minutes.'))
                ->info()
                ->send();

            $cpanel = $this->getCpanel();

            $result = $cpanel->downloadFileToPath(
                $this->remoteBackupPath,
                $localPath,
                function ($downloaded, $total) {
                    if ($total > 0) {
                        $this->downloadProgress = (int) (($downloaded / $total) * 100);
                    }
                }
            );

            if ($result['success'] ?? false) {
                $this->backupFilename = $filename;
                $this->backupPath = $localPath;
                $this->backupSize = (int) ($result['size'] ?? 0);
                $this->downloadProgress = 100;
                $this->backupInProgress = false;

                $this->getAgent()->send('file.chown', [
                    'path' => $localPath,
                    'username' => $user->username,
                ]);

                $this->addStatusLog(__('Backup downloaded: :name (:size)', [
                    'name' => $filename,
                    'size' => Formatter::bytes($this->backupSize),
                ]), 'success');

                Notification::make()
                    ->title(__('Download completed'))
                    ->body(__('Backup file :name (:size) is ready for analysis', [
                        'name' => $filename,
                        'size' => Formatter::bytes($this->backupSize),
                    ]))
                    ->success()
                    ->send();
                $this->storeCredentialsInSession();
            } else {
                throw new Exception($result['message'] ?? __('Failed to download backup'));
            }
        } catch (Exception $e) {
            Log::error('cPanel backup download failed', ['error' => $e->getMessage()]);
            $this->addStatusLog(__('Download failed: :message', ['message' => $e->getMessage()]), 'error');

            Notification::make()
                ->title(__('Download failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function analyzeBackup(): void
    {
        if (! $this->backupPath) {
            return;
        }

        try {
            $this->isAnalyzing = true;
            $this->analysisLog = [];
            $this->addAnalysisLog(__('Analyzing backup contents...'), 'pending');

            $result = $this->getAgent()->send('cpanel.analyze_backup', [
                'backup_path' => $this->backupPath,
            ]);

            if ($result['success'] ?? false) {
                $this->discoveredData = $result['data'] ?? [];
                $this->addAnalysisLog(__('Backup analyzed successfully'), 'success');
                $this->addAnalysisLog(__('Found :domains domains, :dbs databases, :mailboxes mailboxes', [
                    'domains' => count($this->discoveredData['domains'] ?? []),
                    'dbs' => count($this->discoveredData['databases'] ?? []),
                    'mailboxes' => count($this->discoveredData['mailboxes'] ?? []),
                ]), 'info');

                Notification::make()
                    ->title(__('Backup analyzed'))
                    ->body(__('Found :domains domains, :dbs databases, :mailboxes mailboxes', [
                        'domains' => count($this->discoveredData['domains'] ?? []),
                        'dbs' => count($this->discoveredData['databases'] ?? []),
                        'mailboxes' => count($this->discoveredData['mailboxes'] ?? []),
                    ]))
                    ->success()
                    ->send();
                $this->storeCredentialsInSession();
            } else {
                throw new Exception($result['error'] ?? __('Failed to analyze backup'));
            }
        } catch (Exception $e) {
            Log::error('Backup analysis failed', ['error' => $e->getMessage()]);
            $this->addAnalysisLog(__('Analysis failed: :message', ['message' => $e->getMessage()]), 'error');

            Notification::make()
                ->title(__('Analysis failed'))
                ->body(SafeError::message($e))
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

        $user = $this->getUser();

        $this->enqueueRestore($user);
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
        session()->put('user_cpanel_restore_job_id', $this->restoreJobId);
        session()->put('user_cpanel_restore_log_path', $this->restoreLogPath);
        session()->put('user_cpanel_restore_processing', true);
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
            session()->forget(['user_cpanel_restore_job_id', 'user_cpanel_restore_log_path', 'user_cpanel_restore_processing']);
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

    public function resetMigration(): void
    {
        $this->hostname = null;
        $this->cpanelUsername = null;
        $this->apiToken = null;
        $this->port = 2083;
        $this->useSSL = true;
        $this->sourceType = 'remote';
        $this->localBackupPath = null;
        $this->availableBackups = [];
        $this->isConnected = false;
        $this->backupInitiated = false;
        $this->backupPid = null;
        $this->backupInProgress = false;
        $this->remoteBackupPath = null;
        $this->backupFilename = null;
        $this->backupPath = null;
        $this->backupSize = 0;
        $this->downloadProgress = 0;
        $this->discoveredData = [];
        $this->restoreFiles = true;
        $this->restoreDatabases = true;
        $this->restoreEmails = true;
        $this->restoreSsl = true;
        $this->isProcessing = false;
        $this->migrationLog = [];
        $this->pollCount = 0;
        $this->cpanel = null;
        $this->connectionInfo = [];
        $this->statusLog = [];
        $this->analysisLog = [];
        $this->isAnalyzing = false;
        $this->step1Complete = false;
        $this->restoreJobId = null;
        $this->restoreLogPath = null;
        $this->restoreStatus = null;
        $this->wizardStep = null;

        $this->clearSessionCredentials();

        $this->redirect(static::getUrl());
    }
}
