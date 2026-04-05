<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Filament\Admin\Pages\Concerns\HasBackupWizard;
use App\Jobs\RunServerBackup;
use App\Models\Backup;
use App\Models\BackupDestination;
use App\Models\BackupSchedule;
use App\Models\User;
use App\Services\Backup\BackupOrchestrator;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\CheckboxList;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Grid;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Livewire\Attributes\Url;

class Backups extends Page implements HasActions, HasForms, HasTable
{
    use HasBackupWizard;
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
    public ?string $activeTab = 'backups';

    public function getTitle(): string|Htmlable
    {
        return __('Server Backups');
    }

    public function mount(): void
    {
        $this->mountBackupWizard();
    }

    public function updatedActiveTab(): void
    {
        $this->resetTable();
    }

    // ── Header Actions ──────────────────────────────────────────────────

    protected function getHeaderActions(): array
    {
        return [
            $this->backupWizardAction(),
            $this->createServerBackupAction(),
            $this->createScheduleAction(),
            $this->backupPasswordAction(),
            $this->addDestinationAction(),
        ];
    }

    // ── Table (switches by active tab) ─────────────────────────────────

    public function table(Table $table): Table
    {
        return match ($this->activeTab) {
            'destinations' => $this->destinationsTable($table),
            'schedules' => $this->schedulesTable($table),
            'logs' => $this->logsTable($table),
            default => $this->backupsTable($table),
        };
    }

    private function backupsTable(Table $table): Table
    {
        return $table
            ->query(Backup::query()->with(['destination', 'user'])->latest())
            ->columns([
                TextColumn::make('user.username')
                    ->label(__('Account'))
                    ->searchable()
                    ->sortable()
                    ->placeholder(__('Server-wide')),
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->searchable()
                    ->limit(30),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->color(fn (string $state) => match ($state) {
                        'completed' => 'success',
                        'running' => 'warning',
                        'pending' => 'gray',
                        'failed' => 'danger',
                        default => 'gray',
                    }),
                TextColumn::make('size_bytes')
                    ->label(__('Size'))
                    ->formatStateUsing(fn ($state) => $state > 0 ? \App\Support\Formatter::bytes($state) : '-'),
                TextColumn::make('destination.name')
                    ->label(__('Destination'))
                    ->placeholder(__('Local')),
                TextColumn::make('created_at')
                    ->label(__('Created'))
                    ->dateTime('M j, Y H:i')
                    ->sortable(),
            ])
            ->filters([
                \Filament\Tables\Filters\SelectFilter::make('user_id')
                    ->label(__('Account'))
                    ->options(fn () => User::where('is_active', true)->pluck('username', 'id')->toArray())
                    ->searchable(),
            ])
            ->actions([
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->visible(fn (Backup $record) => $record->status === 'completed' && $record->snapshot_id)
                    ->url(fn (Backup $record) => route('filament.admin.pages.backup-download', ['id' => $record->id]))
                    ->openUrlInNewTab(),
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('warning')
                    ->visible(fn (Backup $record) => $record->status === 'completed' && $record->snapshot_id)
                    ->url(fn (Backup $record) => "/jabali-admin/backups/restore/{$record->id}"),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalDescription(__('This will permanently delete the backup snapshot. This cannot be undone.'))
                    ->action(function (Backup $record): void {
                        try {
                            app(BackupOrchestrator::class)->deleteBackup($record);
                            Notification::make()->title(__('Backup deleted'))->success()->send();
                        } catch (Exception $e) {
                            Notification::make()
                                ->title(__('Delete failed'))
                                ->body(SafeError::message($e))
                                ->danger()
                                ->send();
                        }
                    }),
            ])
            ->poll('15s')
            ->emptyStateHeading(__('No backups yet'))
            ->emptyStateDescription(__('Click "Create Server Backup" to create your first backup'))
            ->emptyStateIcon('heroicon-o-cloud-arrow-up')
            ->defaultSort('created_at', 'desc');
    }

