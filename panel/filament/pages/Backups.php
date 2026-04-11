<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Backup\Concerns\BrowsesSnapshots;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Support\Formatter;
use App\Support\SafeError;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\ActionGroup;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\CheckboxList;
use Filament\Forms\Components\FileUpload;
use Filament\Forms\Components\Placeholder;
use Filament\Schemas\Components\Section;
use Filament\Forms\Components\Select;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\Wizard\Step;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Schema;
use Filament\Schemas\Components\Grid;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;

class Backups extends Page implements HasActions, HasForms, HasTable
{
    use BrowsesSnapshots;
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-cloud-arrow-up';

    protected static ?int $navigationSort = 11;

    protected string $view = 'filament.admin.pages.backups';

    // ─── State ───

    public string $activeTab = 'destination';

    public array $snapshots = [];

    public array $backupAccounts = [];

    /** @deprecated Kept for Livewire state compatibility */
    public string $selectedDate = '';

    public string $filterUser = '';

    /** @var list<string> Files/folders selected for restore via browser widget */
    public array $restoreFileList = [];

    /** @var array<string, array> Snapshot inventory cache — class property, not static, to avoid Octane leaks */
    public array $inventoryCache = [];

    // Destinations
    public array $destinations = [];

    // Schedules
    public array $schedules = [];


    // Browser
    public string $browseSnapshotId = '';

    public string $browseUser = '';

    public string $browsePath = '';

    public array $browseItems = [];

    public array $selectedFiles = [];



    // (download state removed — downloads use direct URL via backup-download.php)

    // Logs
    public array $logJobs = [];

    public string $logFilter = 'all';

    // Settings
    public string $doctorOutput = '';

    public string $configOutput = '';

    // Stalwart
    public array $stalwartStatus = [];


    public static function getNavigationLabel(): string
    {
        return __('Backups');
    }

    public function getTitle(): string|Htmlable
    {
        return __('Backups');
    }

    public function mount(): void
    {
        $this->loadDestinations();
        $this->loadSchedules();
    }

    // ─── Header Actions ───

