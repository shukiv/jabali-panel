<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Models\Backup;
use App\Models\BackupDestination;
use App\Models\BackupSchedule;
use App\Models\User;
use App\Services\Agent\AgentClient;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Radio;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Actions as FormActions;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Columns\ViewColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Model;
use Livewire\Attributes\Url;

class Backups extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-cloud-arrow-up';

    protected static ?int $navigationSort = 11;

    public static function getNavigationLabel(): string
    {
        return __('Backups');
    }

    protected string $view = 'filament.admin.pages.backups';

    #[Url(as: 'tab')]
    public ?string $activeTab = 'destinations';

    protected ?AgentClient $agent = null;

    public function getTitle(): string|Htmlable
    {
        return __('Server Backups');
    }

    public function mount(): void
    {
        $this->activeTab = $this->normalizeTabName($this->activeTab);
    }

    protected function normalizeTabName(?string $tab): string
    {
        return match ($tab) {
            'destinations', 'schedules', 'backups' => $tab,
            default => 'destinations',
        };
    }

    public function setTab(string $tab): void
    {
        $this->activeTab = $this->normalizeTabName($tab);
        $this->resetTable();
    }

    public function updatedActiveTab(): void
    {
        $this->activeTab = $this->normalizeTabName($this->activeTab);
        $this->resetTable();
    }

    protected function getForms(): array
    {
        return [
            'backupsForm',
        ];
    }

    public function backupsForm(Schema $schema): Schema
    {
        return $schema
            ->schema([
                Section::make(__('Recommendation'))
                    ->description(__('Use Incremental Backups for scheduled server backups. They only store changes since the last backup, significantly reducing storage space and backup time while maintaining full restore capability.'))
                    ->icon('heroicon-o-light-bulb')
                    ->iconColor('info')
                    ->collapsed(false)
                    ->collapsible(false),
                Tabs::make(__('Backup Sections'))
                    ->contained()
                    ->livewireProperty('activeTab')
                    ->tabs([
                        'destinations' => Tab::make(__('Destinations'))
                            ->icon('heroicon-o-server-stack')
                            ->schema([
                                View::make('filament.admin.pages.backups-tab-table'),
                            ]),
                        'schedules' => Tab::make(__('Schedules'))
                            ->icon('heroicon-o-calendar-days')
                            ->schema([
                                View::make('filament.admin.pages.backups-tab-table'),
                            ]),
                        'backups' => Tab::make(__('Backups'))
                            ->icon('heroicon-o-archive-box')
                            ->schema([
                                View::make('filament.admin.pages.backups-tab-table'),
                            ]),
                    ]),
            ]);
    }

    public function getAgent(): AgentClient
    {
        return $this->agent ??= new AgentClient;
    }

    protected function supportsIncremental($destinationId): bool
    {
        if (empty($destinationId)) {
            return false;
        }

        $destination = BackupDestination::find($destinationId);
        if (! $destination) {
            return false;
        }

        return in_array($destination->type, ['sftp', 'nfs']);
    }

    public function table(Table $table): Table
    {
        return match ($this->activeTab) {
            'destinations' => $this->destinationsTable($table),
            'schedules' => $this->schedulesTable($table),
            'backups' => $this->backupsTable($table),
            default => $this->destinationsTable($table),
        };
    }

    protected function destinationsTable(Table $table): Table
    {
        return $table
            ->query(BackupDestination::query()->where('is_server_backup', true)->orderBy('name'))
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->weight('medium')
                    ->description(fn (BackupDestination $record): ?string => $record->is_default ? __('Default') : null)
                    ->searchable(),
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => strtoupper($state))
                    ->color(fn (string $state): string => match ($state) {
                        'sftp' => 'info',
                        'nfs' => 'warning',
                        's3' => 'success',
                        default => 'gray',
                    }),
                TextColumn::make('test_status')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (?string $state): string => match ($state) {
                        'success' => __('Connected'),
                        'failed' => __('Failed'),
                        default => __('Not Tested'),
                    })
                    ->color(fn (?string $state): string => match ($state) {
                        'success' => 'success',
                        'failed' => 'danger',
                        default => 'gray',
                    }),
                TextColumn::make('last_tested_at')
                    ->label(__('Last Tested'))
                    ->since()
                    ->placeholder(__('Never'))
                    ->color('gray'),
            ])
            ->recordActions([
                Action::make('test')
                    ->label(__('Test'))
                    ->icon('heroicon-o-check-circle')
                    ->color('success')
                    ->size('sm')
                    ->action(fn (BackupDestination $record) => $this->testDestination($record->id)),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->size('sm')
                    ->requiresConfirmation()
                    ->action(fn (BackupDestination $record) => $this->deleteDestination($record->id)),
            ])
            ->emptyStateHeading(__('No remote destinations configured'))
            ->emptyStateDescription(__('Click "Add Destination" to configure SFTP, NFS, or S3 storage'))
            ->emptyStateIcon('heroicon-o-server-stack')
            ->striped();
    }

    protected function schedulesTable(Table $table): Table
    {
        return $table
            ->query(BackupSchedule::query()->where('is_server_backup', true)->with('destination')->orderBy('name'))
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->weight('medium')
                    ->searchable(),
                TextColumn::make('frequency_label')
                    ->label(__('Frequency')),
                TextColumn::make('destination.name')
                    ->label(__('Destination'))
                    ->placeholder(__('Local')),
                TextColumn::make('retention_count')
                    ->label(__('Retention'))
                    ->formatStateUsing(fn (int $state): string => $state.' '.__('backups')),
                TextColumn::make('last_run_at')
                    ->label(__('Last Run'))
                    ->since()
                    ->dateTimeTooltip('M j, Y H:i T', timezone: $this->getSystemTimezone())
                    ->placeholder(__('Never'))
                    ->color('gray'),
                TextColumn::make('next_run_at')
                    ->label(__('Next Run'))
                    ->since()
                    ->dateTimeTooltip('M j, Y H:i T', timezone: $this->getSystemTimezone())
                    ->placeholder(__('Not scheduled'))
                    ->color('gray'),
                ViewColumn::make('status')
                    ->label(__('Status'))
                    ->view('filament.admin.columns.schedule-status'),
            ])
            ->recordActions([
                Action::make('run')
                    ->label(__('Run'))
                    ->icon('heroicon-o-play')
                    ->color('gray')
                    ->size('sm')
                    ->visible(fn (BackupSchedule $record): bool => ! Backup::where('schedule_id', $record->id)->running()->exists())
                    ->action(fn (BackupSchedule $record) => $this->runScheduleNow($record->id)),
                Action::make('stop')
                    ->label(__('Stop'))
                    ->icon('heroicon-o-stop')
                    ->color('danger')
                    ->size('sm')
                    ->visible(fn (BackupSchedule $record): bool => Backup::where('schedule_id', $record->id)->running()->exists())
                    ->requiresConfirmation()
                    ->action(fn (BackupSchedule $record) => $this->stopScheduleBackup($record->id)),
                Action::make('edit')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil')
                    ->color('gray')
                    ->size('sm')
                    ->action(fn (BackupSchedule $record) => $this->mountAction('editSchedule', ['id' => $record->id])),
                Action::make('toggle')
                    ->label(fn (BackupSchedule $record): string => $record->is_active ? __('Disable') : __('Enable'))
                    ->icon(fn (BackupSchedule $record): string => $record->is_active ? 'heroicon-o-pause' : 'heroicon-o-play')
                    ->color('gray')
                    ->size('sm')
                    ->action(fn (BackupSchedule $record) => $this->toggleSchedule($record->id)),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->size('sm')
                    ->requiresConfirmation()
                    ->action(fn (BackupSchedule $record) => $this->deleteSchedule($record->id)),
            ])
            ->headerActions([
                $this->addScheduleAction(),
            ])
            ->emptyStateHeading(__('No backup schedules configured'))
            ->emptyStateDescription(__('Click "Add Schedule" to set up automatic backups'))
            ->emptyStateIcon('heroicon-o-clock')
            ->striped()
            ->poll(fn () => Backup::running()->exists() ? '3s' : null);
    }

    protected function backupsTable(Table $table): Table
    {
        return $table
            ->query(Backup::query()->where('type', 'server')->with(['destination', 'user'])->orderByDesc('created_at')->limit(50))
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->weight('medium')
                    ->searchable()
                    ->limit(40),
                ViewColumn::make('status')
                    ->label(__('Status'))
                    ->view('filament.admin.columns.backup-status'),
                TextColumn::make('size_bytes')
                    ->label(__('Size'))
                    ->formatStateUsing(fn (Backup $record): string => $record->size_human),
                TextColumn::make('destination.name')
                    ->label(__('Destination'))
                    ->placeholder(__('Local')),
                TextColumn::make('created_at')
                    ->label(__('Created'))
                    ->dateTime('M j, Y H:i')
                    ->color('gray'),
                TextColumn::make('duration')
                    ->label(__('Duration'))
                    ->placeholder(__('-'))
                    ->color('gray'),
            ])
            ->recordActions([
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('warning')
                    ->size('sm')
                    ->visible(fn (Backup $record): bool => $record->status === 'completed' && ($record->local_path || $record->remote_path))
                    ->modalHeading(__('Restore Backup'))
                    ->modalDescription(__('Select what you want to restore. Warning: Existing data may be overwritten.'))
                    ->modalWidth('xl')
                    ->form(function (Backup $record): array {
                        // Check if this is a remote backup (no local files)
                        $isRemoteBackup = ! $record->local_path || ! file_exists($record->local_path);

                        $manifest = $this->getBackupManifest($record);
                        $users = $manifest['users'] ?? $record->users ?? [];
                        if (empty($users)) {
                            $users = [$manifest['username'] ?? ''];
                        }
                        $users = array_filter($users);

                        $isServerBackup = ($manifest['type'] ?? $record->type) === 'server' && count($users) > 1;

                        // For server backups, get data for first user by default
                        $selectedUser = $users[0] ?? '';
                        if ($isServerBackup && ! empty($selectedUser) && ! $isRemoteBackup) {
                            $manifest = $this->getBackupManifest($record, $selectedUser);
                        }

                        // For remote backups, use include_* flags from record
                        if ($isRemoteBackup) {
                            $hasFiles = $record->include_files ?? true;
                            $hasDatabases = $record->include_databases ?? true;
                            $hasMailboxes = $record->include_mailboxes ?? true;
                            $hasDns = $record->include_dns ?? true;
                            $hasSsl = true; // Assume SSL is included
                            $domains = [];
                            $databases = [];
                            $mailboxes = [];
                        } else {
                            $domains = $manifest['domains'] ?? [];
                            $databases = $manifest['databases'] ?? [];
                            $mailboxes = $manifest['mailboxes'] ?? [];
                            $hasFiles = ! empty($domains);
                            $hasDatabases = ! empty($databases);
                            $hasMailboxes = ! empty($mailboxes);
                            $hasDns = ! empty($manifest['dns_zones'] ?? []);
                            $hasSsl = ! empty($manifest['ssl_certificates'] ?? []);
                        }

                        $schema = [];

                        // Backup info section
                        $infoSchema = [
                            TextInput::make('backup_name')
                                ->label(__('Backup'))
                                ->default($record->name)
                                ->disabled(),
                        ];

                        // Add user selector for server backups with multiple users
                        if ($isServerBackup || count($users) > 1) {
                            $userOptions = [
                                '__all__' => __('All users'),
                            ];
                            foreach ($users as $userOption) {
                                $userOptions[$userOption] = $userOption;
                            }
                            $infoSchema[] = Select::make('restore_username')
                                ->label(__('User to Restore'))
                                ->options($userOptions)
                                ->default($selectedUser)
                                ->required()
                                ->helperText(__('Backup contains :count user(s)', ['count' => count($users)]));
                        } else {
                            $infoSchema[] = TextInput::make('restore_username')
                                ->label(__('User'))
                                ->default($selectedUser)
                                ->disabled();
                        }

                        $schema[] = Section::make(__('Backup Information'))
                            ->schema([Grid::make(2)->schema($infoSchema)]);

                        // Remote backup notice
                        if ($isRemoteBackup) {
                            $schema[] = Section::make(__('Remote Backup'))
                                ->description(__('This backup will be downloaded from the remote destination before restoring.'))
                                ->icon('heroicon-o-cloud-arrow-down')
                                ->iconColor('info');
                        }

                        // Restore options section
                        $restoreOptions = [];

                        // Website Files
                        $filesLabel = __('Website Files');
                        if (! $isRemoteBackup && ! empty($domains)) {
                            $filesLabel .= ' ('.count($domains).')';
                        }
                        $restoreOptions[] = Toggle::make('restore_files')
                            ->label($filesLabel)
                            ->helperText($isRemoteBackup
                                ? __('Restore all domain files')
                                : (! empty($domains) ? implode(', ', array_slice($domains, 0, 3)).(count($domains) > 3 ? '...' : '') : __('No files')))
                            ->default($hasFiles)
                            ->disabled(! $hasFiles && ! $isRemoteBackup);

                        if (! $isRemoteBackup && count($domains) > 1) {
                            $restoreOptions[] = Select::make('selected_domains')
                                ->label(__('Specific Domains'))
                                ->multiple()
                                ->options(fn () => array_combine($domains, $domains))
                                ->placeholder(__('All domains'))
                                ->visible(fn ($get) => $get('restore_files'));
                        }

                        // Databases
                        $dbLabel = __('Databases');
                        if (! $isRemoteBackup && ! empty($databases)) {
                            $dbLabel .= ' ('.count($databases).')';
                        }
                        $restoreOptions[] = Toggle::make('restore_databases')
                            ->label($dbLabel)
                            ->helperText($isRemoteBackup
                                ? __('Restore all databases')
                                : (! empty($databases) ? implode(', ', array_slice($databases, 0, 3)).(count($databases) > 3 ? '...' : '') : __('No databases')))
                            ->default($hasDatabases)
                            ->disabled(! $hasDatabases && ! $isRemoteBackup);

                        if (! $isRemoteBackup && count($databases) > 1) {
                            $restoreOptions[] = Select::make('selected_databases')
                                ->label(__('Specific Databases'))
                                ->multiple()
                                ->options(fn () => array_combine($databases, $databases))
                                ->placeholder(__('All databases'))
                                ->visible(fn ($get) => $get('restore_databases'));
                        }

                        // MySQL Users
                        $restoreOptions[] = Toggle::make('restore_mysql_users')
                            ->label(__('MySQL Users'))
                            ->default($hasDatabases)
                            ->helperText(__('Restore MySQL users and their permissions'));

                        // Mailboxes
                        $mailLabel = __('Mailboxes');
                        if (! $isRemoteBackup && ! empty($mailboxes)) {
                            $mailLabel .= ' ('.count($mailboxes).')';
                        }
                        $restoreOptions[] = Toggle::make('restore_mailboxes')
                            ->label($mailLabel)
                            ->helperText($isRemoteBackup
                                ? __('Restore all mailboxes')
                                : (! empty($mailboxes) ? implode(', ', array_slice($mailboxes, 0, 3)).(count($mailboxes) > 3 ? '...' : '') : __('No mailboxes')))
                            ->default($hasMailboxes)
                            ->disabled(! $hasMailboxes && ! $isRemoteBackup);

                        // SSL Certificates
                        $restoreOptions[] = Toggle::make('restore_ssl')
                            ->label(__('SSL Certificates'))
                            ->default(false)
                            ->helperText(__('Restore SSL certificates for domains'));

                        // DNS Zones
                        $restoreOptions[] = Toggle::make('restore_dns')
                            ->label(__('DNS Zones'))
                            ->default($hasDns)
                            ->helperText(__('Restore DNS zone files'));

                        $schema[] = Section::make(__('Restore Options'))
                            ->description(__('Toggle items you want to restore.'))
                            ->schema($restoreOptions);

                        return $schema;
                    })
                    ->action(function (array $data, Backup $record): void {
                        $this->executeRestore($record, $data);
                    })
                    ->modalSubmitActionLabel(__('Restore'))
                    ->requiresConfirmation(),
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->size('sm')
                    ->visible(fn (Backup $record): bool => $record->canDownload())
                    ->url(fn (Backup $record): string => route('filament.admin.pages.backup-download', ['id' => $record->id]))
                    ->openUrlInNewTab(),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->size('sm')
                    ->requiresConfirmation()
                    ->action(fn (Backup $record) => $this->deleteBackup($record->id)),
            ])
            ->emptyStateHeading(__('No server backups yet'))
            ->emptyStateDescription(__('Click "Create Server Backup" to create your first backup'))
            ->emptyStateIcon('heroicon-o-archive-box')
            ->striped()
            ->poll(fn () => Backup::whereIn('status', ['pending', 'running', 'uploading'])->exists() ? '3s' : null);
    }

    public function getTableRecordKey(Model|array $record): string
    {
        return is_array($record) ? (string) $record['id'] : (string) $record->getKey();
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('createServerBackup')
                ->label(__('Create Server Backup'))
                ->icon('heroicon-o-archive-box-arrow-down')
                ->color('primary')
                ->form([
                    TextInput::make('name')
                        ->label(__('Backup Name'))
                        ->default(fn () => __('Server Backup').' '.now()->format('Y-m-d H:i'))
                        ->required(),
                    Select::make('destination_id')
                        ->label(__('Destination'))
                        ->options(fn () => BackupDestination::where('is_server_backup', true)
                            ->where('is_active', true)
                            ->pluck('name', 'id')
                            ->prepend(__('Local Storage'), ''))
                        ->default('')
                        ->live()
                        ->afterStateUpdated(fn ($set, $state) => $set('backup_type', $this->supportsIncremental($state) ? 'incremental' : 'full')),
                    Radio::make('backup_type')
                        ->label(__('Backup Type'))
                        ->options(fn ($get) => $this->supportsIncremental($get('destination_id'))
                            ? [
                                'incremental' => __('Incremental (rsync) - Space-efficient'),
                                'full' => __('Full (tar.gz) - Complete archive'),
                            ]
                            : [
                                'full' => __('Full (tar.gz) - Complete archive'),
                            ])
                        ->default('full')
                        ->required(),
                    TextInput::make('local_path')
                        ->label(__('Local Backup Folder'))
                        ->default('/var/backups/jabali')
                        ->visible(fn ($get) => empty($get('destination_id'))),
                    Section::make(__('Include'))
                        ->schema([
                            Grid::make(2)->schema([
                                Toggle::make('include_files')->label(__('Website Files'))->default(true),
                                Toggle::make('include_databases')->label(__('Databases'))->default(true),
                                Toggle::make('include_mailboxes')->label(__('Mailboxes'))->default(true),
                                Toggle::make('include_dns')->label(__('DNS Records'))->default(true),
                            ]),
                        ]),
                    Select::make('users')
                        ->label(__('Users to Backup'))
                        ->multiple()
                        ->options(fn () => User::where('is_admin', false)
                            ->where('is_active', true)
                            ->pluck('username', 'username'))
                        ->placeholder(__('All Users')),
                ])
                ->action(function (array $data) {
                    $this->createServerBackup($data);
                }),

            $this->createUserBackupAction(),

            Action::make('addDestination')
                ->label(__('Add Destination'))
                ->icon('heroicon-o-plus')
                ->color('gray')
                ->form($this->getDestinationForm())
                ->action(function (array $data) {
                    $this->saveDestination($data);
                }),
        ];
    }

    protected function getDestinationForm(): array
    {
        return [
            TextInput::make('name')
                ->label(__('Destination Name'))
                ->required(),
            Select::make('type')
                ->label(__('Type'))
                ->options([
                    'sftp' => __('SFTP Server'),
                    'nfs' => __('NFS Mount'),
                    's3' => __('S3-Compatible Storage'),
                ])
                ->required()
                ->live(),

            Section::make(__('SFTP Settings'))
                ->visible(fn ($get) => $get('type') === 'sftp')
                ->schema([
                    Grid::make(2)->schema([
                        TextInput::make('host')->label(__('Host'))->required(),
                        TextInput::make('port')->label(__('Port'))->numeric()->default(22),
                    ]),
                    TextInput::make('username')->label(__('Username'))->required(),
                    TextInput::make('password')->label(__('Password'))->password(),
                    Textarea::make('private_key')->label(__('Private Key (SSH)'))->rows(4),
                    TextInput::make('path')->label(__('Remote Path'))->default('/backups'),
                ]),

            Section::make(__('NFS Settings'))
                ->visible(fn ($get) => $get('type') === 'nfs')
                ->schema([
                    TextInput::make('server')->label(__('NFS Server'))->required(),
                    TextInput::make('share')->label(__('Share Path'))->required(),
                    TextInput::make('path')->label(__('Sub-directory'))->default(''),
                ]),

            Section::make(__('S3-Compatible Settings'))
                ->visible(fn ($get) => $get('type') === 's3')
                ->schema([
                    TextInput::make('endpoint')->label(__('Endpoint URL')),
                    TextInput::make('bucket')->label(__('Bucket Name'))->required(),
                    Grid::make(2)->schema([
                        TextInput::make('access_key')->label(__('Access Key ID'))->required(),
                        TextInput::make('secret_key')->label(__('Secret Access Key'))->password()->required(),
                    ]),
                    TextInput::make('region')->label(__('Region'))->default('us-east-1'),
                    TextInput::make('path')->label(__('Path Prefix'))->default('backups'),
                ]),

            Toggle::make('is_default')->label(__('Set as Default Destination')),

            FormActions::make([
                Action::make('testConnection')
                    ->label(__('Test Connection'))
                    ->icon('heroicon-o-signal')
                    ->color('gray')
                    ->action(function ($get, $livewire) {
                        $type = $get('type');
                        if (empty($type)) {
                            Notification::make()
                                ->title(__('Select a destination type first'))
                                ->warning()
                                ->send();

                            return;
                        }

                        $config = match ($type) {
                            'sftp' => [
                                'type' => 'sftp',
                                'host' => $get('host') ?? '',
                                'port' => (int) ($get('port') ?? 22),
                                'username' => $get('username') ?? '',
                                'password' => $get('password') ?? '',
                                'private_key' => $get('private_key') ?? '',
                                'path' => $get('path') ?? '/backups',
                            ],
                            'nfs' => [
                                'type' => 'nfs',
                                'server' => $get('server') ?? '',
                                'share' => $get('share') ?? '',
                                'path' => $get('path') ?? '',
                            ],
                            's3' => [
                                'type' => 's3',
                                'endpoint' => $get('endpoint') ?? '',
                                'bucket' => $get('bucket') ?? '',
                                'access_key' => $get('access_key') ?? '',
                                'secret_key' => $get('secret_key') ?? '',
                                'region' => $get('region') ?? 'us-east-1',
                                'path' => $get('path') ?? 'backups',
                            ],
                            default => [],
                        };

                        if (empty($config)) {
                            Notification::make()
                                ->title(__('Invalid destination type'))
                                ->danger()
                                ->send();

                            return;
                        }

                        try {
                            $result = $livewire->getAgent()->backupTestDestination($config);
                            if ($result['success']) {
                                Notification::make()
                                    ->title(__('Connection successful'))
                                    ->body(__('The destination is reachable and ready to use.'))
                                    ->success()
                                    ->send();
                            } else {
                                Notification::make()
                                    ->title(__('Connection failed'))
                                    ->body($result['error'] ?? __('Could not connect to destination'))
                                    ->danger()
                                    ->send();
                            }
                        } catch (Exception $e) {
                            Notification::make()
                                ->title(__('Connection test failed'))
                                ->body($e->getMessage())
                                ->danger()
                                ->send();
                        }
                    }),
            ])->visible(fn ($get) => ! empty($get('type'))),
        ];
    }

    public function saveDestination(array $data): void
    {
        $config = [];
        $type = $data['type'];

        switch ($type) {
            case 'sftp':
                $config = [
                    'host' => $data['host'] ?? '',
                    'port' => (int) ($data['port'] ?? 22),
                    'username' => $data['username'] ?? '',
                    'password' => $data['password'] ?? '',
                    'private_key' => $data['private_key'] ?? '',
                    'path' => $data['path'] ?? '/backups',
                ];
                break;

            case 'nfs':
                $config = [
                    'server' => $data['server'] ?? '',
                    'share' => $data['share'] ?? '',
                    'path' => $data['path'] ?? '',
                ];
                break;

            case 's3':
                $config = [
                    'endpoint' => $data['endpoint'] ?? '',
                    'bucket' => $data['bucket'] ?? '',
                    'access_key' => $data['access_key'] ?? '',
                    'secret_key' => $data['secret_key'] ?? '',
                    'region' => $data['region'] ?? 'us-east-1',
                    'path' => $data['path'] ?? 'backups',
                ];
                break;
        }

        $testConfig = array_merge($config, ['type' => $type]);
        try {
            $result = $this->getAgent()->backupTestDestination($testConfig);
            if (! $result['success']) {
                Notification::make()
                    ->title(__('Connection failed'))
                    ->body($result['error'] ?? __('Could not connect to destination'))
                    ->danger()
                    ->send();

                return;
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Connection test failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();

            return;
        }

        BackupDestination::create([
            'name' => $data['name'],
            'type' => $type,
            'config' => $config,
            'is_server_backup' => true,
            'is_default' => $data['is_default'] ?? false,
            'is_active' => true,
            'last_tested_at' => now(),
            'test_status' => 'success',
        ]);

        Notification::make()->title(__('Destination verified and added'))->success()->send();
        $this->resetTable();
    }

    public function testDestination(int $id): void
    {
        $destination = BackupDestination::find($id);
        if (! $destination) {
            return;
        }

        try {
            $config = array_merge($destination->config ?? [], ['type' => $destination->type]);
            $result = $this->getAgent()->backupTestDestination($config);

            $destination->update([
                'last_tested_at' => now(),
                'test_status' => $result['success'] ? 'success' : 'failed',
                'test_message' => $result['message'] ?? $result['error'] ?? null,
            ]);

            if ($result['success']) {
                Notification::make()->title(__('Connection successful'))->success()->send();
            } else {
                Notification::make()->title(__('Connection failed'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (Exception $e) {
            $destination->update([
                'last_tested_at' => now(),
                'test_status' => 'failed',
                'test_message' => $e->getMessage(),
            ]);
            Notification::make()->title(__('Test failed'))->body($e->getMessage())->danger()->send();
        }

        $this->resetTable();
    }

    public function deleteDestination(int $id): void
    {
        BackupDestination::where('id', $id)->delete();
        Notification::make()->title(__('Destination deleted'))->success()->send();
        $this->resetTable();
    }

    public function createServerBackup(array $data): void
    {
        $backupType = $data['backup_type'] ?? 'full';
        $timestamp = now()->format('Y-m-d_His');
        $folderName = $timestamp;
        $baseFolder = rtrim($data['local_path'] ?? '/var/backups/jabali', '/');
        $outputPath = "{$baseFolder}/{$folderName}";

        $isIncrementalRemote = $backupType === 'incremental' && ! empty($data['destination_id']);

        if ($isIncrementalRemote) {
            $destination = BackupDestination::find($data['destination_id']);
            if (! $destination || ! in_array($destination->type, ['sftp', 'nfs'])) {
                Notification::make()
                    ->title(__('Invalid destination'))
                    ->body(__('Incremental backups require an SFTP or NFS destination'))
                    ->danger()
                    ->send();

                return;
            }
        }

        // Create backup record with pending status
        $backup = Backup::create([
            'name' => $data['name'],
            'filename' => $folderName,
            'type' => 'server',
            'include_files' => $data['include_files'] ?? true,
            'include_databases' => $data['include_databases'] ?? true,
            'include_mailboxes' => $data['include_mailboxes'] ?? true,
            'include_dns' => $data['include_dns'] ?? true,
            'users' => ! empty($data['users']) ? $data['users'] : null,
            'destination_id' => ! empty($data['destination_id']) ? $data['destination_id'] : null,
            'schedule_id' => $data['schedule_id'] ?? null,
            'status' => 'pending',
            'local_path' => $isIncrementalRemote ? null : $outputPath,
            'metadata' => ['backup_type' => $backupType],
        ]);

        // Dispatch job to run backup in background
        \App\Jobs\RunServerBackup::dispatch($backup->id);

        // Show notification and refresh table
        Notification::make()
            ->title(__('Backup started'))
            ->body(__('The backup is running in the background. The status will update automatically.'))
            ->info()
            ->send();

        $this->resetTable();
    }

    protected function uploadToRemote(Backup $backup, bool $keepLocal = false): bool
    {
        if (! $backup->destination || ! $backup->local_path) {
            return false;
        }

        try {
            $backup->update(['status' => 'uploading']);

            $config = array_merge($backup->destination->config ?? [], ['type' => $backup->destination->type]);
            $backupType = $backup->metadata['backup_type'] ?? 'full';
            $result = $this->getAgent()->backupUploadRemote($backup->local_path, $config, $backupType);

            if ($result['success']) {
                $backup->update([
                    'status' => 'completed',
                    'remote_path' => $result['remote_path'] ?? null,
                ]);

                if (! $keepLocal && $backup->local_path) {
                    $this->getAgent()->backupDeleteServer($backup->local_path);
                    $backup->update(['local_path' => null]);
                }

                return true;
            } else {
                throw new Exception($result['error'] ?? __('Upload failed'));
            }
        } catch (Exception $e) {
            $backup->update([
                'status' => 'completed',
                'error_message' => __('Remote upload failed').': '.$e->getMessage(),
            ]);

            return false;
        }
    }

    public function deleteBackup(int $id): void
    {
        $backup = Backup::find($id);
        if (! $backup) {
            return;
        }

        // Delete local file/folder
        if ($backup->local_path && file_exists($backup->local_path)) {
            if (is_file($backup->local_path)) {
                unlink($backup->local_path);
            } else {
                exec('rm -rf '.escapeshellarg($backup->local_path));
            }
        }

        // Delete from remote destination if exists
        if ($backup->remote_path && $backup->destination) {
            try {
                $config = array_merge(
                    $backup->destination->config ?? [],
                    ['type' => $backup->destination->type]
                );
                $this->getAgent()->send('backup.delete_remote', [
                    'remote_path' => $backup->remote_path,
                    'destination' => $config,
                ]);
            } catch (Exception $e) {
                // Log but continue - we still want to delete the DB record
                logger()->warning('Failed to delete remote backup: '.$e->getMessage());
            }
        }

        $backup->delete();
        Notification::make()->title(__('Backup deleted'))->success()->send();
        $this->resetTable();
    }

    public function addScheduleAction(): Action
    {
        return Action::make('addSchedule')
            ->label(__('Add Schedule'))
            ->icon('heroicon-o-clock')
            ->color('primary')
            ->form([
                TextInput::make('name')
                    ->label(__('Schedule Name'))
                    ->required(),
                Select::make('destination_id')
                    ->label(__('Destination'))
                    ->options(fn () => BackupDestination::where('is_server_backup', true)
                        ->where('is_active', true)
                        ->pluck('name', 'id')
                        ->prepend(__('Local Storage'), ''))
                    ->default('')
                    ->live()
                    ->afterStateUpdated(fn ($set, $state) => $set('backup_type', $this->supportsIncremental($state) ? 'incremental' : 'full')),
                Radio::make('backup_type')
                    ->label(__('Backup Type'))
                    ->options(fn ($get) => $this->supportsIncremental($get('destination_id'))
                        ? [
                            'incremental' => __('Incremental (rsync)'),
                            'full' => __('Full (tar.gz)'),
                        ]
                        : [
                            'full' => __('Full (tar.gz)'),
                        ])
                    ->default('full')
                    ->required(),
                Select::make('frequency')
                    ->label(__('Frequency'))
                    ->options([
                        'hourly' => __('Hourly'),
                        'daily' => __('Daily'),
                        'weekly' => __('Weekly'),
                        'monthly' => __('Monthly'),
                    ])
                    ->required()
                    ->live(),
                TextInput::make('time')
                    ->label(__('Time (HH:MM)'))
                    ->default('02:00')
                    ->visible(fn ($get) => in_array($get('frequency'), ['daily', 'weekly', 'monthly'])),
                Select::make('day_of_week')
                    ->label(__('Day of Week'))
                    ->options([
                        0 => __('Sunday'), 1 => __('Monday'), 2 => __('Tuesday'),
                        3 => __('Wednesday'), 4 => __('Thursday'), 5 => __('Friday'), 6 => __('Saturday'),
                    ])
                    ->visible(fn ($get) => $get('frequency') === 'weekly'),
                Select::make('day_of_month')
                    ->label(__('Day of Month'))
                    ->options(array_combine(range(1, 28), range(1, 28)))
                    ->visible(fn ($get) => $get('frequency') === 'monthly'),
                TextInput::make('retention_count')
                    ->label(__('Keep Last N Backups'))
                    ->numeric()
                    ->default(7),
                Section::make(__('Include'))
                    ->schema([
                        Grid::make(2)->schema([
                            Toggle::make('include_files')->label(__('Website Files'))->default(true),
                            Toggle::make('include_databases')->label(__('Databases'))->default(true),
                            Toggle::make('include_mailboxes')->label(__('Mailboxes'))->default(true),
                            Toggle::make('include_dns')->label(__('DNS Records'))->default(true),
                        ]),
                    ]),
            ])
            ->action(function (array $data) {
                $schedule = BackupSchedule::create([
                    'name' => $data['name'],
                    'is_server_backup' => true,
                    'is_active' => true,
                    'frequency' => $data['frequency'],
                    'time' => $data['time'] ?? '02:00',
                    'day_of_week' => $data['day_of_week'] ?? null,
                    'day_of_month' => $data['day_of_month'] ?? null,
                    'destination_id' => ! empty($data['destination_id']) ? $data['destination_id'] : null,
                    'retention_count' => $data['retention_count'] ?? 7,
                    'include_files' => $data['include_files'] ?? true,
                    'include_databases' => $data['include_databases'] ?? true,
                    'include_mailboxes' => $data['include_mailboxes'] ?? true,
                    'include_dns' => $data['include_dns'] ?? true,
                    'metadata' => ['backup_type' => $data['backup_type'] ?? 'full'],
                ]);

                $schedule->calculateNextRun();
                $schedule->save();

                Notification::make()->title(__('Schedule created'))->success()->send();
                $this->resetTable();
            });
    }

    public function toggleSchedule(int $id): void
    {
        $schedule = BackupSchedule::find($id);
        if (! $schedule) {
            return;
        }

        $schedule->update(['is_active' => ! $schedule->is_active]);

        if ($schedule->is_active) {
            $schedule->calculateNextRun();
            $schedule->save();
        }

        Notification::make()->title($schedule->is_active ? __('Schedule enabled') : __('Schedule disabled'))->success()->send();
        $this->resetTable();
    }

    public function deleteSchedule(int $id): void
    {
        BackupSchedule::where('id', $id)->delete();
        Notification::make()->title(__('Schedule deleted'))->success()->send();
        $this->resetTable();
    }

    public function editScheduleAction(): Action
    {
        return Action::make('editSchedule')
            ->label(__('Edit Schedule'))
            ->icon('heroicon-o-pencil')
            ->color('gray')
            ->fillForm(function (array $arguments): array {
                $schedule = BackupSchedule::find($arguments['id']);
                if (! $schedule) {
                    return [];
                }

                return [
                    'name' => $schedule->name,
                    'backup_type' => $schedule->metadata['backup_type'] ?? 'full',
                    'frequency' => $schedule->frequency,
                    'time' => $schedule->time,
                    'day_of_week' => $schedule->day_of_week,
                    'day_of_month' => $schedule->day_of_month,
                    'destination_id' => $schedule->destination_id ?? '',
                    'retention_count' => $schedule->retention_count,
                    'include_files' => $schedule->include_files,
                    'include_databases' => $schedule->include_databases,
                    'include_mailboxes' => $schedule->include_mailboxes,
                    'include_dns' => $schedule->include_dns,
                ];
            })
            ->form([
                TextInput::make('name')->label(__('Schedule Name'))->required(),
                Select::make('destination_id')
                    ->label(__('Destination'))
                    ->options(fn () => BackupDestination::where('is_server_backup', true)
                        ->where('is_active', true)
                        ->pluck('name', 'id')
                        ->prepend(__('Local Storage'), ''))
                    ->default('')
                    ->live(),
                Radio::make('backup_type')
                    ->label(__('Backup Type'))
                    ->options(fn ($get) => $this->supportsIncremental($get('destination_id'))
                        ? ['incremental' => __('Incremental'), 'full' => __('Full')]
                        : ['full' => __('Full')])
                    ->required(),
                Select::make('frequency')
                    ->label(__('Frequency'))
                    ->options(['hourly' => __('Hourly'), 'daily' => __('Daily'), 'weekly' => __('Weekly'), 'monthly' => __('Monthly')])
                    ->required()
                    ->live(),
                TextInput::make('time')->label(__('Time (HH:MM)'))->visible(fn ($get) => in_array($get('frequency'), ['daily', 'weekly', 'monthly'])),
                Select::make('day_of_week')
                    ->label(__('Day of Week'))
                    ->options([0 => __('Sunday'), 1 => __('Monday'), 2 => __('Tuesday'), 3 => __('Wednesday'), 4 => __('Thursday'), 5 => __('Friday'), 6 => __('Saturday')])
                    ->visible(fn ($get) => $get('frequency') === 'weekly'),
                Select::make('day_of_month')
                    ->label(__('Day of Month'))
                    ->options(array_combine(range(1, 28), range(1, 28)))
                    ->visible(fn ($get) => $get('frequency') === 'monthly'),
                TextInput::make('retention_count')->label(__('Keep Last N Backups'))->numeric(),
                Section::make(__('Include'))
                    ->schema([
                        Grid::make(2)->schema([
                            Toggle::make('include_files')->label(__('Website Files')),
                            Toggle::make('include_databases')->label(__('Databases')),
                            Toggle::make('include_mailboxes')->label(__('Mailboxes')),
                            Toggle::make('include_dns')->label(__('DNS Records')),
                        ]),
                    ]),
            ])
            ->action(function (array $data, array $arguments) {
                $schedule = BackupSchedule::find($arguments['id']);
                if (! $schedule) {
                    return;
                }

                $schedule->update([
                    'name' => $data['name'],
                    'frequency' => $data['frequency'],
                    'time' => $data['time'] ?? '02:00',
                    'day_of_week' => $data['day_of_week'] ?? null,
                    'day_of_month' => $data['day_of_month'] ?? null,
                    'destination_id' => ! empty($data['destination_id']) ? $data['destination_id'] : null,
                    'retention_count' => $data['retention_count'] ?? 7,
                    'include_files' => $data['include_files'] ?? true,
                    'include_databases' => $data['include_databases'] ?? true,
                    'include_mailboxes' => $data['include_mailboxes'] ?? true,
                    'include_dns' => $data['include_dns'] ?? true,
                    'metadata' => array_merge($schedule->metadata ?? [], ['backup_type' => $data['backup_type'] ?? 'full']),
                ]);

                $schedule->calculateNextRun();
                $schedule->save();

                Notification::make()->title(__('Schedule updated'))->success()->send();
                $this->resetTable();
            });
    }

    public function runScheduleNow(int $id): void
    {
        $schedule = BackupSchedule::find($id);
        if (! $schedule) {
            return;
        }

        $runningBackup = Backup::where('schedule_id', $id)->running()->first();
        if ($runningBackup) {
            Notification::make()->title(__('Backup already running'))->warning()->send();

            return;
        }

        $this->createServerBackup([
            'name' => $schedule->name.' - '.__('Manual Run').' '.now()->format('Y-m-d H:i'),
            'backup_type' => $schedule->metadata['backup_type'] ?? 'full',
            'destination_id' => $schedule->destination_id,
            'schedule_id' => $schedule->id,
            'include_files' => $schedule->include_files,
            'include_databases' => $schedule->include_databases,
            'include_mailboxes' => $schedule->include_mailboxes,
            'include_dns' => $schedule->include_dns,
            'users' => $schedule->users,
        ]);
    }

    public function stopScheduleBackup(int $id): void
    {
        $backup = Backup::where('schedule_id', $id)->running()->first();
        if ($backup) {
            $backup->update([
                'status' => 'failed',
                'error_message' => __('Cancelled by user'),
                'completed_at' => now(),
            ]);
            Notification::make()->title(__('Backup cancelled'))->success()->send();
            $this->resetTable();
        }
    }

    public function createUserBackupAction(): Action
    {
        return Action::make('createUserBackup')
            ->label(__('Backup User'))
            ->icon('heroicon-o-user')
            ->color('gray')
            ->form([
                Select::make('user_id')
                    ->label(__('User'))
                    ->options(fn () => User::where('is_admin', false)
                        ->where('is_active', true)
                        ->pluck('username', 'id'))
                    ->required()
                    ->searchable(),
                Section::make(__('Include'))
                    ->schema([
                        Grid::make(2)->schema([
                            Toggle::make('include_files')->label(__('Website Files'))->default(true),
                            Toggle::make('include_databases')->label(__('Databases'))->default(true),
                            Toggle::make('include_mailboxes')->label(__('Mailboxes'))->default(true),
                            Toggle::make('include_dns')->label(__('DNS Records'))->default(true),
                        ]),
                    ]),
            ])
            ->action(function (array $data) {
                $user = User::find($data['user_id']);
                if (! $user) {
                    Notification::make()->title(__('User not found'))->danger()->send();

                    return;
                }

                $timestamp = now()->format('Y-m-d_His');
                $filename = "backup_{$timestamp}.tar.gz";
                $outputPath = "/home/{$user->username}/backups/{$filename}";

                $backup = Backup::create([
                    'user_id' => $user->id,
                    'name' => "{$user->username} ".__('Backup').' '.now()->format('Y-m-d H:i'),
                    'filename' => $filename,
                    'type' => 'full',
                    'include_files' => $data['include_files'] ?? true,
                    'include_databases' => $data['include_databases'] ?? true,
                    'include_mailboxes' => $data['include_mailboxes'] ?? true,
                    'include_dns' => $data['include_dns'] ?? true,
                    'status' => 'pending',
                    'local_path' => $outputPath,
                    'metadata' => ['backup_type' => 'full'],
                ]);

                try {
                    $backup->update(['status' => 'running', 'started_at' => now()]);

                    $result = $this->getAgent()->backupCreate($user->username, $outputPath, [
                        'backup_type' => 'full',
                        'include_files' => $data['include_files'] ?? true,
                        'include_databases' => $data['include_databases'] ?? true,
                        'include_mailboxes' => $data['include_mailboxes'] ?? true,
                        'include_dns' => $data['include_dns'] ?? true,
                    ]);

                    if ($result['success']) {
                        $backup->update([
                            'status' => 'completed',
                            'completed_at' => now(),
                            'size_bytes' => $result['size'] ?? 0,
                            'checksum' => $result['checksum'] ?? null,
                            'domains' => $result['domains'] ?? null,
                            'databases' => $result['databases'] ?? null,
                            'mailboxes' => $result['mailboxes'] ?? null,
                        ]);
                        Notification::make()->title(__('Backup created for :username', ['username' => $user->username]))->success()->send();
                    } else {
                        throw new Exception($result['error'] ?? __('Backup failed'));
                    }
                } catch (Exception $e) {
                    $backup->update([
                        'status' => 'failed',
                        'completed_at' => now(),
                        'error_message' => $e->getMessage(),
                    ]);
                    Notification::make()->title(__('Backup failed'))->body($e->getMessage())->danger()->send();
                }

                $this->resetTable();
            });
    }

    protected function executeRestore(Backup $backup, array $data): void
    {
        // Use username from form (allows selecting user for server backups)
        $username = $data['restore_username'] ?? '';

        if ($username === '__all__') {
            $manifest = $this->getBackupManifest($backup);
            $usernames = $manifest['users'] ?? $backup->users ?? [];
            if (empty($usernames)) {
                $usernames = array_filter([$manifest['username'] ?? '']);
            }

            if (empty($usernames)) {
                Notification::make()->title(__('Cannot determine users for this backup'))->danger()->send();

                return;
            }

            foreach ($usernames as $restoreUser) {
                $data['restore_username'] = $restoreUser;
                $this->executeRestore($backup, $data);
            }

            return;
        }

        if (empty($username)) {
            $manifest = $this->getBackupManifest($backup);
            $username = $manifest['username'] ?? ($backup->users[0] ?? '');
        }

        if (empty($username)) {
            Notification::make()->title(__('Cannot determine user for this backup'))->danger()->send();

            return;
        }

        // Prepare backup path
        $backupPath = $backup->local_path;
        $tempDownloadPath = null;

        // For remote backups, download first
        if ((! $backupPath || ! file_exists($backupPath)) && $backup->remote_path && $backup->destination) {
            $destination = $backup->destination;

            if (! $destination) {
                Notification::make()->title(__('Backup destination not found'))->danger()->send();

                return;
            }

            // Create temp directory for download
            $tempDownloadPath = sys_get_temp_dir().'/jabali_restore_download_'.uniqid();
            mkdir($tempDownloadPath, 0755, true);

            // For incremental backups, we need to download the specific user's directory
            $remotePath = $backup->remote_path;

            // If it's a server backup with per-user directories, construct the user-specific path
            if (str_contains($remotePath, '/') && ! str_ends_with($remotePath, '.tar.gz')) {
                // Incremental backup - download the user's directory
                $userRemotePath = rtrim($remotePath, '/').'/'.$username;
            } else {
                $userRemotePath = $remotePath;
            }

            Notification::make()
                ->title(__('Downloading backup'))
                ->body(__('Downloading from remote destination...'))
                ->info()
                ->send();

            try {
                $downloadResult = $this->getAgent()->send('backup.download_remote', [
                    'remote_path' => $userRemotePath,
                    'local_path' => $tempDownloadPath,
                    'destination' => array_merge(
                        $destination->config ?? [],
                        ['type' => $destination->type]
                    ),
                ]);

                if (! ($downloadResult['success'] ?? false)) {
                    throw new Exception($downloadResult['error'] ?? __('Failed to download backup'));
                }

                $backupPath = $tempDownloadPath;
            } catch (Exception $e) {
                // Cleanup temp directory on failure
                if (is_dir($tempDownloadPath)) {
                    exec('rm -rf '.escapeshellarg($tempDownloadPath));
                }
                Notification::make()
                    ->title(__('Download failed'))
                    ->body($e->getMessage())
                    ->danger()
                    ->send();

                return;
            }
        }

        if (! $backupPath || ! file_exists($backupPath)) {
            Notification::make()->title(__('Backup file not found'))->danger()->send();

            return;
        }

        try {
            $result = $this->getAgent()->send('backup.restore', [
                'username' => $username,
                'backup_path' => $backupPath,
                'restore_files' => $data['restore_files'] ?? false,
                'restore_databases' => $data['restore_databases'] ?? false,
                'restore_mailboxes' => $data['restore_mailboxes'] ?? false,
                'restore_dns' => $data['restore_dns'] ?? false,
                'restore_ssl' => $data['restore_ssl'] ?? false,
                'selected_domains' => ! empty($data['selected_domains']) ? $data['selected_domains'] : null,
                'selected_databases' => ! empty($data['selected_databases']) ? $data['selected_databases'] : null,
                'selected_mailboxes' => ! empty($data['selected_mailboxes']) ? $data['selected_mailboxes'] : null,
            ]);

            // Cleanup temp download if used
            if ($tempDownloadPath && is_dir($tempDownloadPath)) {
                exec('rm -rf '.escapeshellarg($tempDownloadPath));
            }

            if ($result['success'] ?? false) {
                $restored = $result['restored'] ?? [];
                $summary = [];

                if (! empty($restored['files'])) {
                    $summary[] = count($restored['files']).' '.__('domain(s)');
                }
                if (! empty($restored['databases'])) {
                    $summary[] = count($restored['databases']).' '.__('database(s)');
                }
                if (! empty($restored['mailboxes'])) {
                    $summary[] = count($restored['mailboxes']).' '.__('mailbox(es)');
                }
                if (! empty($restored['ssl_certificates'])) {
                    $summary[] = count($restored['ssl_certificates']).' '.__('SSL cert(s)');
                }
                if (! empty($restored['dns_zones'])) {
                    $summary[] = count($restored['dns_zones']).' '.__('DNS zone(s)');
                }
                if ($restored['mysql_users'] ?? false) {
                    $summary[] = __('MySQL users');
                }

                Notification::make()
                    ->title(__('Restore completed'))
                    ->body(! empty($summary) ? __('Restored: :items', ['items' => implode(', ', $summary)]) : __('Nothing was restored'))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['error'] ?? __('Restore failed'));
            }
        } catch (Exception $e) {
            // Cleanup temp download on failure
            if ($tempDownloadPath && is_dir($tempDownloadPath)) {
                exec('rm -rf '.escapeshellarg($tempDownloadPath));
            }
            Notification::make()
                ->title(__('Restore failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    protected function getBackupManifest(Backup $backup, ?string $forUser = null): array
    {
        $backupPath = $backup->local_path;

        if (! $backupPath || ! file_exists($backupPath)) {
            // For remote backups, try to get info from stored metadata
            return [
                'username' => $forUser ?? ($backup->users[0] ?? ''),
                'domains' => $backup->domains ?? [],
                'databases' => $backup->databases ?? [],
                'mailboxes' => $backup->mailboxes ?? [],
                'mysql_users' => $backup->metadata['mysql_users'] ?? [],
                'ssl_certificates' => $backup->ssl_certificates ?? [],
                'dns_zones' => $backup->dns_zones ?? [],
                'users' => $backup->users ?? [],
            ];
        }

        try {
            $result = $this->getAgent()->send('backup.get_info', [
                'backup_path' => $backupPath,
            ]);

            if ($result['success'] ?? false) {
                $manifest = $result['manifest'] ?? [];

                // Handle server backup manifest format (has nested 'users' object)
                if (isset($manifest['users']) && is_array($manifest['users']) && $manifest['type'] === 'server') {
                    $userList = array_keys($manifest['users']);

                    // If no specific user requested, return aggregated data
                    if ($forUser === null) {
                        return [
                            'username' => $userList[0] ?? '',
                            'users' => $userList,
                            'type' => 'server',
                            'domains' => $this->aggregateFromUsers($manifest['users'], 'domains'),
                            'databases' => $this->aggregateFromUsers($manifest['users'], 'databases'),
                            'mailboxes' => $this->aggregateFromUsers($manifest['users'], 'mailboxes'),
                            'mysql_users' => [],
                            'ssl_certificates' => [],
                            'dns_zones' => [],
                        ];
                    }

                    // Return specific user's data
                    if (isset($manifest['users'][$forUser])) {
                        $userData = $manifest['users'][$forUser];

                        return [
                            'username' => $forUser,
                            'users' => $userList,
                            'type' => 'server',
                            'domains' => $userData['domains'] ?? [],
                            'databases' => $userData['databases'] ?? [],
                            'mailboxes' => $userData['mailboxes'] ?? [],
                            'mysql_users' => [],
                            'ssl_certificates' => [],
                            'dns_zones' => [],
                        ];
                    }
                }

                return $manifest;
            }
        } catch (Exception $e) {
            // Fall back to stored data
        }

        return [
            'username' => $forUser ?? ($backup->users[0] ?? ''),
            'domains' => $backup->domains ?? [],
            'databases' => $backup->databases ?? [],
            'mailboxes' => $backup->mailboxes ?? [],
            'mysql_users' => [],
            'ssl_certificates' => [],
            'dns_zones' => [],
            'users' => $backup->users ?? [],
        ];
    }

    protected function aggregateFromUsers(array $users, string $key): array
    {
        $result = [];
        foreach ($users as $userData) {
            if (isset($userData[$key]) && is_array($userData[$key])) {
                $result = array_merge($result, $userData[$key]);
            }
        }

        return array_unique($result);
    }

    /**
     * Get the system timezone for display purposes.
     * Laravel uses UTC internally but we display times in server's local timezone.
     */
    protected function getSystemTimezone(): string
    {
        static $timezone = null;
        if ($timezone === null) {
            $timezone = trim(shell_exec('cat /etc/timezone 2>/dev/null') ?? '');
            if ($timezone === '') {
                $timezone = trim(shell_exec('timedatectl show -p Timezone --value 2>/dev/null') ?? '');
            }
            if ($timezone === '') {
                $timezone = 'UTC';
            }
        }

        return $timezone;
    }
}