    private function destinationsTable(Table $table): Table
    {
        return $table
            ->query(BackupDestination::where('is_server_backup', true))
            ->columns([
                TextColumn::make('name')->label(__('Name'))->searchable(),
                TextColumn::make('type')->label(__('Type'))->badge()
                    ->formatStateUsing(fn (string $state) => strtoupper($state))
                    ->color(fn (string $state) => match ($state) {
                        'sftp' => 'info', 's3' => 'warning', default => 'gray',
                    }),
                TextColumn::make('test_status')->label(__('Status'))->badge()
                    ->formatStateUsing(fn (?string $state) => match ($state) {
                        'success' => __('Connected'), 'failed' => __('Failed'), default => __('Not tested'),
                    })
                    ->color(fn (?string $state) => match ($state) {
                        'success' => 'success', 'failed' => 'danger', default => 'gray',
                    }),
                TextColumn::make('last_tested_at')->label(__('Last Tested'))->since()->placeholder(__('Never')),
            ])
            ->actions([
                Action::make('test')->label(__('Test'))->icon('heroicon-o-signal')->color('gray')
                    ->action(fn (BackupDestination $record) => $this->testDestination($record->id)),
                Action::make('edit')->label(__('Edit'))->icon('heroicon-o-pencil')->color('gray')
                    ->form(fn (BackupDestination $record) => $this->destinationFormFields($record->type, $record->config ?? []))
                    ->fillForm(fn (BackupDestination $record) => array_merge(['name' => $record->name], $record->config ?? []))
                    ->action(fn (BackupDestination $record, array $data) => $this->updateDestination($record, $data)),
                Action::make('delete')->label(__('Delete'))->icon('heroicon-o-trash')->color('danger')
                    ->requiresConfirmation()
                    ->action(fn (BackupDestination $record) => $record->delete()),
            ])
            ->emptyStateHeading(__('No destinations configured'))
            ->emptyStateDescription(__('Click "Add Destination" to add SFTP or S3 storage'))
            ->emptyStateIcon('heroicon-o-server-stack');
    }