    protected function getHeaderActions(): array
    {
        return [
            Action::make('createBackup')
                ->label(__('Create Backup'))
                ->icon('heroicon-o-cloud-arrow-up')
                ->color('primary')
                ->form([
                    Select::make('username')
                        ->label(__('Account'))
                        ->placeholder(__('All Accounts'))
                        ->options(fn () => User::where('is_active', true)->orderBy('username')->pluck('username', 'username'))
                        ->searchable(),
                    Grid::make(3)->schema([
                        Toggle::make('include_files')->label(__('Files'))->default(true),
                        Toggle::make('include_databases')->label(__('MySQL'))->default(true),
                        Toggle::make('include_postgres')->label(__('PostgreSQL'))->default(true),
                    ]),
                    Grid::make(3)->schema([
                        Toggle::make('include_mailboxes')->label(__('Email'))->default(true),
                        Toggle::make('include_dns')->label(__('DNS'))->default(true),
                        Toggle::make('include_ssl')->label(__('SSL'))->default(true),
                    ]),
                ])
                ->action(function (array $data): void {
                    try {
                        $result = app(AgentClient::class)->send('jb.run', $data);
                        if ($result['success'] ?? false) {
                            $target = ! empty($data['username']) ? $data['username'] : __('all accounts');
                            Notification::make()->title(__('Backup started'))
                                ->body(__('Running in background for :user', ['user' => $target]))
                                ->success()->send();
                        } else {
                            Notification::make()->title(__('Backup failed'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
                        }
                    } catch (\Throwable $e) {
                        Notification::make()->title(__('Backup failed'))->body(SafeError::message($e))->danger()->send();
                    }
                }),
            Action::make('importBackup')
                ->label(__('Import Backup'))
                ->icon('heroicon-o-arrow-up-tray')
                ->color('warning')
                ->form([
                    FileUpload::make('archive')
                        ->label(__('Backup Archive'))
                        ->acceptedFileTypes(['application/gzip', 'application/x-gzip', 'application/x-tar', 'application/x-compressed-tar', 'application/octet-stream'])
                        ->maxSize(5 * 1024 * 1024) // 5 GB
                        ->disk('local')
                        ->directory('jabali-imports')
                        ->required(),
                    Toggle::make('force')
                        ->label(__('Overwrite existing data'))
                        ->default(false),
                ])
                ->action(function (array $data): void {
                    try {
                        $storedPath = $data['archive'] ?? '';
                        if ($storedPath === '') {
                            Notification::make()->title(__('Import failed'))->body(__('No file uploaded'))->danger()->send();
                            return;
                        }
                        $fullPath = storage_path('app/private/' . $storedPath);
                        if (! file_exists($fullPath)) {
                            $fullPath = storage_path('app/' . $storedPath);
                        }

                        // Preview first
                        $preview = app(AgentClient::class)->send('jb.upload_preview', ['file' => $fullPath]);
                        if (! ($preview['success'] ?? false)) {
                            Notification::make()->title(__('Import failed'))->body($preview['error'] ?? __('Invalid archive'))->danger()->send();
                            @unlink($fullPath);
                            return;
                        }

                        $users = array_column($preview['users'] ?? [], 'username');
                        $userList = implode(', ', $users) ?: __('none');

                        // Execute import
                        $result = app(AgentClient::class)->send('jb.upload_restore', [
                            'file' => $fullPath,
                            'force' => $data['force'] ?? false,
                        ]);
                        if ($result['success'] ?? false) {
                            Notification::make()->title(__('Import complete'))
                                ->body(__('Restored accounts: :users', ['users' => $userList]))
                                ->success()->send();
                            $this->loadBackupAccounts();
                        } else {
                            Notification::make()->title(__('Import failed'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
                        }
                    } catch (\Throwable $e) {
                        Notification::make()->title(__('Import failed'))->body(SafeError::message($e))->danger()->send();
                    }
                }),
            Action::make('refresh')
                ->label(__('Refresh'))
                ->icon('heroicon-o-arrow-path')
                ->color('gray')
                ->action(fn () => $this->loadSnapshots()),
        ];
    }

    // ─── Backup Accounts Table ───

    public function loadBackupAccounts(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.list_snapshots', []);
            $allSnapshots = $result['snapshots'] ?? [];
        } catch (\Throwable) {
            $allSnapshots = [];
        }

        // Group by account — show all snapshots
        $byAccount = [];
        foreach ($allSnapshots as $snap) {
            $username = $snap['username'] ?? '';
            if ($username === '') {
                continue;
            }
            if (! isset($byAccount[$username])) {
                $byAccount[$username] = [
                    'username' => $username,
                    'snapshot_count' => 0,
                    'latest_snapshot_id' => $snap['id'] ?? '',
                    'latest_date' => $snap['date'] ?? substr($snap['time'] ?? '', 0, 10),
                    'latest_time' => $snap['time'] ?? '',
                    'backup_size' => $snap['size'] ?? '—',
                ];
            }
            // Always keep the most recent snapshot
            $snapTime = $snap['time'] ?? '';
            if ($snapTime > ($byAccount[$username]['latest_time'] ?? '')) {
                $byAccount[$username]['latest_snapshot_id'] = $snap['id'] ?? '';
                $byAccount[$username]['latest_date'] = $snap['date'] ?? substr($snapTime, 0, 10);
                $byAccount[$username]['latest_time'] = $snapTime;
                $byAccount[$username]['backup_size'] = $snap['size'] ?? '—';
            }
            $byAccount[$username]['snapshot_count']++;
        }
        $this->backupAccounts = array_values($byAccount);
        $this->resetTable();
    }

    // ─── Snapshots (used by loadSnapshots for other contexts) ───

    public function loadSnapshots(): void
    {
        try {
            $params = [];
            if ($this->filterUser !== '') {
                $params['username'] = $this->filterUser;
            }
            $result = app(AgentClient::class)->send('jb.list_snapshots', $params);
            $this->snapshots = $result['snapshots'] ?? [];
        } catch (\Throwable $e) {
            $this->snapshots = [];
        }
        $this->resetTable();
    }

    public function updatedFilterUser(): void
    {
        $this->loadSnapshots();
    }

    public function getTableRecordKey(\Illuminate\Database\Eloquent\Model|array $record): string
    {
        return $record['id'] ?? $record['username'] ?? '';
    }

    public function table(Table $table): Table
    {
        if ($this->activeTab === 'destination') {
            return $this->destinationsTable($table);
        }

        if ($this->activeTab === 'schedule') {
            return $this->schedulesTable($table);
        }

        if ($this->activeTab === 'restore_download') {
            return $this->accountsTable($table);
        }

        if ($this->activeTab === 'logs') {
            return $this->logsTable($table);
        }

        return $this->accountsTable($table);
    }

    protected function destinationsTable(Table $table): Table
    {
        return $table
            ->records(fn () => collect($this->destinations)->mapWithKeys(fn ($d) => [$d['id'] => $d])->all())
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->weight('medium')
                    ->description(fn (array $record): string => ($record['is_default'] ?? false) ? __('Default') : '')
                    ->icon(fn (array $record): ?string => ($record['is_default'] ?? false) ? 'heroicon-o-star' : null)
                    ->iconColor('warning'),
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->color('gray')
                    ->formatStateUsing(fn (array $record): string => strtoupper($record['type'] ?? '')),
                TextColumn::make('path')
                    ->label(__('Path / URL'))
                    ->fontFamily('mono')
                    ->size('sm'),
                TextColumn::make('encryption_key')
                    ->label(__('Encryption'))
                    ->size('sm')
                    ->badge()
                    ->formatStateUsing(fn (array $record): string => ($record['credentials']['insecure_no_password'] ?? false)
                        ? __('None')
                        : (! empty($record['encryption_key']) ? __('Encrypted') : '—'))
                    ->color(fn (array $record): string => ($record['credentials']['insecure_no_password'] ?? false) ? 'gray' : 'success'),
                TextColumn::make('initialized')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (array $record): string => ($record['initialized'] ?? false) ? __('Ready') : __('Not Initialized'))
                    ->color(fn (array $record): string => ($record['initialized'] ?? false) ? 'success' : 'danger'),
            ])
            ->recordActions([
                Action::make('test')
                    ->label(fn (array $record): string => ($record['initialized'] ?? false) ? __('Test') : __('Test & Init'))
                    ->icon('heroicon-o-signal')
                    ->color(fn (array $record): string => ($record['initialized'] ?? false) ? 'gray' : 'primary')
                    ->size('sm')
                    ->action(fn (array $record) => $this->testDestination($record['id'])),
                Action::make('edit')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil-square')
                    ->color('gray')
                    ->size('sm')
                    ->fillForm(function (array $record): array {
                        $data = [
                            'name' => $record['name'] ?? '',
                            'type' => $record['type'] ?? '',
                        ];

                        $path = $record['path'] ?? '';
                        $type = $record['type'] ?? '';

                        if (in_array($type, ['local', 'rest'])) {
                            $data['path'] = $path;
                        } elseif ($type === 'sftp' && preg_match('#^sftp:([^@]+)@([^:]+):(.+)$#', $path, $m)) {
                            $data['user'] = $m[1];
                            $data['host'] = $m[2];
                            $data['sftp_path'] = $m[3];
                        }

                        // Pre-fill auth method from credentials
                        $creds = $record['credentials'] ?? [];
                        if (! empty($creds['ssh_password_file'])) {
                            $data['ssh_auth'] = 'password';
                        } elseif (! empty($creds['ssh_key_file'])) {
                            $data['ssh_auth'] = 'key';
                            $data['ssh_key'] = $creds['ssh_key_file'];
                        }

                        return $data;
                    })
                    ->schema([
                        TextInput::make('name')->label(__('Name'))->required(),
                        Select::make('type')->label(__('Backend Type'))->options([
                            'local' => __('Local Directory'),
                            'sftp' => __('SFTP / SSH'),
                            's3' => __('Amazon S3 / Wasabi / MinIO'),
                            'b2' => __('Backblaze B2'),
                            'gcs' => __('Google Cloud Storage'),
                            'azure' => __('Azure Blob Storage'),
                            'rest' => __('REST Server'),
                            'rclone' => __('Rclone Remote'),
                            'gdrive' => __('Google Drive (rclone)'),
                        ])->required()->live(),
                        TextInput::make('host')->label(__('Host'))->visible(fn ($get) => $get('type') === 'sftp'),
                        Grid::make(2)->schema([
                            TextInput::make('user')->label(__('Username'))->visible(fn ($get) => $get('type') === 'sftp'),
                            TextInput::make('port')->label(__('Port'))->numeric()->visible(fn ($get) => $get('type') === 'sftp'),
                        ]),
                        TextInput::make('sftp_path')->label(__('Remote Path'))->visible(fn ($get) => $get('type') === 'sftp'),
                        Select::make('ssh_auth')->label(__('Authentication'))->options(['key' => __('SSH Key'), 'password' => __('Password')])->default('key')->live()->visible(fn ($get) => $get('type') === 'sftp'),
                        Select::make('ssh_key')->label(__('SSH Key'))->options(fn () => $this->getSshKeyOptions())->placeholder(__('Keep current'))->visible(fn ($get) => $get('type') === 'sftp' && ($get('ssh_auth') ?? 'key') === 'key'),
                        TextInput::make('ssh_password')->label(__('SSH Password'))->password()->revealable()->visible(fn ($get) => $get('type') === 'sftp' && ($get('ssh_auth') ?? 'key') === 'password'),
                        TextInput::make('path')->label(__('Path / URL'))->visible(fn ($get) => in_array($get('type'), ['local', 'rest'])),
                        TextInput::make('bucket')->label(__('Bucket'))->visible(fn ($get) => in_array($get('type'), ['s3', 'b2', 'gcs', 'azure'])),
                        TextInput::make('access_key')->label(__('Access Key (blank = keep)'))->password()->revealable()->visible(fn ($get) => $get('type') === 's3'),
                        TextInput::make('secret_key')->label(__('Secret Key (blank = keep)'))->password()->revealable()->visible(fn ($get) => $get('type') === 's3'),
                        TextInput::make('remote')->label(__('Rclone Remote'))->visible(fn ($get) => in_array($get('type'), ['rclone', 'gdrive'])),
                    ])
                    ->action(function (array $data, array $record): void {
                        $params = ['id' => $record['id']];
                        // Map sftp_path to path for CLI
                        if (isset($data['sftp_path']) && $data['sftp_path'] !== '') {
                            $data['path'] = $data['sftp_path'];
                        }
                        unset($data['sftp_path'], $data['ssh_auth']);
                        foreach ($data as $key => $value) {
                            if ($value !== '' && $value !== null) {
                                $params[$key] = $value;
                            }
                        }
                        try {
                            $result = app(AgentClient::class)->send('jb.destinations_update', $params);
                            if ($result['success'] ?? false) {
                                Notification::make()->title(__('Destination updated'))->success()->send();
                                $this->loadDestinations();
                            } else {
                                Notification::make()->title(__('Update failed'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
                            }
                        } catch (\Throwable $e) {
                            Notification::make()->title(__('Update failed'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
                ActionGroup::make([
                    Action::make('setDefault')
                        ->label(__('Set as Default'))
                        ->icon('heroicon-o-star')
                        ->color('gray')
                        ->visible(fn (array $record): bool => ! ($record['is_default'] ?? false))
                        ->action(fn (array $record) => $this->setDefaultDestination($record['id'])),
                    Action::make('reinit')
                        ->label(__('Re-initialize'))
                        ->icon('heroicon-o-arrow-path')
                        ->color('danger')
                        ->visible(fn (array $record): bool => (bool) ($record['initialized'] ?? false))
                        ->requiresConfirmation()
                        ->modalIcon('heroicon-o-exclamation-triangle')
                        ->modalIconColor('danger')
                        ->modalHeading(__('Re-initialize Repository'))
                        ->modalDescription(__('This is a destructive operation. The existing repository and ALL its backups will be permanently destroyed. A new empty repository will be created with a new encryption key. Previous backups will become permanently inaccessible. This cannot be undone.'))
                        ->action(fn (array $record) => $this->reinitDestination($record['id'])),
                    Action::make('remove')
                        ->label(__('Remove'))
                        ->icon('heroicon-o-trash')
                        ->color('danger')
                        ->requiresConfirmation()
                        ->modalHeading(__('Remove Destination'))
                        ->modalDescription(__('Credential files will be deleted. This cannot be undone.'))
                        ->action(fn (array $record) => $this->removeDestination($record['id'])),
                ]),
            ])
            ->heading(__('Backup Destinations'))
            ->description(__(':count destination(s)', ['count' => count($this->destinations)]))
            ->toolbarActions([
                Action::make('addDestination')
                    ->label(__('Add Destination'))
                    ->icon('heroicon-o-plus')
                    ->color('primary')
                    ->schema($this->getAddDestinationFormSchema())
                    ->action(function (array $data): void {
                        $this->executeAddDestination($data);
                    }),
                Action::make('refreshDestinations')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(fn () => $this->loadDestinations()),
            ]);
    }

    protected function getRestoreComponentOptions(): array
    {
        return [
            'metadata' => __('Panel Config'),
            'files' => __('Home Dir Files'),
            'mysql' => __('MySQL'),
            'postgres' => __('PostgreSQL'),
            'email' => __('Email Accounts'),
            'dns' => __('DNS Zones'),
            'cron' => __('Cron Jobs'),
            'ssl' => __('SSL Certificates'),
            'nginx' => __('Nginx Config'),
            'php' => __('PHP Config'),
            'stalwart' => __('Stalwart Mail'),
        ];
    }

    protected function getRestoreComponentDefaults(): array
    {
        return array_keys($this->getRestoreComponentOptions());
    }

    protected function accountsTable(Table $table): Table
    {
        $accountsByKey = collect($this->backupAccounts)->mapWithKeys(fn ($a) => [$a['username'] => $a])->all();

        return $table
            ->records(fn () => $accountsByKey)
            ->selectable()
            ->resolveSelectedRecordsUsing(function (array $keys, bool $isTrackingDeselectedKeys = false, array $deselectedKeys = []) use ($accountsByKey): \Illuminate\Support\Collection {
                if ($isTrackingDeselectedKeys) {
                    return collect(\Illuminate\Support\Arr::except($accountsByKey, $deselectedKeys));
                }

                return collect(\Illuminate\Support\Arr::only($accountsByKey, $keys));
            })
            ->columns([
                TextColumn::make('username')
                    ->label(__('Account'))
                    ->weight('medium')
                    ->searchable(),
                TextColumn::make('snapshot_count')
                    ->label(__('Snapshots'))
                    ->alignCenter(),
                TextColumn::make('latest_date')
                    ->label(__('Latest Backup'))
                    ->formatStateUsing(fn (array $record): string => $record['latest_date'] ?: $record['latest_time'] ?: '—'),
                TextColumn::make('backup_size')
                    ->label(__('Size')),
            ])
            ->recordActions([
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('primary')
                    ->size('sm')
                    ->modalHeading(fn (array $record): string => __('Restore :user', ['user' => $record['username']]))
                    ->modalWidth('4xl')
                    ->steps(function (array $record): array {
                        // Load inventory from snapshot
                        $cacheKey = $record['username'] . ':' . ($record['latest_snapshot_id'] ?? 'latest');
                        if (isset($this->inventoryCache[$cacheKey])) {
                            $inv = $this->inventoryCache[$cacheKey];
                        } else {
                            $inv = [];
                            try {
                                $result = app(AgentClient::class)->send('jb.snapshot_inventory', [
                                    'username' => $record['username'],
                                    'snapshot_id' => $record['latest_snapshot_id'] ?? 'latest',
                                ]);
                                $inv = $result['inventory'] ?? [];
                            } catch (\Throwable) {
                            }
                            $this->inventoryCache[$cacheKey] = $inv;
                        }

                        $snapshotOptions = ['latest' => __('Latest (most recent)')];
                        try {
                            $snaps = app(AgentClient::class)->send('jb.list_snapshots', ['username' => $record['username']]);
                            foreach ($snaps['snapshots'] ?? [] as $snap) {
                                $label = trim(($snap['date'] ?? '') . ' ' . ($snap['time'] ?? ''));
                                $snapshotOptions[$snap['id']] = $label ?: $snap['id'];
                            }
                        } catch (\Throwable) {
                        }

                        $tabs = [];

                        if ($inv['metadata']['exists'] ?? false) {
                            $domains = $inv['metadata']['domains'] ?? [];
                            $tabs[] = Tab::make(__('Panel Config'))
                                ->icon('heroicon-o-cog-6-tooth')
                                ->schema([
                                    Toggle::make('restore_metadata')->label(__('Restore user account & domains'))->default(true),
                                    Placeholder::make('metadata_info')
                                        ->content(__('Domains: :domains', ['domains' => implode(', ', $domains) ?: __('none')])),
                                ]);
                        }

                        if ($inv['files']['exists'] ?? false) {
                            $tabs[] = Tab::make(__('Home Dir Files'))
                                ->icon('heroicon-o-folder')
                                ->schema([
                                    Toggle::make('restore_files')
                                        ->label(__('Restore all home directory files'))
                                        ->default(true)
                                        ->live()
                                        ->helperText(__('Turn off to select specific files below')),
                                    \Filament\Forms\Components\ViewField::make('restore_file_list')
                                        ->label('')
                                        ->visible(fn (\Filament\Schemas\Components\Utilities\Get $get): bool => ! $get('restore_files'))
                                        ->view('filament.admin.pages.partials.restore-file-badges'),
                                    \Filament\Forms\Components\ViewField::make('file_browser')
                                        ->label('')
                                        ->visible(fn (\Filament\Schemas\Components\Utilities\Get $get): bool => ! $get('restore_files'))
                                        ->view('filament.admin.pages.partials.snapshot-browser-embed')
                                        ->viewData([
                                            'snapshotId' => $record['latest_snapshot_id'] ?? 'latest',
                                            'username' => $record['username'],
                                        ]),
                                ]);
                        }

                        if ($inv['mysql']['exists'] ?? false) {
                            $dbs = collect($inv['mysql']['databases'] ?? [])->mapWithKeys(fn ($d) => [$d => $d])->all();
                            $mysqlUsers = collect($inv['mysql']['users'] ?? [])->mapWithKeys(fn ($u) => [$u => $u])->all();
                            $dbSchema = [];
                            if (! empty($mysqlUsers)) {
                                $dbSchema[] = CheckboxList::make('restore_mysql_users')->label(__('MySQL Users'))->options($mysqlUsers)->default(array_keys($mysqlUsers));
                            }
                            if (! empty($dbs)) {
                                $dbSchema[] = CheckboxList::make('restore_databases')->label(__('Databases'))->options($dbs)->default(array_keys($dbs));
                            }
                            $tabs[] = Tab::make(__('Databases'))->icon('heroicon-o-circle-stack')->badge(count($dbs) + count($mysqlUsers))->schema($dbSchema);
                        }

                        if ($inv['postgres']['exists'] ?? false) {
                            $pgDbs = collect($inv['postgres']['databases'] ?? [])->mapWithKeys(fn ($d) => [$d => $d])->all();
                            $tabs[] = Tab::make(__('PostgreSQL'))->icon('heroicon-o-circle-stack')->badge(count($pgDbs))->schema([
                                CheckboxList::make('restore_postgres')->label(__('PostgreSQL databases to restore'))->options($pgDbs)->default(array_keys($pgDbs)),
                            ]);
                        }

                        if ($inv['email']['exists'] ?? false) {
                            $emailDomains = collect($inv['email']['domains'] ?? [])->mapWithKeys(fn ($d) => [$d => $d])->all();
                            $tabs[] = Tab::make(__('Email'))->icon('heroicon-o-envelope')->badge(count($emailDomains))->schema([
                                CheckboxList::make('restore_email')->label(__('Email domains to restore'))->options($emailDomains)->default(array_keys($emailDomains)),
                            ]);
                        }

                        if ($inv['dns']['exists'] ?? false) {
                            $zones = collect($inv['dns']['zones'] ?? [])->mapWithKeys(fn ($d) => [$d => $d])->all();
                            $tabs[] = Tab::make(__('DNS'))->icon('heroicon-o-globe-alt')->badge(count($zones))->schema([
                                CheckboxList::make('restore_dns')->label(__('DNS zones to restore'))->options($zones)->default(array_keys($zones)),
                            ]);
                        }

                        if (($inv['ssl']['exists'] ?? false) && ! empty($inv['ssl']['domains'])) {
                            $sslDomains = collect($inv['ssl']['domains'])->mapWithKeys(fn ($d) => [$d => $d])->all();
                            $tabs[] = Tab::make(__('SSL Certificates'))->icon('heroicon-o-lock-closed')->schema([
                                CheckboxList::make('restore_ssl')->label(__('SSL certificates to restore'))->options($sslDomains)->default(array_keys($sslDomains)),
                            ]);
                        }

                        if ($inv['nginx']['exists'] ?? false) {
                            $configs = collect($inv['nginx']['configs'] ?? [])->mapWithKeys(fn ($d) => [$d => $d . '.conf'])->all();
                            $tabs[] = Tab::make(__('Nginx'))->icon('heroicon-o-server')->schema([
                                CheckboxList::make('restore_nginx')->label(__('Nginx configs to restore'))->options($configs)->default(array_keys($configs)),
                            ]);
                        }

                        if ($inv['php']['exists'] ?? false) {
                            $pools = collect($inv['php']['pools'] ?? [])->mapWithKeys(fn ($d) => [$d => $d . '.conf'])->all();
                            $tabs[] = Tab::make(__('PHP'))->icon('heroicon-o-code-bracket')->schema([
                                CheckboxList::make('restore_php')->label(__('PHP pool configs to restore'))->options($pools)->default(array_keys($pools)),
                            ]);
                        }

                        if (($inv['cron']['exists'] ?? false) && ! empty($inv['cron']['jobs'])) {
                            $tabs[] = Tab::make(__('Cron Jobs'))->icon('heroicon-o-clock')->schema([
                                Toggle::make('restore_cron')->label(__('Restore cron jobs'))->default(true),
                            ]);
                        }

                        if (($inv['stalwart']['exists'] ?? false) && ! empty($inv['stalwart']['accounts'])) {
                            $stalwartAccounts = collect($inv['stalwart']['accounts'])->mapWithKeys(fn ($a) => [$a => $a])->all();
                            $tabs[] = Tab::make(__('Stalwart Mail'))
                                ->icon('heroicon-o-inbox-stack')
                                ->badge(count($stalwartAccounts))
                                ->schema([
                                    CheckboxList::make('restore_stalwart')
                                        ->label(__('JMAP accounts to restore (emails, mailboxes, Sieve scripts, identities)'))
                                        ->options($stalwartAccounts)
                                        ->default(array_keys($stalwartAccounts)),
                                ]);
                        }

                        // If no components discovered, show a message instead of empty tabs
                        if (empty($tabs)) {
                            $tabs[] = Tab::make(__('No Data'))
                                ->icon('heroicon-o-exclamation-triangle')
                                ->schema([
                                    Placeholder::make('no_data')
                                        ->content(__('No restorable components found in this snapshot. The snapshot may be empty or the inventory check failed.')),
                                ]);
                        }

                        return [
                            // Step 1: Select what to restore
                            Step::make(__('Select Components'))
                                ->icon('heroicon-o-adjustments-horizontal')
                                ->schema([
                                    Select::make('snapshot')
                                        ->label(__('Snapshot'))
                                        ->options($snapshotOptions)
                                        ->default('latest')
                                        ->required(),
                                    Tabs::make('restore_tabs')->tabs($tabs)->contained(false),
                                ]),

                            // Step 2: Summary & confirm
                            Step::make(__('Review & Confirm'))
                                ->icon('heroicon-o-check-circle')
                                ->schema([
                                    Section::make(__('Restore Summary'))
                                        ->icon('heroicon-o-clipboard-document-check')
                                        ->schema([
                                            CheckboxList::make('restore_summary')
                                                ->label('')
                                                ->options(function (\Filament\Schemas\Components\Utilities\Get $get): array {
                                                    $options = [];

                                                    if ($get('restore_metadata')) {
                                                        $options['metadata'] = __('Panel Config (user account & domains)');
                                                    }

                                                    $restoreFiles = $get('restore_files');
                                                    if ($restoreFiles || $restoreFiles === null) {
                                                        $options['files'] = __('All home directory files');
                                                    } elseif (! empty($this->restoreFileList)) {
                                                        $list = implode(', ', array_slice($this->restoreFileList, 0, 5))
                                                            . (count($this->restoreFileList) > 5 ? '...' : '');
                                                        $options['files_selected'] = __(':count selected file(s): :list', [
                                                            'count' => count($this->restoreFileList),
                                                            'list' => $list,
                                                        ]);
                                                    }

                                                    $dbs = $get('restore_databases') ?? [];
                                                    if (! empty($dbs)) {
                                                        $options['databases'] = __('Databases: :list', ['list' => implode(', ', $dbs)]);
                                                    }

                                                    $mu = $get('restore_mysql_users') ?? [];
                                                    if (! empty($mu)) {
                                                        $options['mysql_users'] = __('MySQL users: :list', ['list' => implode(', ', $mu)]);
                                                    }

                                                    $pg = $get('restore_postgres') ?? [];
                                                    if (! empty($pg)) {
                                                        $options['postgres'] = __('PostgreSQL: :list', ['list' => implode(', ', $pg)]);
                                                    }

                                                    $em = $get('restore_email') ?? [];
                                                    if (! empty($em)) {
                                                        $options['email'] = __('Email: :list', ['list' => implode(', ', $em)]);
                                                    }

                                                    $dns = $get('restore_dns') ?? [];
                                                    if (! empty($dns)) {
                                                        $options['dns'] = __('DNS: :list', ['list' => implode(', ', $dns)]);
                                                    }

                                                    $ssl = $get('restore_ssl') ?? [];
                                                    if (! empty($ssl)) {
                                                        $options['ssl'] = __('SSL: :list', ['list' => implode(', ', $ssl)]);
                                                    }

                                                    $ng = $get('restore_nginx') ?? [];
                                                    if (! empty($ng)) {
                                                        $options['nginx'] = __('Nginx configs');
                                                    }

                                                    $php = $get('restore_php') ?? [];
                                                    if (! empty($php)) {
                                                        $options['php'] = __('PHP pool configs');
                                                    }

                                                    if ($get('restore_cron')) {
                                                        $options['cron'] = __('Cron jobs');
                                                    }

                                                    $stalwart = $get('restore_stalwart') ?? [];
                                                    if (! empty($stalwart)) {
                                                        $options['stalwart'] = __('Stalwart: :list', ['list' => implode(', ', $stalwart)]);
                                                    }

                                                    return $options;
                                                })
                                                ->default(function (\Filament\Schemas\Components\Utilities\Get $get): array {
                                                    // All items are pre-selected (this is a read-only summary)
                                                    $keys = [];
                                                    if ($get('restore_metadata')) $keys[] = 'metadata';
                                                    if ($get('restore_files') || $get('restore_files') === null) $keys[] = 'files';
                                                    elseif (! empty($this->restoreFileList)) $keys[] = 'files_selected';
                                                    if (! empty($get('restore_databases'))) $keys[] = 'databases';
                                                    if (! empty($get('restore_mysql_users'))) $keys[] = 'mysql_users';
                                                    if (! empty($get('restore_postgres'))) $keys[] = 'postgres';
                                                    if (! empty($get('restore_email'))) $keys[] = 'email';
                                                    if (! empty($get('restore_dns'))) $keys[] = 'dns';
                                                    if (! empty($get('restore_ssl'))) $keys[] = 'ssl';
                                                    if (! empty($get('restore_nginx'))) $keys[] = 'nginx';
                                                    if (! empty($get('restore_php'))) $keys[] = 'php';
                                                    if ($get('restore_cron')) $keys[] = 'cron';
                                                    if (! empty($get('restore_stalwart'))) $keys[] = 'stalwart';
                                                    return $keys;
                                                })
                                                ->disabled(),
                                        ])
                                        ->compact(),
                                    Toggle::make('force')
                                        ->label(__('Overwrite existing data'))
                                        ->helperText(__('Warning: existing data will be replaced')),
                                ]),
                        ];
                    })
                    ->action(function (array $data, array $record): void {
                        // Build components list from selected items
                        $components = [];
                        if ($data['restore_metadata'] ?? true) {
                            $components[] = 'metadata';
                        }
                        $fileList = [];
                        if ($data['restore_files'] ?? true) {
                            $components[] = 'files';
                        } elseif (! empty($this->restoreFileList)) {
                            $components[] = 'files';
                            $fileList = $this->restoreFileList;
                        }
                        if (! empty($data['restore_databases']) || ! empty($data['restore_mysql_users'])) {
                            $components[] = 'mysql';
                        }
                        if (! empty($data['restore_postgres'])) {
                            $components[] = 'postgres';
                        }
                        if (! empty($data['restore_email'])) {
                            $components[] = 'email';
                        }
                        if (! empty($data['restore_dns'])) {
                            $components[] = 'dns';
                        }
                        if (! empty($data['restore_ssl'])) {
                            $components[] = 'ssl';
                        }
                        if (! empty($data['restore_nginx'])) {
                            $components[] = 'nginx';
                        }
                        if (! empty($data['restore_php'])) {
                            $components[] = 'php';
                        }
                        if (! empty($data['restore_cron'])) {
                            $components[] = 'cron';
                        }
                        if (! empty($data['restore_stalwart'])) {
                            $components[] = 'stalwart';
                        }

                        $this->executeWizardRestore(
                            [$record['username']],
                            $components,
                            $data['force'] ?? false,
                            $data['snapshot'] ?? 'latest',
                            $fileList,
                        );
                        $this->restoreFileList = [];
                    }),
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->size('sm')
                    ->modalHeading(fn (array $record): string => __('Download :user', ['user' => $record['username']]))
                    ->form([
                        Select::make('snapshot')
                            ->label(__('Snapshot'))
                            ->options(function (array $record): array {
                                $options = ['latest' => __('Latest (most recent)')];
                                try {
                                    $result = app(AgentClient::class)->send('jb.list_snapshots', ['username' => $record['username']]);
                                    foreach ($result['snapshots'] ?? [] as $snap) {
                                        $label = trim(($snap['date'] ?? '') . ' ' . ($snap['time'] ?? ''));
                                        $options[$snap['id']] = $label ?: $snap['id'];
                                    }
                                } catch (\Throwable) {
                                }

                                return $options;
                            })
                            ->default('latest')
                            ->required(),
                        CheckboxList::make('components')
                            ->label(__('What to Download'))
                            ->options($this->getRestoreComponentOptions())
                            ->columns(3)
                            ->default($this->getRestoreComponentDefaults()),
                    ])
                    ->action(function (array $data, array $record): void {
                        $this->executeDownload(
                            [$record['username']],
                            $data['components'] ?? [],
                            $data['snapshot'] ?? 'latest',
                        );
                    }),
                Action::make('browse')
                    ->label(__('Browse'))
                    ->icon('heroicon-o-folder-open')
                    ->color('gray')
                    ->size('sm')
                    ->url(fn (array $record): string => '/jabali-admin/backups-browse?snapshot=' . ($record['latest_snapshot_id'] ?: 'latest') . '&user=' . $record['username']),
            ])
            ->heading(__('Backup Accounts'))
            ->description(trans_choice(':count account|:count accounts', count($this->backupAccounts), ['count' => count($this->backupAccounts)]))
            ->toolbarActions([
                Action::make('restoreSelected')
                    ->label(__('Restore Selected'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('primary')
                    ->accessSelectedRecords()
                    ->modalHeading(__('Restore Selected Accounts'))
                    ->modalDescription(__('Restore all selected accounts from their latest backup snapshot.'))
                    ->form([
                        CheckboxList::make('components')
                            ->label(__('What to Restore'))
                            ->options($this->getRestoreComponentOptions())
                            ->columns(3)
                            ->default($this->getRestoreComponentDefaults()),
                        Toggle::make('force')
                            ->label(__('Overwrite existing data'))
                            ->helperText(__('Warning: existing data will be replaced')),
                    ])
                    ->action(function (array $data, \Illuminate\Support\Collection $selectedRecords): void {
                        $accounts = $selectedRecords->pluck('username')->all();
                        if (empty($accounts)) {
                            Notification::make()->title(__('No accounts selected'))->warning()->send();

                            return;
                        }
                        $this->executeWizardRestore($accounts, $data['components'] ?? [], $data['force'] ?? false);
                    }),
                Action::make('downloadSelected')
                    ->label(__('Download Selected'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->accessSelectedRecords()
                    ->form([
                        CheckboxList::make('components')
                            ->label(__('What to Download'))
                            ->options($this->getRestoreComponentOptions())
                            ->columns(3)
                            ->default($this->getRestoreComponentDefaults()),
                    ])
                    ->action(function (array $data, \Illuminate\Support\Collection $selectedRecords): void {
                        $accounts = $selectedRecords->pluck('username')->all();
                        if (empty($accounts)) {
                            Notification::make()->title(__('No accounts selected'))->warning()->send();

                            return;
                        }
                        $this->executeDownload($accounts, $data['components'] ?? []);
                    }),
                Action::make('refreshAccounts')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(fn () => $this->loadBackupAccounts()),
            ]);
    }

    // ─── Snapshot Actions ───


    // ─── Browser (shared logic in BrowsesSnapshots trait) ───

    public function browseSnapshot(string $snapshotId, string $username): void
    {
        if (! preg_match('/^[a-z][a-z0-9_-]{0,31}$/', $username)) {
            Notification::make()->title(__('Invalid username'))->danger()->send();

            return;
        }
        if (! preg_match('/^[a-f0-9]{6,64}$/', $snapshotId) && $snapshotId !== 'latest') {
            Notification::make()->title(__('Invalid snapshot ID'))->danger()->send();

            return;
        }
        $this->browseSnapshotId = $snapshotId;
        $this->browseUser = $username;
        $this->browsePath = '';
        $this->selectedFiles = [];
        $this->activeTab = 'restore_download';
        $this->loadBrowseItems();
    }

    protected function browseUsername(): string
    {
        return $this->browseUser;
    }

    // ─── Restore / Download Wizard ───

    public function updatedActiveTab(): void
    {
        $this->resetTable();

        match ($this->activeTab) {
            'destination' => $this->loadDestinations(),
            'schedule' => $this->onScheduleTab(),
            'restore_download' => $this->loadBackupAccounts(),
            'logs' => $this->loadLogs(),
            'settings' => $this->loadSettings(),
            default => null,
        };
    }

    #[\Livewire\Attributes\On('files-selected-for-restore')]
    public function onFilesSelected(array $paths): void
    {
        $merged = array_merge($this->restoreFileList, $paths);
        $this->restoreFileList = array_values(array_unique(array_filter($merged, fn ($p) => $p !== '' && $p !== null)));
        $count = count($this->restoreFileList);
        Notification::make()
            ->title(__(':count item(s) added to restore list', ['count' => count($paths)]))
            ->body(__('Total: :total item(s) selected', ['total' => $count]))
            ->success()
            ->send();
    }

    public function removeFromRestoreList(string $path): void
    {
        $this->restoreFileList = array_values(array_filter($this->restoreFileList, fn ($p) => $p !== $path));
    }

    protected function onScheduleTab(): void
    {
        if (empty($this->destinations)) {
            $this->loadDestinations();
        }
        $this->loadSchedules();
    }

    protected function loadSettings(): void
    {
        try {
            $this->doctorOutput = (app(AgentClient::class)->send('jb.doctor', []))['output'] ?? '';
        } catch (\Throwable) {
            $this->doctorOutput = '';
        }
        try {
            $this->configOutput = (app(AgentClient::class)->send('jb.config_test', []))['output'] ?? '';
        } catch (\Throwable) {
            $this->configOutput = '';
        }
        $this->loadStalwartStatus();
    }

    protected function loadStalwartStatus(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.stalwart_status', []);
            $this->stalwartStatus = ($result['success'] ?? false) ? $result : [];
        } catch (\Throwable) {
            $this->stalwartStatus = [];
        }
    }

    public function testStalwartConnection(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.stalwart_test', []);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Stalwart connected'))->body($result['message'] ?? '')->success()->send();
            } else {
                Notification::make()->title(__('Connection failed'))->body(SafeError::fromAgent($result['error'] ?? __('Unknown error')))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function toggleStalwart(): void
    {
        $currentlyEnabled = $this->stalwartStatus['enabled'] ?? false;
        $enable = ! $currentlyEnabled;

        try {
            $result = app(AgentClient::class)->send('jb.stalwart_toggle', ['enable' => $enable]);
            if ($result['success'] ?? false) {
                Notification::make()
                    ->title($enable ? __('Stalwart backup enabled') : __('Stalwart backup disabled'))
                    ->success()
                    ->send();
                $this->loadStalwartStatus();
            } else {
                Notification::make()->title(__('Failed'))->body(SafeError::fromAgent($result['error'] ?? __('Unknown error')))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    protected function executeWizardRestore(array $accounts, array $components, bool $force, string $snapshot = 'latest', array $fileList = []): void
    {
        if (empty($accounts)) {
            Notification::make()->title(__('No accounts selected'))->warning()->send();

            return;
        }

        $succeeded = 0;
        $failed = 0;

        foreach ($accounts as $username) {
            try {
                $params = [
                    'username' => $username,
                    'snapshot_id' => $snapshot,
                    'components' => $components,
                    'force' => $force,
                ];
                if (! empty($fileList)) {
                    $params['files'] = $fileList;
                }
                $result = app(AgentClient::class)->send('jb.restore', $params);
                if ($result['success'] ?? false) {
                    $succeeded++;
                    // Show restore summary with skipped items warning
                    $output = $result['output'] ?? '';
                    if (str_contains($output, 'skipping')) {
                        Notification::make()
                            ->title(__('Restore completed for :user (some items skipped)', ['user' => $username]))
                            ->body(__('Use "Overwrite existing data" to restore all items'))
                            ->warning()
                            ->send();
                    }
                } else {
                    $failed++;
                    $errorMsg = $result['error'] ?? '';
                    // Extract last [ERROR] line for a clean message
                    if (preg_match_all('/\[ERROR\]\s*(.+)$/m', $errorMsg, $em)) {
                        $errorMsg = end($em[1]);
                    }
                    Notification::make()->title(__('Restore failed for :user', ['user' => $username]))->body($errorMsg)->danger()->send();
                }
            } catch (\Throwable $e) {
                $failed++;
                Notification::make()->title(__('Restore failed for :user', ['user' => $username]))->body(SafeError::message($e))->danger()->send();
            }
        }

        if ($succeeded > 0) {
            Notification::make()->title(__('Restore completed'))->body(__(':count account(s) restored', ['count' => $succeeded]))->success()->send();
        }
        if ($failed > 0 && $succeeded === 0 && count($accounts) > 1) {
            Notification::make()->title(__('Restore failed'))->body(__('All :count account(s) failed', ['count' => $failed]))->danger()->send();
        }
    }

    protected function executeDownload(array $accounts, array $components, string $snapshot = 'latest'): void
    {
        if (empty($accounts)) {
            Notification::make()->title(__('No account selected'))->warning()->send();

            return;
        }

        $url = url('/backup-download.php?' . http_build_query([
            'users' => implode(',', $accounts),
            'snapshot' => $snapshot,
        ]));
        $this->js("window.open('{$url}', '_blank')");
    }

    // ─── Schedule ───

    public function loadSchedules(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.schedules_list', []);
            $this->schedules = $result['schedules'] ?? [];
        } catch (\Throwable $e) {
            $this->schedules = [];
        }
        $this->resetTable();
    }


    protected function getScheduleFormSchema(): array
    {
        return [
            TextInput::make('name')->label(__('Job Name'))->placeholder('Daily backup')->required()->maxLength(60),
            Select::make('destination')->label(__('Destination'))->options(fn () => collect($this->destinations)->pluck('name', 'id')->all())->required(),
            Select::make('frequency')->label(__('Frequency'))->options([
                'daily' => __('Daily'),
                'weekly' => __('Weekly'),
                'monthly' => __('Monthly'),
            ])->default('daily')->required()->live(),
            Toggle::make('every_day')->label(__('Every Day'))->default(true)->live()->visible(fn ($get) => $get('frequency') === 'daily'),
            CheckboxList::make('days')->label(__('Days'))->options([
                '0' => __('Sunday'),
                '1' => __('Monday'),
                '2' => __('Tuesday'),
                '3' => __('Wednesday'),
                '4' => __('Thursday'),
                '5' => __('Friday'),
                '6' => __('Saturday'),
            ])->columns(4)->visible(fn ($get) => $get('frequency') === 'daily' && ! $get('every_day')),
            Select::make('week_day')->label(__('Day of the Week'))->options([
                '0' => __('Sunday'),
                '1' => __('Monday'),
                '2' => __('Tuesday'),
                '3' => __('Wednesday'),
                '4' => __('Thursday'),
                '5' => __('Friday'),
                '6' => __('Saturday'),
            ])->default('0')->visible(fn ($get) => $get('frequency') === 'weekly'),
            Select::make('month_day')->label(__('Day of the Month'))->options(
                collect(range(1, 28))->mapWithKeys(fn ($d) => [$d => (string) $d])->all()
            )->default(1)->visible(fn ($get) => $get('frequency') === 'monthly'),
            Select::make('hour')->label(__('Start Time'))->options(
                collect(range(0, 23))->mapWithKeys(fn ($h) => [
                    $h => sprintf('%02d:00', $h),
                ])->all()
            )->default(2)->required(),
            Select::make('account_mode')->label(__('Accounts'))->options([
                'all' => __('All accounts'),
                'specific' => __('Specific accounts'),
            ])->default('all')->live(),
            Select::make('accounts')->label(__('Select Accounts'))->multiple()->options(fn () => User::where('is_active', true)->orderBy('username')->pluck('username', 'username')->all())->visible(fn ($get) => $get('account_mode') === 'specific'),
            Select::make('exclude')->label(__('Exclude Accounts'))->multiple()->options(fn () => User::where('is_active', true)->orderBy('username')->pluck('username', 'username')->all())->helperText(__('Accounts to skip during this job')),
            TextInput::make('retention')->label(__('Retention'))->numeric()->default(7)->required()->helperText(__('Number of backups to keep for this job')),
        ];
    }

    protected function schedulesTable(Table $table): Table
    {
        return $table
            ->records(fn () => collect($this->schedules)->mapWithKeys(fn ($s) => [$s['id'] => $s])->all())
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->weight('medium'),
                TextColumn::make('destination')
                    ->label(__('Destination'))
                    ->badge()
                    ->color('gray'),
                TextColumn::make('cron')
                    ->label(__('Schedule'))
                    ->fontFamily('mono')
                    ->size('sm'),
                TextColumn::make('accounts')
                    ->label(__('Accounts'))
                    ->formatStateUsing(fn (array $record): string => ($record['accounts'] ?? 'all') === 'all' ? __('All') : $record['accounts']),
                TextColumn::make('enabled')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (array $record): string => ($record['enabled'] ?? false) ? __('Enabled') : __('Disabled'))
                    ->color(fn (array $record): string => ($record['enabled'] ?? false) ? 'success' : 'gray'),
            ])
            ->recordActions([
                Action::make('runNow')
                    ->label(__('Run'))
                    ->icon('heroicon-o-play')
                    ->color('primary')
                    ->size('sm')
                    ->requiresConfirmation()
                    ->modalHeading(__('Run Backup Now'))
                    ->modalDescription(__('This will start the backup job immediately in the background.'))
                    ->action(fn (array $record) => $this->runScheduleNow($record)),
                Action::make('toggle')
                    ->label(fn (array $record): string => ($record['enabled'] ?? false) ? __('Disable') : __('Enable'))
                    ->icon(fn (array $record): string => ($record['enabled'] ?? false) ? 'heroicon-o-pause' : 'heroicon-o-play')
                    ->color(fn (array $record): string => ($record['enabled'] ?? false) ? 'gray' : 'success')
                    ->size('sm')
                    ->action(fn (array $record) => $this->toggleSchedule($record['id'], ! ($record['enabled'] ?? false))),
                Action::make('editSchedule')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil-square')
                    ->color('gray')
                    ->size('sm')
                    ->fillForm(function (array $record): array {
                        $cron = $record['cron'] ?? '0 2 * * *';
                        $parts = explode(' ', $cron);
                        $hour = (int) ($parts[1] ?? 2);
                        $dom = $parts[2] ?? '*';
                        $dow = $parts[4] ?? '*';

                        // Detect frequency from cron pattern
                        if ($dom !== '*') {
                            $frequency = 'monthly';
                        } elseif ($dow !== '*' && ! str_contains($dow, ',')) {
                            $frequency = 'weekly';
                        } else {
                            $frequency = 'daily';
                        }

                        return [
                            'name' => $record['name'] ?? '',
                            'destination' => $record['destination'] ?? '',
                            'frequency' => $frequency,
                            'every_day' => $frequency === 'daily' && $dow === '*',
                            'days' => $frequency === 'daily' && $dow !== '*' ? explode(',', $dow) : [],
                            'week_day' => $frequency === 'weekly' ? $dow : '0',
                            'month_day' => $frequency === 'monthly' ? (int) $dom : 1,
                            'hour' => $hour,
                            'account_mode' => ($record['accounts'] ?? 'all') === 'all' ? 'all' : 'specific',
                            'accounts' => ($record['accounts'] ?? 'all') !== 'all' ? explode(',', $record['accounts']) : [],
                            'exclude' => ! empty($record['exclude']) ? explode(',', $record['exclude']) : [],
                            'retention' => $record['retention']['keep_last'] ?? $record['retention']['keep_daily'] ?? 7,
                        ];
                    })
                    ->schema($this->getScheduleFormSchema())
                    ->action(function (array $data, array $record): void {
                        $hour = (int) ($data['hour'] ?? 2);
                        $freq = $data['frequency'] ?? 'daily';
                        $cron = match ($freq) {
                            'daily' => ! empty($data['every_day'])
                                ? "0 {$hour} * * *"
                                : '0 ' . $hour . ' * * ' . implode(',', $data['days'] ?? ['*']),
                            'weekly' => "0 {$hour} * * " . ($data['week_day'] ?? '0'),
                            'monthly' => "0 {$hour} " . ($data['month_day'] ?? '1') . ' * *',
                            default => "0 {$hour} * * *",
                        };
                        $accounts = ($data['account_mode'] ?? 'all') === 'all' ? 'all' : implode(',', $data['accounts'] ?? []);

                        $params = ['id' => $record['id'], 'name' => $data['name'], 'destination' => $data['destination'], 'cron' => $cron, 'accounts' => $accounts, 'exclude' => implode(',', $data['exclude'] ?? [])];

                        try {
                            $result = app(AgentClient::class)->send('jb.schedules_update', $params);
                            if ($result['success'] ?? false) {
                                Notification::make()->title(__('Schedule updated'))->success()->send();
                                $this->loadSchedules();
                            } else {
                                Notification::make()->title(__('Error'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
                            }
                        } catch (\Throwable $e) {
                            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
                Action::make('removeSchedule')
                    ->label(__('Remove'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->size('sm')
                    ->requiresConfirmation()
                    ->modalHeading(__('Remove Schedule'))
                    ->modalDescription(__('The cron job will be removed.'))
                    ->action(function (array $record): void {
                        try {
                            $result = app(AgentClient::class)->send('jb.schedules_remove', ['id' => $record['id']]);
                            if ($result['success'] ?? false) {
                                Notification::make()->title(__('Schedule removed'))->success()->send();
                                $this->loadSchedules();
                            } else {
                                Notification::make()->title(__('Error'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
                            }
                        } catch (\Throwable $e) {
                            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
            ])
            ->heading(__('Backup Schedules'))
            ->description(__(':count job(s)', ['count' => count($this->schedules)]))
            ->toolbarActions([
                Action::make('addSchedule')
                    ->label(__('Add Schedule'))
                    ->icon('heroicon-o-plus')
                    ->color('primary')
                    ->schema($this->getScheduleFormSchema())
                    ->action(function (array $data): void {
                        $hour = (int) ($data['hour'] ?? 2);
                        $freq = $data['frequency'] ?? 'daily';
                        $cron = match ($freq) {
                            'daily' => ! empty($data['every_day'])
                                ? "0 {$hour} * * *"
                                : '0 ' . $hour . ' * * ' . implode(',', $data['days'] ?? ['*']),
                            'weekly' => "0 {$hour} * * " . ($data['week_day'] ?? '0'),
                            'monthly' => "0 {$hour} " . ($data['month_day'] ?? '1') . ' * *',
                            default => "0 {$hour} * * *",
                        };
                        $accounts = ($data['account_mode'] ?? 'all') === 'all' ? 'all' : implode(',', $data['accounts'] ?? []);

                        $retention = (int) ($data['retention'] ?? 7);
                        $keepKey = match ($freq ?? 'daily') {
                            'weekly' => 'keep_weekly',
                            'monthly' => 'keep_monthly',
                            default => 'keep_daily',
                        };
                        $params = [
                            'name' => $data['name'], 'destination' => $data['destination'],
                            'cron' => $cron, 'accounts' => $accounts,
                            'exclude' => implode(',', $data['exclude'] ?? []),
                            $keepKey => (string) $retention,
                            'keep_last' => (string) $retention,
                        ];

                        try {
                            $result = app(AgentClient::class)->send('jb.schedules_add', $params);
                            if ($result['success'] ?? false) {
                                Notification::make()->title(__('Schedule added'))->success()->send();
                                $this->loadSchedules();
                            } else {
                                Notification::make()->title(__('Failed to add schedule'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
                            }
                        } catch (\Throwable $e) {
                            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
                Action::make('refreshSchedules')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(fn () => $this->loadSchedules()),
            ]);
    }

    public function runScheduleNow(array $record): void
    {
        $params = [];
        if (! empty($record['destination'])) {
            $params['destination'] = $record['destination'];
        }

        $accounts = $record['accounts'] ?? 'all';
        if ($accounts !== 'all') {
            $params['accounts'] = $accounts;
        }

        if (! empty($record['exclude'])) {
            $params['exclude_accounts'] = $record['exclude'];
        }

        try {
            $result = app(AgentClient::class)->send('jb.run', $params);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Backup started'))->body($result['message'] ?? '')->success()->send();
            } else {
                Notification::make()->title(__('Backup failed'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function toggleSchedule(string $id, bool $enable): void
    {
        try {
            $route = $enable ? 'jb.schedules_enable' : 'jb.schedules_disable';
            $result = app(AgentClient::class)->send($route, ['id' => $id]);
            if ($result['success'] ?? false) {
                Notification::make()->title($enable ? __('Schedule enabled') : __('Schedule disabled'))->success()->send();
                $this->loadSchedules();
            } else {
                Notification::make()->title(__('Error'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    // ─── Destinations ───

    public function loadDestinations(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.destinations_list', []);
            $this->destinations = $result['destinations'] ?? [];
        } catch (\Throwable $e) {
            $this->destinations = [];
        }
        $this->resetTable();
    }


    protected function getAddDestinationFormSchema(): array
    {
        return [
            TextInput::make('name')->label(__('Name'))->placeholder('My Backup Server')->required()->maxLength(60),
            Select::make('type')->label(__('Backend Type'))->options([
                'local' => __('Local Directory'),
                'sftp' => __('SFTP / SSH'),
                's3' => __('Amazon S3 / Wasabi / MinIO'),
                'b2' => __('Backblaze B2'),
                'gcs' => __('Google Cloud Storage'),
                'azure' => __('Azure Blob Storage'),
                'rest' => __('REST Server'),
                'rclone' => __('Rclone Remote'),
                'gdrive' => __('Google Drive (rclone)'),
            ])->required()->live(),
            TextInput::make('path')->label(__('Directory Path'))->placeholder('/var/backups/jabali')->visible(fn ($get) => in_array($get('type'), ['local', 'rest']))->requiredIf('type', 'local'),
            TextInput::make('host')->label(__('Host'))->placeholder('backup.example.com')->visible(fn ($get) => $get('type') === 'sftp')->requiredIf('type', 'sftp'),
            Grid::make(2)->schema([
                TextInput::make('user')->label(__('Username'))->placeholder('backup')->visible(fn ($get) => $get('type') === 'sftp'),
                TextInput::make('port')->label(__('Port'))->placeholder('22')->numeric()->visible(fn ($get) => $get('type') === 'sftp'),
            ]),
            TextInput::make('sftp_path')->label(__('Remote Path'))->placeholder('/data/jabali')->visible(fn ($get) => $get('type') === 'sftp')->requiredIf('type', 'sftp'),
            Select::make('ssh_auth')->label(__('Authentication'))->options(['key' => __('SSH Key'), 'password' => __('Password')])->default('key')->live()->visible(fn ($get) => $get('type') === 'sftp'),
            Select::make('ssh_key')->label(__('SSH Key'))->options(fn () => $this->getSshKeyOptions())->placeholder(__('Select a key from /root/.ssh/'))->visible(fn ($get) => $get('type') === 'sftp' && ($get('ssh_auth') ?? 'key') === 'key'),
            TextInput::make('ssh_password')->label(__('SSH Password'))->password()->revealable()->visible(fn ($get) => $get('type') === 'sftp' && ($get('ssh_auth') ?? 'key') === 'password'),
            TextInput::make('bucket')->label(__('Bucket'))->placeholder('my-backup-bucket')->visible(fn ($get) => in_array($get('type'), ['s3', 'b2', 'gcs', 'azure']))->requiredIf('type', 's3'),
            Grid::make(2)->schema([
                TextInput::make('endpoint')->label(__('Endpoint'))->placeholder('s3.amazonaws.com')->visible(fn ($get) => $get('type') === 's3'),
                TextInput::make('region')->label(__('Region'))->placeholder('us-east-1')->visible(fn ($get) => $get('type') === 's3'),
            ]),
            Grid::make(2)->schema([
                TextInput::make('access_key')->label(__('Access Key'))->password()->revealable()->visible(fn ($get) => $get('type') === 's3'),
                TextInput::make('secret_key')->label(__('Secret Key'))->password()->revealable()->visible(fn ($get) => $get('type') === 's3'),
            ]),
            Grid::make(2)->schema([
                TextInput::make('account_id')->label(__('Account ID'))->password()->revealable()->visible(fn ($get) => $get('type') === 'b2'),
                TextInput::make('account_key')->label(__('Account Key'))->password()->revealable()->visible(fn ($get) => in_array($get('type'), ['b2', 'azure'])),
            ]),
            TextInput::make('account_name')->label(__('Account Name'))->visible(fn ($get) => $get('type') === 'azure'),
            TextInput::make('remote')->label(__('Rclone Remote Name'))->placeholder('gdrive')->visible(fn ($get) => in_array($get('type'), ['rclone', 'gdrive'])),
            TextInput::make('rclone_path')->label(__('Remote Path'))->placeholder('/backups/jabali')->visible(fn ($get) => in_array($get('type'), ['rclone', 'gdrive'])),
            TextInput::make('rest_url')->label(__('REST Server URL'))->placeholder('https://user:pass@host:8000/')->visible(fn ($get) => $get('type') === 'rest'),
            Toggle::make('encrypt')->label(__('Encrypt backups'))->default(false)->live()->helperText(__('Encrypted backups require an encryption key. Losing the key means losing access to your backups.')),
            TextInput::make('restic_password')->label(__('Encryption Key'))->password()->revealable()->placeholder(__('Auto-generated if empty'))->visible(fn ($get) => (bool) $get('encrypt')),
        ];
    }

    public function executeAddDestination(array $data): void
    {
        $params = ['name' => $data['name'], 'type' => $data['type']];

        if (empty($data['encrypt'])) {
            $params['no_encryption'] = '1';
        } elseif (! empty($data['restic_password'])) {
            $params['restic_password'] = $data['restic_password'];
        }

        match ($data['type']) {
            'local' => $params['path'] = $data['path'] ?? '',
            'sftp' => $params += array_filter([
                'host' => $data['host'] ?? '', 'user' => $data['user'] ?? '',
                'port' => $data['port'] ?? '', 'path' => $data['sftp_path'] ?? '',
                'ssh_key' => $data['ssh_key'] ?? '', 'ssh_password' => $data['ssh_password'] ?? '',
            ]),
            's3' => $params += array_filter([
                'bucket' => $data['bucket'] ?? '', 'endpoint' => $data['endpoint'] ?? 's3.amazonaws.com',
                'region' => $data['region'] ?? '', 'access_key' => $data['access_key'] ?? '',
                'secret_key' => $data['secret_key'] ?? '',
            ]),
            'b2' => $params += array_filter(['bucket' => $data['bucket'] ?? '', 'account_id' => $data['account_id'] ?? '', 'account_key' => $data['account_key'] ?? '']),
            'gcs' => $params += array_filter(['bucket' => $data['bucket'] ?? '']),
            'azure' => $params += array_filter(['bucket' => $data['bucket'] ?? '', 'account_name' => $data['account_name'] ?? '', 'account_key' => $data['account_key'] ?? '']),
            'rest' => $params['path'] = $data['rest_url'] ?? '',
            'rclone', 'gdrive' => $params += array_filter(['remote' => $data['remote'] ?? '', 'path' => $data['rclone_path'] ?? '']),
            default => null,
        };

        try {
            $result = app(AgentClient::class)->send('jb.destinations_add', $params);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Destination added'))->success()->send();
                $this->loadDestinations();
            } else {
                $error = $result['error'] ?? $result['output'] ?? __('Unknown error');
                Notification::make()->title(__('Failed to add destination'))->body($error)->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Failed to add destination'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function testDestination(string $id): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.destinations_test', ['id' => $id]);
            $ok = $result['success'] ?? false;
            Notification::make()
                ->title($ok ? __('Connection OK') : __('Connection failed'))
                ->body($result['output'] ?? $result['error'] ?? '')
                ->{$ok ? 'success' : 'danger'}()->send();
            $this->loadDestinations();
        } catch (\Throwable $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function initDestination(string $id): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.destinations_init', ['id' => $id]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Repository initialized'))->success()->send();
                $this->loadDestinations();
            } else {
                Notification::make()->title(__('Init failed'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Init failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function setDefaultDestination(string $id): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.destinations_set_default', ['id' => $id]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Default updated'))->success()->send();
                $this->loadDestinations();
            } else {
                Notification::make()->title(__('Failed'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function reinitDestination(string $id): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.destinations_reinit', ['id' => $id]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Repository re-initialized'))->body($result['output'] ?? '')->success()->send();
                $this->loadDestinations();
            } else {
                Notification::make()->title(__('Re-init failed'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Re-init failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function removeDestination(string $id): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.destinations_remove', ['id' => $id]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Destination removed'))->success()->send();
                $this->loadDestinations();
            } else {
                Notification::make()->title(__('Cannot remove'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    // ─── Logs ───

    public function loadLogs(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.logs', ['lines' => 500]);
            $this->logJobs = $result['jobs'] ?? [];
        } catch (\Throwable) {
            $this->logJobs = [];
        }
    }

    protected function logsTable(Table $table): Table
    {
        return $table
            ->records(fn (array $filters) => collect($this->logJobs)
                ->when(
                    ($filters['type']['value'] ?? null) !== null,
                    fn ($c) => $c->filter(fn ($job) => $filters['type']['value']
                        ? ($job['type'] ?? '') === 'backup'
                        : in_array($job['type'] ?? '', ['restore', 'file-restore'], true)
                    )
                )
                ->mapWithKeys(fn ($job, $i) => [$job['id'] ?? "job-{$i}" => $job])
                ->all())
            ->columns([
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->formatStateUsing(fn (array $record): string => ucfirst($record['type'] ?? 'backup'))
                    ->icon(fn (array $record): string => match ($record['type'] ?? 'backup') {
                        'restore' => 'heroicon-o-arrow-path',
                        'file-restore' => 'heroicon-o-document-arrow-down',
                        default => 'heroicon-o-cloud-arrow-up',
                    })
                    ->color(fn (array $record): string => match ($record['type'] ?? 'backup') {
                        'restore', 'file-restore' => 'info',
                        default => 'primary',
                    }),
                TextColumn::make('accounts')
                    ->label(__('Accounts'))
                    ->formatStateUsing(fn (array $record): string => implode(', ', $record['accounts'] ?? []) ?: '-'),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->color(fn (array $record): string => match ($record['status'] ?? '') {
                        'success' => 'success',
                        'partial' => 'warning',
                        'failed' => 'danger',
                        default => 'gray',
                    })
                    ->formatStateUsing(fn (array $record): string => ucfirst($record['status'] ?? 'running')),
                TextColumn::make('started_at')
                    ->label(__('Date'))
                    ->formatStateUsing(fn (array $record): string => $record['started_at'] ?? ''),
                TextColumn::make('duration_seconds')
                    ->label(__('Duration'))
                    ->formatStateUsing(fn (array $record): string => isset($record['duration_seconds']) ? $record['duration_seconds'] . 's' : '-')
                    ->alignEnd(),
            ])
            ->filters([
                \Filament\Tables\Filters\TernaryFilter::make('type')
                    ->label(__('Type'))
                    ->placeholder(__('All'))
                    ->trueLabel(__('Backup Logs'))
                    ->falseLabel(__('Restore Logs')),
            ], layout: \Filament\Tables\Enums\FiltersLayout::AboveContent)
            ->actions([
                \Filament\Actions\Action::make('viewLog')
                    ->label(__('View Log'))
                    ->icon('heroicon-o-eye')
                    ->color('gray')
                    ->modalHeading(fn (array $record): string => ucfirst($record['type'] ?? 'backup') . ' — ' . ($record['started_at'] ?? ''))
                    ->modalContent(function (array $record): \Illuminate\Contracts\View\View {
                        return view('filament.admin.pages.partials.log-modal', [
                            'events' => $record['events'] ?? [],
                            'log' => $record['log'] ?? [],
                        ]);
                    })
                    ->modalSubmitAction(false)
                    ->modalCancelActionLabel(__('Close')),
            ])
            ->headerActions([
                \Filament\Actions\Action::make('refreshLogs')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(fn () => $this->loadLogs()),
            ])
            ->emptyStateHeading(__('No jobs found'))
            ->emptyStateIcon('heroicon-o-document-text')
            ->paginated(false);
    }


    // ─── Settings ───


    public function runForget(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.forget', ['dry_run' => false]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Retention applied'))->success()->send();
                $this->loadSnapshots();
            } else {
                Notification::make()->title(__('Error'))->body($result['error'] ?? $result['output'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    // ─── Helpers ───

    public function getUserOptions(): array
    {
        return User::where('is_active', true)->orderBy('username')
            ->pluck('username', 'username')->prepend(__('All accounts'), '')->toArray();
    }

    public function getSshKeyOptions(): array
    {
        try {
            $result = app(AgentClient::class)->send('jb.ssh_keys', []);
            $keys = $result['keys'] ?? [];
            $options = [];
            foreach ($keys as $key) {
                $options[$key['path']] = $key['name'] . ($key['type'] !== '' ? ' (' . $key['type'] . ')' : '');
            }

            return $options;
        } catch (\Throwable) {
            return [];
        }
    }

    public function formatBytes(int|float|null $bytes): string
    {
        return Formatter::bytes($bytes ?? 0);
    }
}
