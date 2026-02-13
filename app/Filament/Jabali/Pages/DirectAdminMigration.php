<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\ServerImport;
use App\Models\ServerImportAccount;
use App\Services\Agent\AgentClient;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Checkbox;
use Filament\Forms\Components\FileUpload;
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
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Components\View;
use Filament\Schemas\Components\Wizard;
use Filament\Schemas\Components\Wizard\Step;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Storage;
use Livewire\Attributes\Url;

class DirectAdminMigration extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-arrow-down-tray';

    protected static ?string $navigationLabel = null;

    protected static bool $shouldRegisterNavigation = false;

    protected static ?int $navigationSort = 16;

    protected static ?string $slug = 'directadmin-migration';

    protected string $view = 'filament.jabali.pages.directadmin-migration';

    #[Url(as: 'directadmin-step')]
    public ?string $wizardStep = null;

    public bool $step1Complete = false;

    public ?int $importId = null;

    public string $importMethod = 'backup_file'; // remote_server|backup_file

    public ?string $remoteHost = null;

    public int $remotePort = 2222;

    public ?string $remoteUser = null;

    public ?string $remotePassword = null;

    public ?string $localBackupPath = null;

    public array $availableBackups = [];

    public ?string $backupPath = null;

    public bool $importFiles = true;

    public bool $importDatabases = true;

    public bool $importEmails = true;

    public bool $importSsl = true;

    protected ?AgentClient $agent = null;

    public static function getNavigationLabel(): string
    {
        return __('DirectAdmin Migration');
    }

    public function getTitle(): string|Htmlable
    {
        return __('DirectAdmin Migration');
    }

    public function getSubheading(): ?string
    {
        return __('Migrate your DirectAdmin account into your Jabali account');
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
                ->modalDescription(__('This will reset the DirectAdmin migration wizard. Are you sure?'))
                ->action('resetMigration'),
        ];
    }

    public function mount(): void
    {
        $this->restoreFromSession();
        $this->restoreFromImport();

        if ($this->importMethod === 'backup_file') {
            $this->loadLocalBackups();
        }
    }

    public function updatedImportMethod(): void
    {
        $this->remoteHost = null;
        $this->remotePort = 2222;
        $this->remoteUser = null;
        $this->remotePassword = null;

        $this->localBackupPath = null;
        $this->backupPath = null;
        $this->availableBackups = [];

        if ($this->importMethod === 'backup_file') {
            $this->loadLocalBackups();
        }
    }

    public function updatedLocalBackupPath(): void
    {
        if (! $this->localBackupPath) {
            $this->backupPath = null;

            return;
        }

        $this->selectLocalBackup();
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
                $this->getConfigureStep(),
                $this->getMigrateStep(),
            ])
                ->persistStepInQueryString('directadmin-step'),
        ]);
    }

    protected function getConnectStep(): Step
    {
        return Step::make(__('Connect'))
            ->id('connect')
            ->icon('heroicon-o-link')
            ->description(__('Connect to DirectAdmin or upload a backup'))
            ->schema([
                Section::make(__('Source'))
                    ->description(__('For now, migration requires a DirectAdmin backup archive. Remote migration will be added next.'))
                    ->icon('heroicon-o-server')
                    ->schema([
                        Radio::make('importMethod')
                            ->label(__('Import Method'))
                            ->options([
                                'backup_file' => __('Backup File'),
                                'remote_server' => __('Remote Server (Discovery only)'),
                            ])
                            ->default('backup_file')
                            ->live(),

                        Grid::make(['default' => 1, 'sm' => 2])
                            ->schema([
                                TextInput::make('remoteHost')
                                    ->label(__('Host'))
                                    ->placeholder('directadmin.example.com')
                                    ->required()
                                    ->visible(fn (Get $get): bool => $get('importMethod') === 'remote_server'),
                                TextInput::make('remotePort')
                                    ->label(__('Port'))
                                    ->numeric()
                                    ->default(2222)
                                    ->required()
                                    ->visible(fn (Get $get): bool => $get('importMethod') === 'remote_server'),
                                TextInput::make('remoteUser')
                                    ->label(__('Username'))
                                    ->required()
                                    ->visible(fn (Get $get): bool => $get('importMethod') === 'remote_server'),
                                TextInput::make('remotePassword')
                                    ->label(__('Password'))
                                    ->password()
                                    ->revealable()
                                    ->required()
                                    ->visible(fn (Get $get): bool => $get('importMethod') === 'remote_server'),
                            ]),

                        Section::make(__('Backup File'))
                            ->description(__('Upload your DirectAdmin backup archive to your backups folder, then select it here.'))
                            ->icon('heroicon-o-folder')
                            ->visible(fn (Get $get): bool => $get('importMethod') === 'backup_file')
                            ->headerActions([
                                Action::make('uploadBackup')
                                    ->label(__('Upload'))
                                    ->icon('heroicon-o-arrow-up-tray')
                                    ->color('gray')
                                    ->modalHeading(__('Upload Backup'))
                                    ->modalDescription(fn (): string => ($user = $this->getUser())
                                        ? __('Upload a DirectAdmin backup archive into /home/:user/backups', ['user' => $user->username])
                                        : __('Upload a DirectAdmin backup archive into your backups folder'))
                                    ->modalSubmitActionLabel(__('Upload'))
                                    ->form([
                                        FileUpload::make('backup')
                                            ->label(__('DirectAdmin Backup Archive'))
                                            ->storeFiles(false)
                                            ->required()
                                            ->maxSize(512000) // 500MB in KB
                                            ->helperText(__('Supported formats: .tar.zst, .tar.gz, .tgz (max 500MB via upload)')),
                                    ])
                                    ->action(function (array $data): void {
                                        try {
                                            $user = $this->getUser();
                                            if (! $user) {
                                                throw new Exception(__('You must be logged in.'));
                                            }

                                            $file = $data['backup'] ?? null;
                                            if (! $file) {
                                                throw new Exception(__('Please select a backup file.'));
                                            }

                                            $filename = (string) $file->getClientOriginalName();
                                            $filename = basename($filename);

                                            if (! preg_match('/\\.(tar\\.zst|zst|tar\\.gz|tgz)$/i', $filename)) {
                                                throw new Exception(__('Backup must be a .zst, .tar.zst, .tar.gz or .tgz file.'));
                                            }

                                            $maxBytes = 500 * 1024 * 1024;
                                            $fileSize = (int) ($file->getSize() ?? 0);
                                            if ($fileSize > $maxBytes) {
                                                throw new Exception(__('File too large for upload (max 500MB). Upload it via SSH/SFTP to /home/:user/backups.', [
                                                    'user' => $user->username,
                                                ]));
                                            }

                                            // Ensure backups folder exists (mkdir will error if it already exists).
                                            try {
                                                $this->getAgent()->fileMkdir($user->username, 'backups');
                                            } catch (Exception $e) {
                                                if ($e->getMessage() !== 'Path already exists') {
                                                    throw $e;
                                                }
                                            }

                                            // Stage into the agent-allowed temp dir, then let the agent move it.
                                            $tmpDir = '/tmp/jabali-uploads';
                                            if (! is_dir($tmpDir)) {
                                                mkdir($tmpDir, 0700, true);
                                                chmod($tmpDir, 0700);
                                            } else {
                                                @chmod($tmpDir, 0700);
                                            }

                                            $safeName = preg_replace('/[^a-zA-Z0-9._-]/', '_', $filename);
                                            $tmpPath = $tmpDir.'/'.uniqid('upload_', true).'_'.$safeName;

                                            if (! @copy($file->getRealPath(), $tmpPath)) {
                                                throw new Exception(__('Failed to stage upload.'));
                                            }
                                            @chmod($tmpPath, 0600);

                                            $result = $this->getAgent()->send('file.upload_temp', [
                                                'username' => $user->username,
                                                'path' => 'backups',
                                                'filename' => $safeName,
                                                'temp_path' => $tmpPath,
                                            ]);

                                            if (! ($result['success'] ?? false)) {
                                                if (file_exists($tmpPath)) {
                                                    @unlink($tmpPath);
                                                }
                                                throw new Exception((string) ($result['error'] ?? __('Upload failed')));
                                            }

                                            $this->loadLocalBackups();

                                            $uploadedPath = $result['path'] ?? null;
                                            if (is_string($uploadedPath) && $uploadedPath !== '') {
                                                $this->localBackupPath = $uploadedPath;
                                                $this->selectLocalBackup();
                                            }

                                            Notification::make()
                                                ->title(__('Backup uploaded'))
                                                ->body(__('Uploaded :name', ['name' => $safeName]))
                                                ->success()
                                                ->send();
                                        } catch (Exception $e) {
                                            Notification::make()
                                                ->title(__('Upload failed'))
                                                ->body($e->getMessage())
                                                ->danger()
                                                ->send();
                                        }
                                    }),
                                Action::make('refreshLocalBackups')
                                    ->label(__('Refresh'))
                                    ->icon('heroicon-o-arrow-path')
                                    ->color('gray')
                                    ->action('refreshLocalBackups'),
                            ])
                            ->schema([
                                Select::make('localBackupPath')
                                    ->label(__('Backup File'))
                                    ->options(fn (): array => $this->getLocalBackupOptions())
                                    ->searchable()
                                    ->required(fn (Get $get): bool => $get('importMethod') === 'backup_file')
                                    ->live(),
                                Text::make(fn (): string => $this->backupPath
                                    ? __('Selected file: :file', ['file' => basename($this->backupPath)])
                                    : __('No backup selected yet.'))
                                    ->color('gray'),
                                Text::make(fn (): string => ($user = $this->getUser())
                                    ? __('Upload the file to: /home/:user/backups', ['user' => $user->username])
                                    : __('Upload the file to your /home/<user>/backups folder.'))
                                    ->color('gray'),
                                Text::make(__('Supported formats: .tar.zst, .tar.gz, .tgz'))->color('gray'),
                                Text::make(fn (): string => ($user = $this->getUser())
                                    ? __('No backups found in /home/:user/backups. Upload a file there and click Refresh.', ['user' => $user->username])
                                    : __('No backups found.'))
                                    ->color('gray')
                                    ->visible(fn (): bool => empty($this->availableBackups)),
                            ]),

                        FormActions::make([
                            Action::make('discoverAccount')
                                ->label(__('Discover Account'))
                                ->icon('heroicon-o-magnifying-glass')
                                ->color('primary')
                                ->action('discoverAccount'),
                        ])->alignEnd(),
                    ]),

                Section::make(__('Discovery'))
                    ->description(__('After discovery, you can choose what to import.'))
                    ->icon('heroicon-o-user')
                    ->schema([
                        Text::make(__('Discovered account details will be used for migration.'))->color('gray'),
                    ]),
            ])
            ->afterValidation(function () {
                $import = $this->getImport();
                $hasAccounts = $import?->accounts()->exists() ?? false;

                if (! $hasAccounts) {
                    Notification::make()
                        ->title(__('No account discovered'))
                        ->body(__('Click "Discover Account" to continue.'))
                        ->danger()
                        ->send();

                    throw new Exception(__('No account discovered'));
                }

                $this->step1Complete = true;
                $this->saveToSession();
            });
    }

    protected function getConfigureStep(): Step
    {
        return Step::make(__('Configure'))
            ->id('configure')
            ->icon('heroicon-o-cog')
            ->description(__('Choose what to import'))
            ->schema([
                Section::make(__('What to Import'))
                    ->description(__('Select which parts of your account to import.'))
                    ->icon('heroicon-o-check-circle')
                    ->schema([
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            Checkbox::make('importFiles')
                                ->label(__('Website Files'))
                                ->helperText(__('Restore website files from the backup'))
                                ->default(true),
                            Checkbox::make('importDatabases')
                                ->label(__('Databases'))
                                ->helperText(__('Restore MySQL databases and import dumps'))
                                ->default(true),
                            Checkbox::make('importEmails')
                                ->label(__('Email'))
                                ->helperText(__('Create email domains and mailboxes (limited in Phase 1)'))
                                ->default(true),
                            Checkbox::make('importSsl')
                                ->label(__('SSL'))
                                ->helperText(__('Install custom certificates or issue Let\'s Encrypt (Phase 3)'))
                                ->default(true),
                        ]),
                    ]),
            ])
            ->afterValidation(function (): void {
                $import = $this->getImport();
                if (! $import) {
                    throw new Exception(__('Import job not found'));
                }

                $import->update([
                    'import_options' => [
                        'files' => $this->importFiles,
                        'databases' => $this->importDatabases,
                        'emails' => $this->importEmails,
                        'ssl' => $this->importSsl,
                    ],
                ]);

                $this->saveToSession();
            });
    }

    protected function getMigrateStep(): Step
    {
        return Step::make(__('Migrate'))
            ->id('migrate')
            ->icon('heroicon-o-play')
            ->description(__('Run the migration and watch progress'))
            ->schema([
                FormActions::make([
                    Action::make('startMigration')
                        ->label(__('Start Migration'))
                        ->icon('heroicon-o-play')
                        ->color('success')
                        ->requiresConfirmation()
                        ->modalHeading(__('Start Migration'))
                        ->modalDescription(__('This will import data into your Jabali account. Continue?'))
                        ->action('startMigration'),

                    Action::make('newMigration')
                        ->label(__('New Migration'))
                        ->icon('heroicon-o-plus')
                        ->color('primary')
                        ->visible(fn (): bool => ($this->getImport()?->status ?? null) === 'completed')
                        ->action('resetMigration'),
                ])->alignEnd(),

                Section::make(__('Import Status'))
                    ->icon('heroicon-o-queue-list')
                    ->schema([
                        View::make('filament.jabali.pages.directadmin-migration-status-table'),
                    ]),
            ]);
    }

    public function discoverAccount(): void
    {
        try {
            $user = Auth::user();
            if (! $user) {
                throw new Exception(__('You must be logged in.'));
            }

            $import = $this->upsertImportForDiscovery();

            $backupFullPath = null;
            $remotePassword = null;

            if ($this->importMethod === 'backup_file') {
                if (! $import->backup_path) {
                    throw new Exception(__('Please select a DirectAdmin backup archive.'));
                }

                $backupFullPath = $this->resolveBackupFullPath($import->backup_path);
                if (! $backupFullPath) {
                    throw new Exception(__('Backup file not found: :path', ['path' => $import->backup_path]));
                }
            } else {
                $remotePassword = $this->remotePassword;

                if (($remotePassword === null || $remotePassword === '') && filled($import->remote_password)) {
                    $remotePassword = (string) $import->remote_password;
                }

                if (! $import->remote_host || ! $import->remote_port || ! $import->remote_user || ! $remotePassword) {
                    throw new Exception(__('Please enter DirectAdmin host, port, username and password.'));
                }
            }

            $result = $this->getAgent()->importDiscover(
                $import->id,
                'directadmin',
                $import->import_method,
                $backupFullPath,
                $import->remote_host,
                $import->remote_port ? (int) $import->remote_port : null,
                $import->remote_user,
                $remotePassword,
            );

            if (! ($result['success'] ?? false)) {
                throw new Exception((string) ($result['error'] ?? __('Discovery failed')));
            }

            $accounts = $result['accounts'] ?? [];
            if (! is_array($accounts) || $accounts === []) {
                throw new Exception(__('No account was discovered.'));
            }

            $account = null;
            if (count($accounts) === 1) {
                $account = $accounts[0];
            } else {
                // Prefer matching the provided username if multiple accounts are returned.
                foreach ($accounts as $candidate) {
                    if (! is_array($candidate)) {
                        continue;
                    }
                    if (($candidate['username'] ?? null) === $this->remoteUser) {
                        $account = $candidate;
                        break;
                    }
                }
            }

            if (! is_array($account)) {
                throw new Exception(__('Multiple accounts were discovered. Please upload a single-user backup archive.'));
            }

            $sourceUsername = trim((string) ($account['username'] ?? ''));
            if ($sourceUsername === '') {
                throw new Exception(__('Discovered account is missing a username.'));
            }

            $import->accounts()->delete();

            $record = ServerImportAccount::create([
                'server_import_id' => $import->id,
                'source_username' => $sourceUsername,
                'target_username' => $user->username,
                'email' => (string) ($account['email'] ?? ''),
                'main_domain' => (string) ($account['main_domain'] ?? ''),
                'addon_domains' => $account['addon_domains'] ?? [],
                'subdomains' => $account['subdomains'] ?? [],
                'databases' => $account['databases'] ?? [],
                'email_accounts' => $account['email_accounts'] ?? [],
                'disk_usage' => (int) ($account['disk_usage'] ?? 0),
                'status' => 'pending',
                'progress' => 0,
                'current_task' => null,
                'import_log' => [],
                'error' => null,
            ]);

            $import->update([
                'discovered_accounts' => [$account],
                'selected_accounts' => [$record->id],
                'status' => 'ready',
                'progress' => 0,
                'current_task' => null,
                'errors' => [],
            ]);

            $this->importId = $import->id;
            $this->step1Complete = true;
            $this->saveToSession();

            $this->dispatch('directadmin-self-status-updated');

            Notification::make()
                ->title(__('Account discovered'))
                ->body(__('Ready to migrate into your Jabali account (:username).', ['username' => $user->username]))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Discovery failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function startMigration(): void
    {
        $import = $this->getImport();
        if (! $import) {
            Notification::make()
                ->title(__('Import job not found'))
                ->danger()
                ->send();

            return;
        }

        $selected = $import->selected_accounts ?? [];
        if (! is_array($selected) || $selected === []) {
            Notification::make()
                ->title(__('No account selected'))
                ->danger()
                ->send();

            return;
        }

        if ($import->import_method === 'remote_server') {
            Notification::make()
                ->title(__('Remote DirectAdmin import is not available yet'))
                ->body(__('For now, please download a DirectAdmin backup archive and use the "Backup File" method.'))
                ->warning()
                ->send();

            return;
        }

        $import->update([
            'status' => 'importing',
            'started_at' => now(),
        ]);

        $result = $this->getAgent()->importStart($import->id);

        if (! ($result['success'] ?? false)) {
            Notification::make()
                ->title(__('Failed to start migration'))
                ->body((string) ($result['error'] ?? __('Unknown error')))
                ->danger()
                ->send();

            return;
        }

        Notification::make()
            ->title(__('Migration started'))
            ->body(__('Import process has started in the background.'))
            ->success()
            ->send();

        $this->dispatch('directadmin-self-status-updated');
    }

    public function resetMigration(): void
    {
        if ($this->importId) {
            ServerImport::whereKey($this->importId)->delete();
        }

        session()->forget('directadmin_self_migration.import_id');

        $this->wizardStep = null;
        $this->step1Complete = false;
        $this->importId = null;
        $this->importMethod = 'backup_file';
        $this->remoteHost = null;
        $this->remotePort = 2222;
        $this->remoteUser = null;
        $this->remotePassword = null;
        $this->localBackupPath = null;
        $this->availableBackups = [];
        $this->backupPath = null;
        $this->importFiles = true;
        $this->importDatabases = true;
        $this->importEmails = true;
        $this->importSsl = true;
    }

    protected function getAgent(): AgentClient
    {
        return $this->agent ??= new AgentClient;
    }

    protected function getUser()
    {
        return Auth::user();
    }

    protected function loadLocalBackups(): void
    {
        $this->availableBackups = [];

        $user = $this->getUser();
        if (! $user) {
            return;
        }

        $result = $this->getAgent()->send('file.list', [
            'username' => $user->username,
            'path' => 'backups',
        ]);

        if (! ($result['success'] ?? false)) {
            $this->getAgent()->send('file.mkdir', [
                'username' => $user->username,
                'path' => 'backups',
            ]);

            $result = $this->getAgent()->send('file.list', [
                'username' => $user->username,
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
            if (! preg_match('/\\.(tar\\.zst|zst|tar\\.gz|tgz)$/i', $name)) {
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

            $size = $this->formatBytes((int) ($item['size'] ?? 0));
            $options[$path] = "{$name} ({$size})";
        }

        return $options;
    }

    protected function selectLocalBackup(): void
    {
        $user = $this->getUser();
        if (! $user || ! $this->localBackupPath) {
            return;
        }

        $info = $this->getAgent()->send('file.info', [
            'username' => $user->username,
            'path' => $this->localBackupPath,
        ]);

        if (! ($info['success'] ?? false)) {
            Notification::make()
                ->title(__('Backup file not found'))
                ->body($info['error'] ?? __('Unable to read backup file'))
                ->danger()
                ->send();
            $this->backupPath = null;

            return;
        }

        $details = $info['info'] ?? [];
        if (! ($details['is_file'] ?? false)) {
            Notification::make()
                ->title(__('Invalid backup selection'))
                ->body(__('Please select a backup file'))
                ->warning()
                ->send();
            $this->backupPath = null;

            return;
        }

        $this->backupPath = "/home/{$user->username}/{$this->localBackupPath}";

        Notification::make()
            ->title(__('Backup selected'))
            ->body(__('Selected :name (:size)', [
                'name' => $details['name'] ?? basename($this->backupPath),
                'size' => $this->formatBytes((int) ($details['size'] ?? 0)),
            ]))
            ->success()
            ->send();
    }

    protected function formatBytes(int $bytes): string
    {
        if ($bytes >= 1073741824) {
            return number_format($bytes / 1073741824, 2).' GB';
        }
        if ($bytes >= 1048576) {
            return number_format($bytes / 1048576, 2).' MB';
        }
        if ($bytes >= 1024) {
            return number_format($bytes / 1024, 2).' KB';
        }

        return $bytes.' B';
    }

    protected function resolveBackupFullPath(?string $path): ?string
    {
        $path = trim((string) ($path ?? ''));
        if ($path === '') {
            return null;
        }

        if (str_starts_with($path, '/') && file_exists($path)) {
            return $path;
        }

        $localCandidate = Storage::disk('local')->path($path);
        if (file_exists($localCandidate)) {
            return $localCandidate;
        }

        $backupCandidate = Storage::disk('backups')->path($path);
        if (file_exists($backupCandidate)) {
            return $backupCandidate;
        }

        return file_exists($path) ? $path : null;
    }

    protected function getImport(): ?ServerImport
    {
        if (! $this->importId) {
            return null;
        }

        return ServerImport::with('accounts')->find($this->importId);
    }

    protected function upsertImportForDiscovery(): ServerImport
    {
        $user = Auth::user();
        $name = $user ? ('DirectAdmin Import - '.$user->username.' - '.now()->format('Y-m-d H:i')) : ('DirectAdmin Import '.now()->format('Y-m-d H:i'));

        $attributes = [
            'name' => $name,
            'source_type' => 'directadmin',
            'import_method' => $this->importMethod,
            'import_options' => [
                'files' => $this->importFiles,
                'databases' => $this->importDatabases,
                'emails' => $this->importEmails,
                'ssl' => $this->importSsl,
            ],
            'status' => 'discovering',
            'progress' => 0,
            'current_task' => null,
        ];

        if ($this->importMethod === 'backup_file') {
            $attributes['backup_path'] = $this->backupPath;
            $attributes['remote_host'] = null;
            $attributes['remote_port'] = null;
            $attributes['remote_user'] = null;
        } else {
            $attributes['backup_path'] = null;
            $attributes['remote_host'] = $this->remoteHost ? trim($this->remoteHost) : null;
            $attributes['remote_port'] = $this->remotePort;
            $attributes['remote_user'] = $this->remoteUser ? trim($this->remoteUser) : null;

            if (filled($this->remotePassword)) {
                $attributes['remote_password'] = $this->remotePassword;
            }
        }

        $import = $this->importId ? ServerImport::find($this->importId) : null;

        if ($import) {
            $import->update($attributes);
        } else {
            $import = ServerImport::create($attributes);
            $this->importId = $import->id;
        }

        $this->saveToSession();

        return $import->fresh();
    }

    protected function saveToSession(): void
    {
        if ($this->importId) {
            session()->put('directadmin_self_migration.import_id', $this->importId);
        }

        session()->save();
    }

    protected function restoreFromSession(): void
    {
        $this->importId = session('directadmin_self_migration.import_id');
    }

    protected function restoreFromImport(): void
    {
        $import = $this->getImport();
        if (! $import) {
            return;
        }

        $this->importMethod = (string) ($import->import_method ?? 'backup_file');
        $this->backupPath = $import->backup_path;
        if ($this->backupPath && ($user = $this->getUser())) {
            $prefix = "/home/{$user->username}/";
            if (str_starts_with($this->backupPath, $prefix)) {
                $this->localBackupPath = ltrim(substr($this->backupPath, strlen($prefix)), '/');
            }
        }
        $this->remoteHost = $import->remote_host;
        $this->remotePort = (int) ($import->remote_port ?? 2222);
        $this->remoteUser = $import->remote_user;

        $options = $import->import_options ?? [];
        if (is_array($options)) {
            $this->importFiles = (bool) ($options['files'] ?? true);
            $this->importDatabases = (bool) ($options['databases'] ?? true);
            $this->importEmails = (bool) ($options['emails'] ?? true);
            $this->importSsl = (bool) ($options['ssl'] ?? true);
        }

        $this->step1Complete = $import->accounts()->exists();
    }
}