    private function schedulesTable(Table $table): Table
    {
        return $table
            ->query(BackupSchedule::where('is_server_backup', true))
            ->columns([
                TextColumn::make('name')->label(__('Name'))->searchable(),
                TextColumn::make('frequency')->label(__('Frequency'))->badge()
                    ->formatStateUsing(fn (string $state) => ucfirst($state)),
                TextColumn::make('time')->label(__('Time')),
                TextColumn::make('retention_count')->label(__('Keep'))->suffix(' backups'),
                TextColumn::make('destination.name')->label(__('Destination'))->placeholder(__('Local')),
                TextColumn::make('is_active')->label(__('Active'))->badge()
                    ->formatStateUsing(fn (bool $state) => $state ? __('Active') : __('Paused'))
                    ->color(fn (bool $state) => $state ? 'success' : 'gray'),
                TextColumn::make('last_run_at')->label(__('Last Run'))->since()->placeholder(__('Never')),
            ])
            ->actions([
                Action::make('run')->label(__('Run Now'))->icon('heroicon-o-play')->color('primary')
                    ->action(function (BackupSchedule $record): void {
                        $backup = Backup::create([
                            'name' => $record->name.' - Manual '.now()->format('Y-m-d H:i'),
                            'filename' => 'restic-snapshot',
                            'type' => 'server',
                            'schedule_id' => $record->id,
                            'destination_id' => $record->destination_id,
                            'users' => $record->users,
                            'include_files' => $record->include_files,
                            'include_databases' => $record->include_databases,
                            'include_mailboxes' => $record->include_mailboxes,
                            'include_dns' => $record->include_dns ?? true,
                            'include_ssl' => $record->include_ssl ?? true,
                            'status' => 'pending',
                        ]);

                        RunServerBackup::dispatch($backup->id);

                        Notification::make()
                            ->title(__('Backup started'))
                            ->body(__('Running ":name" schedule now', ['name' => $record->name]))
                            ->success()
                            ->send();
                    }),
                Action::make('edit')->label(__('Edit'))->icon('heroicon-o-pencil')->color('gray')
                    ->form([
                        TextInput::make('name')->label(__('Name'))->required(),
                        Grid::make(2)->schema([
                            Select::make('frequency')->label(__('Frequency'))
                                ->options(['daily' => __('Daily'), 'weekly' => __('Weekly'), 'monthly' => __('Monthly')])
                                ->required(),
                            TextInput::make('time')->label(__('Time (HH:MM)'))->required(),
                        ]),
                        Select::make('destination_id')->label(__('Destination'))
                            ->options(BackupDestination::where('is_active', true)->pluck('name', 'id')->toArray())
                            ->placeholder(__('Local (default)')),
                        TextInput::make('retention_count')->label(__('Keep last N backups'))
                            ->numeric()->minValue(1),
                    ])
                    ->fillForm(fn (BackupSchedule $record) => [
                        'name' => $record->name,
                        'frequency' => $record->frequency,
                        'time' => $record->time,
                        'destination_id' => $record->destination_id,
                        'retention_count' => $record->retention_count,
                    ])
                    ->action(function (BackupSchedule $record, array $data): void {
                        $record->update([
                            'name' => $data['name'],
                            'frequency' => $data['frequency'],
                            'time' => $data['time'],
                            'destination_id' => ! empty($data['destination_id']) ? (int) $data['destination_id'] : null,
                            'retention_count' => (int) ($data['retention_count'] ?? 7),
                        ]);
                        Notification::make()->title(__('Schedule updated'))->success()->send();
                    }),
                Action::make('toggle')->label(fn (BackupSchedule $record) => $record->is_active ? __('Pause') : __('Enable'))
                    ->icon(fn (BackupSchedule $record) => $record->is_active ? 'heroicon-o-pause' : 'heroicon-o-play')
                    ->color('gray')
                    ->action(fn (BackupSchedule $record) => $record->update(['is_active' => ! $record->is_active])),
                Action::make('delete')->label(__('Delete'))->icon('heroicon-o-trash')->color('danger')
                    ->requiresConfirmation()
                    ->action(fn (BackupSchedule $record) => $record->delete()),
            ])
            ->emptyStateHeading(__('No schedules configured'))
            ->emptyStateDescription(__('Click "Add Schedule" to set up automated backups'))
            ->emptyStateIcon('heroicon-o-clock');
    }

    // ── Backup Actions ────────────────────────────────────────────────

    private function createServerBackupAction(): Action
    {
        $destinations = BackupDestination::where('is_server_backup', true)
            ->where('is_active', true)
            ->pluck('name', 'id')
            ->toArray();

        $users = User::where('is_active', true)
            ->pluck('username', 'id')
            ->toArray();

        return Action::make('createServerBackup')
            ->label(__('Create Server Backup'))
            ->icon('heroicon-o-cloud-arrow-up')
            ->color('danger')
            ->modalHeading(__('Create Server Backup'))
            ->form([
                Select::make('destination_id')
                    ->label(__('Destination'))
                    ->options($destinations)
                    ->placeholder(__('Local (default)'))
                    ->placeholder(__('Select destination')),
                CheckboxList::make('selected_users')
                    ->label(__('Users'))
                    ->options($users)
                    ->columns(3)
                    ->helperText(__('Leave empty to backup all users')),
                Grid::make(3)->schema([
                    Toggle::make('include_files')->label(__('Files'))->default(true),
                    Toggle::make('include_databases')->label(__('Databases'))->default(true),
                    Toggle::make('include_mailboxes')->label(__('Mailboxes'))->default(true),
                ]),
                Grid::make(2)->schema([
                    Toggle::make('include_dns')->label(__('DNS Zones'))->default(true),
                    Toggle::make('include_ssl')->label(__('SSL Certificates'))->default(true),
                ]),
            ])
            ->action(function (array $data): void {
                $destinationId = ! empty($data['destination_id']) ? (int) $data['destination_id'] : null;
                $selectedUsers = ! empty($data['selected_users'])
                    ? User::whereIn('id', $data['selected_users'])->pluck('username')->toArray()
                    : null;

                $name = 'Server Backup '.now()->format('Y-m-d H:i');

                $backup = Backup::create([
                    'name' => $name,
                    'filename' => 'restic-snapshot',
                    'type' => 'server',
                    'destination_id' => $destinationId,
                    'users' => $selectedUsers,
                    'include_files' => $data['include_files'] ?? true,
                    'include_databases' => $data['include_databases'] ?? true,
                    'include_mailboxes' => $data['include_mailboxes'] ?? true,
                    'include_dns' => $data['include_dns'] ?? true,
                    'include_ssl' => $data['include_ssl'] ?? true,
                    'status' => 'pending',
                ]);

                RunServerBackup::dispatch($backup->id);

                Notification::make()
                    ->title(__('Backup started'))
                    ->body(__('Server backup is running in the background.'))
                    ->success()
                    ->send();
            });
    }

    // ── Schedule Actions ─────────────────────────────────────────────

    private function createScheduleAction(): Action
    {
        $destinations = BackupDestination::where('is_active', true)
            ->pluck('name', 'id')
            ->toArray();

        return Action::make('createSchedule')
            ->label(__('Add Schedule'))
            ->icon('heroicon-o-clock')
            ->color('gray')
            ->modalHeading(__('Create Backup Schedule'))
            ->form([
                TextInput::make('name')
                    ->label(__('Name'))
                    ->placeholder(__('Daily Backup'))
                    ->required(),
                Grid::make(2)->schema([
                    Select::make('frequency')
                        ->label(__('Frequency'))
                        ->options([
                            'daily' => __('Daily'),
                            'weekly' => __('Weekly'),
                            'monthly' => __('Monthly'),
                        ])
                        ->default('daily')
                        ->required(),
                    TextInput::make('time')
                        ->label(__('Time (HH:MM)'))
                        ->placeholder('03:00')
                        ->default('03:00')
                        ->required(),
                ]),
                Select::make('destination_id')
                    ->label(__('Destination'))
                    ->options($destinations)
                    ->placeholder(__('Local (default)'))
                    ->placeholder(__('Select destination')),
                TextInput::make('retention_count')
                    ->label(__('Keep last N backups'))
                    ->numeric()
                    ->default(7)
                    ->minValue(1)
                    ->maxValue(365),
                Grid::make(3)->schema([
                    Toggle::make('include_files')->label(__('Files'))->default(true),
                    Toggle::make('include_databases')->label(__('Databases'))->default(true),
                    Toggle::make('include_mailboxes')->label(__('Mailboxes'))->default(true),
                ]),
            ])
            ->action(function (array $data): void {
                $timeParts = explode(':', $data['time'] ?? '03:00');
                $hour = (int) ($timeParts[0] ?? 3);
                $minute = (int) ($timeParts[1] ?? 0);

                $schedule = BackupSchedule::create([
                    'name' => $data['name'],
                    'is_active' => true,
                    'is_server_backup' => true,
                    'frequency' => $data['frequency'] ?? 'daily',
                    'time' => sprintf('%02d:%02d', $hour, $minute),
                    'destination_id' => ! empty($data['destination_id']) ? (int) $data['destination_id'] : null,
                    'retention_count' => (int) ($data['retention_count'] ?? 7),
                    'include_files' => $data['include_files'] ?? true,
                    'include_databases' => $data['include_databases'] ?? true,
                    'include_mailboxes' => $data['include_mailboxes'] ?? true,
                    'include_dns' => true,
                    'include_ssl' => true,
                    'next_run_at' => now()->setTime($hour, $minute)->addDay(),
                ]);

                Notification::make()
                    ->title(__('Schedule created'))
                    ->body(__(':name — :freq at :time', [
                        'name' => $schedule->name,
                        'freq' => $schedule->frequency,
                        'time' => $schedule->time,
                    ]))
                    ->success()
                    ->send();
            });
    }

    // ── Password Action ─────────────────────────────────────────────────

    private function backupPasswordAction(): Action
    {
        $currentPassword = '';
        try {
            $result = app(\App\Services\Agent\AgentClient::class)->send('backup.get_password', []);
            $currentPassword = $result['password'] ?? '';
        } catch (\Throwable) {
        }

        return Action::make('backupPassword')
            ->label(__('Password'))
            ->icon('heroicon-o-key')
            ->color('gray')
            ->modalHeading(__('Backup Encryption Password'))
            ->modalDescription(__('This password encrypts all Restic backups. If you reinstall the panel, you must restore this password to access existing backup repositories.'))
            ->modalSubmitActionLabel(__('Save Password'))
            ->form([
                TextInput::make('password')
                    ->label(__('Restic Password'))
                    ->password()
                    ->revealable()
                    ->default($currentPassword)
                    ->required()
                    ->helperText(__('Changing this will make existing remote repositories inaccessible unless they are re-initialized.')),
            ])
            ->action(function (array $data): void {
                $password = trim($data['password'] ?? '');
                if (empty($password)) {
                    Notification::make()->title(__('Password cannot be empty'))->danger()->send();

                    return;
                }

                try {
                    $agent = app(\App\Services\Agent\AgentClient::class);
                    $agent->send('backup.set_password', ['password' => $password]);

                    Notification::make()
                        ->title(__('Password updated'))
                        ->body(__('Backup encryption password has been saved.'))
                        ->success()
                        ->send();
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Failed to update password'))
                        ->body(SafeError::message($e))
                        ->danger()
                        ->send();
                }
            });
    }

    // ── Destination Actions ─────────────────────────────────────────────

    private function addDestinationAction(): Action
    {
        return Action::make('addDestination')
            ->label(__('Add Destination'))
            ->icon('heroicon-o-plus')
            ->form([
                Select::make('type')
                    ->label(__('Type'))
                    ->options([
                        'sftp' => __('SFTP / SSH'),
                        's3' => __('Amazon S3'),
                        'b2' => __('Backblaze B2'),
                        'wasabi' => __('Wasabi'),
                        'minio' => __('MinIO / S3-Compatible'),
                        'gcs' => __('Google Cloud Storage'),
                        'azure' => __('Azure Blob Storage'),
                        'rest' => __('Restic REST Server'),
                        'local' => __('Local Path'),
                    ])
                    ->required()
                    ->live(),
                TextInput::make('name')
                    ->label(__('Name'))
                    ->placeholder(__('My Backup Server'))
                    ->required(),
                // SFTP fields
                Grid::make(2)
                    ->schema([
                        TextInput::make('host')
                            ->label(__('Host'))
                            ->required(),
                        TextInput::make('port')
                            ->label(__('Port'))
                            ->numeric()
                            ->default(22),
                    ])
                    ->visible(fn ($get) => $get('type') === 'sftp'),
                TextInput::make('username')
                    ->label(__('Username'))
                    ->visible(fn ($get) => $get('type') === 'sftp')
                    ->required(fn ($get) => $get('type') === 'sftp'),
                TextInput::make('password')
                    ->label(__('Password'))
                    ->password()
                    ->visible(fn ($get) => $get('type') === 'sftp'),
                Textarea::make('private_key')
                    ->label(__('SSH Private Key'))
                    ->rows(3)
                    ->visible(fn ($get) => $get('type') === 'sftp')
                    ->helperText(__('Paste private key here (optional, alternative to password)')),
                TextInput::make('path')
                    ->label(__('Remote Path'))
                    ->default('/backups')
                    ->visible(fn ($get) => in_array($get('type'), ['sftp', 'local'])),
                // S3-compatible fields (S3, Wasabi, MinIO, B2)
                TextInput::make('endpoint')
                    ->label(__('Endpoint'))
                    ->placeholder(fn ($get) => match ($get('type')) {
                        's3' => 'https://s3.amazonaws.com',
                        'b2' => 'https://s3.us-west-000.backblazeb2.com',
                        'wasabi' => 'https://s3.wasabisys.com',
                        'minio' => 'https://minio.example.com:9000',
                        default => 'https://s3.amazonaws.com',
                    })
                    ->visible(fn ($get) => in_array($get('type'), ['s3', 'b2', 'wasabi', 'minio']))
                    ->required(fn ($get) => in_array($get('type'), ['s3', 'b2', 'wasabi', 'minio'])),
                TextInput::make('bucket')
                    ->label(__('Bucket'))
                    ->visible(fn ($get) => in_array($get('type'), ['s3', 'b2', 'wasabi', 'minio', 'gcs']))
                    ->required(fn ($get) => in_array($get('type'), ['s3', 'b2', 'wasabi', 'minio', 'gcs'])),
                TextInput::make('access_key')
                    ->label(fn ($get) => match ($get('type')) {
                        'b2' => __('Application Key ID'),
                        'gcs' => __('Project ID'),
                        default => __('Access Key'),
                    })
                    ->visible(fn ($get) => in_array($get('type'), ['s3', 'b2', 'wasabi', 'minio', 'gcs']))
                    ->required(fn ($get) => $get('type') === 's3'),
                TextInput::make('secret_key')
                    ->label(fn ($get) => match ($get('type')) {
                        'b2' => __('Application Key'),
                        default => __('Secret Key'),
                    })
                    ->password()
                    ->visible(fn ($get) => in_array($get('type'), ['s3', 'b2', 'wasabi', 'minio', 'gcs']))
                    ->required(fn ($get) => in_array($get('type'), ['s3', 'b2', 'wasabi', 'minio', 'gcs'])),
                // Azure fields
                TextInput::make('azure_account')
                    ->label(__('Storage Account'))
                    ->visible(fn ($get) => $get('type') === 'azure')
                    ->required(fn ($get) => $get('type') === 'azure'),
                TextInput::make('azure_key')
                    ->label(__('Account Key'))
                    ->password()
                    ->visible(fn ($get) => $get('type') === 'azure')
                    ->required(fn ($get) => $get('type') === 'azure'),
                TextInput::make('azure_container')
                    ->label(__('Container'))
                    ->visible(fn ($get) => $get('type') === 'azure')
                    ->required(fn ($get) => $get('type') === 'azure'),
                // REST server
                TextInput::make('rest_url')
                    ->label(__('REST Server URL'))
                    ->placeholder('https://backup.example.com:8000')
                    ->visible(fn ($get) => $get('type') === 'rest')
                    ->required(fn ($get) => $get('type') === 'rest'),
                TextInput::make('rest_username')
                    ->label(__('Username'))
                    ->visible(fn ($get) => $get('type') === 'rest'),
                TextInput::make('rest_password')
                    ->label(__('Password'))
                    ->password()
                    ->visible(fn ($get) => $get('type') === 'rest'),
            ])
            ->action(function (array $data): void {
                $type = $data['type'];
                $config = $this->buildConfig($type, $data);

                // Test connection first
                try {
                    $orchestrator = app(BackupOrchestrator::class);
                    $dest = BackupDestination::create([
                        'name' => $data['name'],
                        'type' => $type,
                        'config' => $config,
                        'is_server_backup' => true,
                        'is_active' => true,
                    ]);

                    $orchestrator->testDestination($dest);

                    if ($dest->test_status === 'failed') {
                        Notification::make()
                            ->title(__('Destination added but connection failed'))
                            ->body($dest->test_message ?? __('Check credentials'))
                            ->warning()
                            ->send();
                    } else {
                        Notification::make()
                            ->title(__('Destination added'))
                            ->success()
                            ->send();
                    }
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Failed to add destination'))
                        ->body(SafeError::message($e))
                        ->danger()
                        ->send();
                }
            });
    }

    public function testDestination(int $id): void
    {
        $destination = BackupDestination::find($id);
        if (! $destination) {
            return;
        }

        try {
            $orchestrator = app(BackupOrchestrator::class);
            $orchestrator->testDestination($destination);

            if ($destination->fresh()->test_status === 'success') {
                Notification::make()->title(__('Connection successful'))->success()->send();
            } else {
                Notification::make()
                    ->title(__('Connection failed'))
                    ->body($destination->fresh()->test_message ?? __('Unknown error'))
                    ->danger()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Connection failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    private function updateDestination(BackupDestination $dest, array $data): void
    {
        $config = $this->buildConfig($dest->type, $data);
        $dest->update([
            'name' => $data['name'] ?? $dest->name,
            'config' => $config,
        ]);

        Notification::make()->title(__('Destination updated'))->success()->send();
    }

    // ── Logs Table ──────────────────────────────────────────────────────

    private function logsTable(Table $table): Table
    {
        return $table
            ->query(
                Backup::query()
                    ->with(['destination', 'user', 'schedule'])
                    ->latest()
            )
            ->columns([
                TextColumn::make('created_at')
                    ->label(__('Date'))
                    ->dateTime('M j, Y H:i:s')
                    ->sortable(),
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->formatStateUsing(fn (?string $state) => match ($state) {
                        'server' => __('Server'), 'user' => __('User'), 'scheduled' => __('Scheduled'), default => ucfirst($state ?? 'manual'),
                    })
                    ->color(fn (?string $state) => match ($state) {
                        'server' => 'info', 'scheduled' => 'purple', default => 'gray',
                    }),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->color(fn (string $state) => match ($state) {
                        'completed' => 'success',
                        'running', 'uploading' => 'warning',
                        'failed' => 'danger',
                        default => 'gray',
                    }),
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->limit(35),
                TextColumn::make('destination.name')
                    ->label(__('Destination'))
                    ->placeholder(__('Local')),
                TextColumn::make('size_bytes')
                    ->label(__('Size'))
                    ->formatStateUsing(fn ($state) => $state > 0 ? \App\Support\Formatter::bytes($state) : '-'),
                TextColumn::make('started_at')
                    ->label(__('Started'))
                    ->dateTime('H:i:s')
                    ->placeholder('-'),
                TextColumn::make('completed_at')
                    ->label(__('Finished'))
                    ->dateTime('H:i:s')
                    ->placeholder('-'),
                TextColumn::make('error_message')
                    ->label(__('Error'))
                    ->limit(50)
                    ->placeholder('-')
                    ->color('danger'),
            ])
            ->actions([
                Action::make('view_details')
                    ->label(__('Details'))
                    ->icon('heroicon-o-document-text')
                    ->color('gray')
                    ->modalHeading(fn (Backup $record) => $record->name)
                    ->modalContent(function (Backup $record): \Illuminate\Contracts\View\View {
                        return view('filament.admin.pages.backup-log-modal', [
                            'backup' => $record,
                            'restores' => $record->restores()->latest()->get(),
                        ]);
                    })
                    ->modalSubmitAction(false),
            ])
            ->emptyStateHeading(__('No backup activity yet'))
            ->emptyStateDescription(__('Backup and restore operations will appear here.'))
            ->emptyStateIcon('heroicon-o-document-text')
            ->defaultSort('created_at', 'desc');
    }

    // ── Helpers ─────────────────────────────────────────────────────────

    private function buildConfig(string $type, array $data): array
    {
        $base = ['type' => $type];

        return match ($type) {
            'sftp' => array_merge($base, [
                'host' => $data['host'] ?? '',
                'port' => (int) ($data['port'] ?? 22),
                'username' => $data['username'] ?? '',
                'password' => $data['password'] ?? '',
                'private_key' => $data['private_key'] ?? '',
                'path' => $data['path'] ?? '/backups',
            ]),
            's3', 'b2', 'wasabi', 'minio' => array_merge($base, [
                'endpoint' => $data['endpoint'] ?? '',
                'bucket' => $data['bucket'] ?? '',
                'access_key' => $data['access_key'] ?? '',
                'secret_key' => $data['secret_key'] ?? '',
            ]),
            'gcs' => array_merge($base, [
                'bucket' => $data['bucket'] ?? '',
                'access_key' => $data['access_key'] ?? '',
                'secret_key' => $data['secret_key'] ?? '',
            ]),
            'azure' => array_merge($base, [
                'account' => $data['azure_account'] ?? '',
                'key' => $data['azure_key'] ?? '',
                'container' => $data['azure_container'] ?? '',
            ]),
            'rest' => array_merge($base, [
                'url' => $data['rest_url'] ?? '',
                'username' => $data['rest_username'] ?? '',
                'password' => $data['rest_password'] ?? '',
            ]),
            default => array_merge($base, [
                'path' => $data['path'] ?? BackupDestination::defaultRepo(),
            ]),
        };
    }

    /**
     * @return array<int, \Filament\Forms\Components\Component>
     */
    private function destinationFormFields(string $type, array $config = []): array
    {
        $fields = [
            TextInput::make('name')->label(__('Name'))->required(),
        ];

        match ($type) {
            'sftp' => array_push($fields,
                Grid::make(2)->schema([
                    TextInput::make('host')->label(__('Host'))->default($config['host'] ?? '')->required(),
                    TextInput::make('port')->label(__('Port'))->numeric()->default($config['port'] ?? 22),
                ]),
                TextInput::make('username')->label(__('Username'))->default($config['username'] ?? '')->required(),
                TextInput::make('password')->label(__('Password'))->password()->revealable()->default($config['password'] ?? ''),
                TextInput::make('path')->label(__('Remote Path'))->default($config['path'] ?? '/backups'),
            ),
            's3', 'b2', 'wasabi', 'minio' => array_push($fields,
                TextInput::make('endpoint')->label(__('Endpoint'))->default($config['endpoint'] ?? '')->required(),
                TextInput::make('bucket')->label(__('Bucket'))->default($config['bucket'] ?? '')->required(),
                TextInput::make('access_key')->label(__('Access Key'))->default($config['access_key'] ?? '')->required(),
                TextInput::make('secret_key')->label(__('Secret Key'))->password()->revealable()->default($config['secret_key'] ?? '')->required(),
            ),
            'gcs' => array_push($fields,
                TextInput::make('bucket')->label(__('Bucket'))->default($config['bucket'] ?? '')->required(),
                TextInput::make('access_key')->label(__('Project ID'))->default($config['access_key'] ?? '')->required(),
                TextInput::make('secret_key')->label(__('Credentials Path'))->default($config['secret_key'] ?? '')->required(),
            ),
            'azure' => array_push($fields,
                TextInput::make('container')->label(__('Container'))->default($config['container'] ?? '')->required(),
                TextInput::make('account')->label(__('Account Name'))->default($config['account'] ?? '')->required(),
                TextInput::make('key')->label(__('Account Key'))->password()->revealable()->default($config['key'] ?? '')->required(),
            ),
            'rest' => array_push($fields,
                TextInput::make('url')->label(__('Server URL'))->default($config['url'] ?? '')->required()
                    ->helperText(__('e.g. https://backup.example.com:8000/')),
                TextInput::make('username')->label(__('Username'))->default($config['username'] ?? ''),
                TextInput::make('password')->label(__('Password'))->password()->revealable()->default($config['password'] ?? ''),
            ),
            default => array_push($fields,
                TextInput::make('path')->label(__('Path'))->default($config['path'] ?? BackupDestination::defaultRepo()),
            ),
        };

        return $fields;
    }
}
