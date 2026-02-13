<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\Backup;
use App\Models\BackupDestination;
use App\Models\BackupRestore;
use App\Models\Domain;
use App\Models\EmailDomain;
use App\Models\Mailbox;
use App\Models\UserRemoteBackup;
use App\Services\Agent\AgentClient;
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
use Filament\Schemas\Components\Actions as FormActions;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Str;
use Livewire\Attributes\Url;

class Backups extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-archive-box';

    protected static ?string $navigationLabel = null;

    public static function getNavigationLabel(): string
    {
        return __('Backups');
    }

    protected static ?int $navigationSort = 14;

    protected string $view = 'filament.jabali.pages.backups';

    #[Url(as: 'tab')]
    public ?string $activeTab = 'local';

    public ?int $selectedBackupId = null;

    protected ?AgentClient $agent = null;

    public function getTitle(): string|Htmlable
    {
        return __('Backups');
    }

    public function getAgent(): AgentClient
    {
        return $this->agent ??= new AgentClient;
    }

    protected function getUser()
    {
        return Auth::user();
    }

    public function mount(): void
    {
        $this->activeTab = $this->normalizeTabName($this->activeTab);
    }

    protected function normalizeTabName(?string $tab): string
    {
        return match ($tab) {
            'local', 'remote', 'destinations', 'history' => $tab,
            default => 'local',
        };
    }

    public function setTab(string $tab): void
    {
        $this->activeTab = $this->normalizeTabName($tab);
        $this->resetTable();
    }

    protected function getForms(): array
    {
        return ['backupsForm'];
    }

    public function backupsForm(Schema $schema): Schema
    {
        return $schema->schema([
            Section::make(__('Backup Management'))
                ->description(__('Create and manage backups of your account data. Backups include your websites, databases, and mailboxes. You can restore from any backup at any time.'))
                ->icon('heroicon-o-information-circle')
                ->iconColor('info'),
            Tabs::make(__('Backup Sections'))
                ->contained()
                ->livewireProperty('activeTab')
                ->tabs([
                    'local' => Tab::make(__('My Backups'))
                        ->icon('heroicon-o-archive-box')
                        ->schema([
                            View::make('filament.jabali.pages.backups-tab-table'),
                        ]),
                    'remote' => Tab::make(__('Server Backups'))
                        ->icon('heroicon-o-cloud')
                        ->schema([
                            View::make('filament.jabali.pages.backups-tab-table'),
                        ]),
                    'destinations' => Tab::make(__('SFTP Destinations'))
                        ->icon('heroicon-o-server-stack')
                        ->schema([
                            View::make('filament.jabali.pages.backups-tab-table'),
                        ]),
                    'history' => Tab::make(__('Restore History'))
                        ->icon('heroicon-o-arrow-path')
                        ->schema([
                            View::make('filament.jabali.pages.backups-tab-table'),
                        ]),
                ]),
        ]);
    }

    public function table(Table $table): Table
    {
        return match ($this->activeTab) {
            'local' => $this->localBackupsTable($table),
            'remote' => $this->remoteBackupsTable($table),
            'destinations' => $this->destinationsTable($table),
            'history' => $this->restoreHistoryTable($table),
            default => $this->localBackupsTable($table),
        };
    }

    protected function localBackupsTable(Table $table): Table
    {
        return $table
            ->query(Backup::query()->where('user_id', $this->getUser()->id))
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->searchable()
                    ->sortable()
                    ->description(fn (Backup $record) => $record->created_at->format('M j, Y H:i')),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->color(fn (string $state): string => match ($state) {
                        'completed' => 'success',
                        'running' => 'warning',
                        'pending' => 'gray',
                        'failed' => 'danger',
                        default => 'gray',
                    })
                    ->formatStateUsing(fn (string $state): string => ucfirst($state)),
                TextColumn::make('size_human')
                    ->label(__('Size'))
                    ->sortable(query: fn (Builder $query, string $direction) => $query->orderBy('size_bytes', $direction)),
                IconColumn::make('include_files')
                    ->label(__('Files'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-x-circle')
                    ->trueColor('success')
                    ->falseColor('gray'),
                IconColumn::make('include_databases')
                    ->label(__('DB'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-x-circle')
                    ->trueColor('success')
                    ->falseColor('gray'),
                IconColumn::make('include_mailboxes')
                    ->label(__('Mail'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-x-circle')
                    ->trueColor('success')
                    ->falseColor('gray'),
                TextColumn::make('created_at')
                    ->label(__('Created'))
                    ->dateTime('M j, Y H:i')
                    ->sortable(),
            ])
            ->defaultSort('created_at', 'desc')
            ->recordActions([
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->size('sm')
                    ->url(fn (Backup $record) => route('filament.jabali.pages.backup-download', ['path' => base64_encode($record->local_path ?? '')]))
                    ->openUrlInNewTab()
                    ->visible(fn (Backup $record) => $record->canDownload()),
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('warning')
                    ->size('sm')
                    ->modalHeading(__('Restore Backup'))
                    ->modalDescription(__('Select what you want to restore from this backup. Existing data may be overwritten.'))
                    ->modalIcon('heroicon-o-arrow-path')
                    ->modalIconColor('warning')
                    ->modalSubmitActionLabel(__('Start Restore'))
                    ->form([
                        Section::make(__('Restore Options'))
                            ->description(__('Choose which types of data to restore'))
                            ->schema([
                                Grid::make(2)->schema([
                                    Toggle::make('restore_files')
                                        ->label(__('Restore Website Files'))
                                        ->default(true)
                                        ->helperText(__('Restores all website files to their original locations')),
                                    Toggle::make('restore_databases')
                                        ->label(__('Restore Databases'))
                                        ->default(true)
                                        ->helperText(__('Restores MySQL databases (overwrites existing data)')),
                                    Toggle::make('restore_mailboxes')
                                        ->label(__('Restore Mailboxes'))
                                        ->default(true)
                                        ->helperText(__('Restores email accounts and messages')),
                                ]),
                            ]),
                    ])
                    ->action(function (array $data, Backup $record): void {
                        $this->selectedBackupId = $record->id;
                        $this->performRestore($data);
                    })
                    ->visible(fn (Backup $record) => $record->status === 'completed'),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->size('sm')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Backup'))
                    ->modalDescription(__('Are you sure you want to delete this backup? This action cannot be undone.'))
                    ->action(function (Backup $record): void {
                        $user = $this->getUser();
                        if ($record->local_path) {
                            try {
                                $this->getAgent()->backupDelete($user->username, $record->local_path);
                            } catch (Exception) {
                                // Continue anyway
                            }
                        }
                        $record->delete();
                        Notification::make()->title(__('Backup deleted'))->success()->send();
                    }),
            ])
            ->emptyStateHeading(__('No backups yet'))
            ->emptyStateDescription(__('Click "Create Backup" to create your first backup of your account data.'))
            ->emptyStateIcon('heroicon-o-archive-box')
            ->striped();
    }

    protected function remoteBackupsTable(Table $table): Table
    {
        return $table
            ->query(
                UserRemoteBackup::query()
                    ->where('user_id', $this->getUser()->id)
                    ->with('destination')
                    ->orderByDesc('backup_date')
            )
            ->columns([
                TextColumn::make('backup_name')
                    ->label(__('Backup'))
                    ->icon('heroicon-o-cloud')
                    ->iconColor('info')
                    ->description(fn (UserRemoteBackup $record): string => $record->backup_date?->format('M j, Y H:i') ?? '')
                    ->searchable(),
                TextColumn::make('backup_type')
                    ->label(__('Type'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => $state === 'incremental' ? __('Incremental') : __('Full'))
                    ->color(fn (string $state): string => $state === 'incremental' ? 'info' : 'success'),
                TextColumn::make('destination.name')
                    ->label(__('Destination')),
            ])
            ->recordActions([
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('warning')
                    ->size('sm')
                    ->modalHeading(__('Restore from Server Backup'))
                    ->modalDescription(__('This will download and restore the backup. Select what you want to restore.'))
                    ->form([
                        Section::make(__('Restore Options'))
                            ->schema([
                                Grid::make(2)->schema([
                                    Toggle::make('restore_files')->label(__('Website Files'))->default(true),
                                    Toggle::make('restore_databases')->label(__('Databases'))->default(true),
                                    Toggle::make('restore_mailboxes')->label(__('Mailboxes'))->default(true),
                                ]),
                            ]),
                    ])
                    ->action(fn (array $data, UserRemoteBackup $record) => $this->restoreFromRemoteBackup($record, $data)),
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->size('sm')
                    ->action(fn (UserRemoteBackup $record) => $this->downloadFromRemote($record->destination_id, $record->backup_path)),
            ])
            ->headerActions([
                Action::make('refresh')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(function () {
                        // Dispatch re-indexing job
                        \App\Jobs\IndexRemoteBackups::dispatch();
                        Notification::make()
                            ->title(__('Refreshing backup list'))
                            ->body(__('The backup list will be updated in a moment.'))
                            ->info()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No server backups found'))
            ->emptyStateDescription(__('Your backups from scheduled server backups will appear here when available.'))
            ->emptyStateIcon('heroicon-o-cloud')
            ->striped();
    }

    protected function restoreHistoryTable(Table $table): Table
    {
        return $table
            ->query(BackupRestore::query()->where('user_id', $this->getUser()->id)->with('backup'))
            ->columns([
                TextColumn::make('backup.name')
                    ->label(__('Backup'))
                    ->placeholder(__('Unknown'))
                    ->searchable(),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (BackupRestore $record): string => $record->status_label)
                    ->color(fn (BackupRestore $record): string => $record->status_color),
                TextColumn::make('progress')
                    ->label(__('Progress'))
                    ->formatStateUsing(fn (int $state): string => $state.'%')
                    ->color(fn (BackupRestore $record): string => $record->status === 'running' ? 'warning' : 'gray'),
                TextColumn::make('created_at')
                    ->label(__('Date'))
                    ->dateTime('M j, Y H:i')
                    ->sortable(),
                TextColumn::make('duration')
                    ->label(__('Duration'))
                    ->placeholder(__('-')),
            ])
            ->defaultSort('created_at', 'desc')
            ->emptyStateHeading(__('No restore history'))
            ->emptyStateDescription(__('Your backup restore operations will appear here.'))
            ->emptyStateIcon('heroicon-o-arrow-path')
            ->striped();
    }

    protected function destinationsTable(Table $table): Table
    {
        return $table
            ->query(BackupDestination::query()->where('user_id', $this->getUser()->id)->where('is_server_backup', false))
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->weight('medium')
                    ->searchable(),
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => strtoupper($state))
                    ->color('info'),
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
                    ->placeholder(__('Never')),
            ])
            ->recordActions([
                Action::make('test')
                    ->label(__('Test'))
                    ->icon('heroicon-o-check-circle')
                    ->color('success')
                    ->size('sm')
                    ->action(fn (BackupDestination $record) => $this->testUserDestination($record->id)),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->size('sm')
                    ->requiresConfirmation()
                    ->action(fn (BackupDestination $record) => $this->deleteUserDestination($record->id)),
            ])
            ->headerActions([
                $this->addDestinationAction(),
            ])
            ->emptyStateHeading(__('No SFTP destinations configured'))
            ->emptyStateDescription(__('Add an SFTP server to upload your backups to remote storage.'))
            ->emptyStateIcon('heroicon-o-server-stack')
            ->striped();
    }

    public function restoreFromRemoteBackup(UserRemoteBackup $record, array $data): void
    {
        $user = $this->getUser();
        $destination = $record->destination;

        if (! $destination) {
            Notification::make()->title(__('Destination not found'))->danger()->send();

            return;
        }

        // Create temp directory for download
        $tempPath = sys_get_temp_dir().'/jabali_restore_'.uniqid();
        mkdir($tempPath, 0755, true);

        Notification::make()
            ->title(__('Downloading backup'))
            ->body(__('Please wait while the backup is downloaded...'))
            ->info()
            ->send();

        try {
            $config = array_merge($destination->config ?? [], ['type' => $destination->type]);

            $downloadResult = $this->getAgent()->send('backup.download_remote', [
                'remote_path' => $record->backup_path,
                'local_path' => $tempPath,
                'destination' => $config,
            ]);

            if (! ($downloadResult['success'] ?? false)) {
                throw new Exception($downloadResult['error'] ?? __('Download failed'));
            }

            // Now restore from the downloaded backup
            $restoreResult = $this->getAgent()->send('backup.restore', [
                'username' => $user->username,
                'backup_path' => $tempPath,
                'restore_files' => $data['restore_files'] ?? false,
                'restore_databases' => $data['restore_databases'] ?? false,
                'restore_mailboxes' => $data['restore_mailboxes'] ?? false,
            ]);

            // Cleanup temp directory
            exec('rm -rf '.escapeshellarg($tempPath));

            if ($restoreResult['success'] ?? false) {
                $restoredItems = [];

                // Create database records for restored mailboxes
                $restoredMailboxes = $restoreResult['restored']['mailboxes'] ?? [];
                if (! empty($restoredMailboxes)) {
                    foreach ($restoredMailboxes as $mailboxEmail) {
                        $this->createMailboxRecord($user, $mailboxEmail);
                    }
                    $restoredItems[] = count($restoredMailboxes).' '.__('mailbox(es)');
                }

                // Count other restored items
                if (! empty($restoreResult['restored']['files'] ?? [])) {
                    $restoredItems[] = count($restoreResult['restored']['files']).' '.__('domain(s)');
                }
                if (! empty($restoreResult['restored']['databases'] ?? [])) {
                    $restoredItems[] = count($restoreResult['restored']['databases']).' '.__('database(s)');
                }

                $message = ! empty($restoredItems) ? implode(', ', $restoredItems) : __('No items needed restoring');

                Notification::make()
                    ->title(__('Restore completed'))
                    ->body($message)
                    ->success()
                    ->send();
            } else {
                throw new Exception($restoreResult['error'] ?? __('Restore failed'));
            }
        } catch (Exception $e) {
            // Cleanup on error
            if (is_dir($tempPath)) {
                exec('rm -rf '.escapeshellarg($tempPath));
            }
            Notification::make()
                ->title(__('Restore failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    /**
     * Create database record for a restored mailbox.
     * Files are restored by the agent, this creates the DB entry so the mailbox appears in the panel.
     */
    protected function createMailboxRecord($user, string $mailboxEmail): void
    {
        // Parse email
        $parts = explode('@', $mailboxEmail);
        if (count($parts) !== 2) {
            return;
        }

        $localPart = $parts[0];
        $domainName = $parts[1];

        // Check if mailbox already exists
        $existingMailbox = Mailbox::whereHas('emailDomain.domain', function ($query) use ($domainName) {
            $query->where('domain', $domainName);
        })->where('local_part', $localPart)->first();

        if ($existingMailbox) {
            return; // Already exists
        }

        // Find the domain
        $domain = Domain::where('domain', $domainName)
            ->where('user_id', $user->id)
            ->first();

        if (! $domain) {
            return; // Domain not found for this user
        }

        // Find or create email domain
        $emailDomain = EmailDomain::firstOrCreate(
            ['domain_id' => $domain->id],
            [
                'is_active' => true,
                'max_mailboxes' => 10,
                'max_quota_bytes' => 10737418240, // 10GB
            ]
        );

        // Generate a temporary password
        $tempPassword = Str::random(16);

        // Get password hash from agent
        try {
            $result = $this->getAgent()->send('email.hash_password', ['password' => $tempPassword]);
            $passwordHash = $result['password_hash'] ?? '';
        } catch (\Exception $e) {
            // Fallback: generate SHA512-CRYPT hash in PHP
            $passwordHash = '{SHA512-CRYPT}'.crypt($tempPassword, '$6$'.bin2hex(random_bytes(8)).'$');
        }

        // Get system user UID/GID
        $userInfo = posix_getpwnam($user->username);
        $systemUid = $userInfo['uid'] ?? null;
        $systemGid = $userInfo['gid'] ?? null;

        // The maildir path in user's home directory
        $maildirPath = "/home/{$user->username}/mail/{$domainName}/{$localPart}/";

        // Create the mailbox record
        Mailbox::create([
            'email_domain_id' => $emailDomain->id,
            'user_id' => $user->id,
            'local_part' => $localPart,
            'password_hash' => $passwordHash,
            'password_encrypted' => Crypt::encryptString($tempPassword),
            'maildir_path' => $maildirPath,
            'system_uid' => $systemUid,
            'system_gid' => $systemGid,
            'name' => $localPart,
            'quota_bytes' => 1073741824, // 1GB default
            'is_active' => true,
            'imap_enabled' => true,
            'pop3_enabled' => true,
            'smtp_enabled' => true,
        ]);
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('createBackup')
                ->label(__('Create Backup'))
                ->icon('heroicon-o-archive-box-arrow-down')
                ->color('primary')
                ->modalHeading(__('Create Backup'))
                ->modalDescription(__('Create a backup of your account data including websites, databases, and mailboxes.'))
                ->modalIcon('heroicon-o-archive-box-arrow-down')
                ->modalIconColor('primary')
                ->modalSubmitActionLabel(__('Create Backup'))
                ->form([
                    TextInput::make('name')
                        ->label(__('Backup Name'))
                        ->default(fn () => __('Backup').' '.now()->format('Y-m-d H:i'))
                        ->required()
                        ->helperText(__('A descriptive name to identify this backup')),
                    Select::make('destination_id')
                        ->label(__('Save To'))
                        ->options(fn () => BackupDestination::where('user_id', Auth::id())
                            ->where('is_active', true)
                            ->pluck('name', 'id')
                            ->prepend(__('Local (Home Folder)'), ''))
                        ->default('')
                        ->helperText(__('Choose where to store your backup')),
                    Section::make(__('What to Include'))
                        ->description(__('Select the data you want to include in this backup'))
                        ->schema([
                            Grid::make(2)->schema([
                                Toggle::make('include_files')
                                    ->label(__('Website Files'))
                                    ->default(true)
                                    ->helperText(__('All files in your domains folder')),
                                Toggle::make('include_databases')
                                    ->label(__('Databases'))
                                    ->default(true)
                                    ->helperText(__('All MySQL databases and data')),
                                Toggle::make('include_mailboxes')
                                    ->label(__('Mailboxes'))
                                    ->default(true)
                                    ->helperText(__('All email accounts and messages')),
                                Toggle::make('include_ssl')
                                    ->label(__('SSL Certificates'))
                                    ->default(true)
                                    ->helperText(__('SSL certificates for your domains')),
                            ]),
                        ]),
                ])
                ->action(function (array $data) {
                    $this->createBackup($data);
                }),
        ];
    }

    public function createBackup(array $data): void
    {
        $user = $this->getUser();
        $timestamp = now()->format('Y-m-d_His');
        $filename = "backup_{$timestamp}.tar.gz";
        $outputPath = "/home/{$user->username}/backups/{$filename}";
        $destinationId = ! empty($data['destination_id']) ? (int) $data['destination_id'] : null;

        $backup = Backup::create([
            'user_id' => $user->id,
            'name' => $data['name'],
            'filename' => $filename,
            'type' => 'full',
            'include_files' => $data['include_files'] ?? true,
            'include_databases' => $data['include_databases'] ?? true,
            'include_mailboxes' => $data['include_mailboxes'] ?? true,
            'destination_id' => $destinationId,
            'status' => 'pending',
            'local_path' => $outputPath,
        ]);

        try {
            $backup->update(['status' => 'running', 'started_at' => now()]);

            $result = $this->getAgent()->backupCreate($user->username, $outputPath, [
                'include_files' => $data['include_files'] ?? true,
                'include_databases' => $data['include_databases'] ?? true,
                'include_mailboxes' => $data['include_mailboxes'] ?? true,
                'include_ssl' => $data['include_ssl'] ?? true,
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

                // Upload to SFTP if destination selected
                if ($destinationId) {
                    $this->uploadBackupToDestination($backup, $destinationId);
                } else {
                    Notification::make()->title(__('Backup created successfully'))->success()->send();
                }
            } else {
                throw new Exception($result['error'] ?? 'Backup failed');
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
    }

    protected function uploadBackupToDestination(Backup $backup, int $destinationId): void
    {
        $user = $this->getUser();
        $destination = BackupDestination::where('id', $destinationId)
            ->where('user_id', $user->id)
            ->first();

        if (! $destination) {
            Notification::make()
                ->title(__('Backup created locally'))
                ->body(__('Could not upload to destination - not found'))
                ->warning()
                ->send();

            return;
        }

        try {
            $backup->update(['status' => 'uploading']);

            $config = array_merge($destination->config ?? [], ['type' => $destination->type]);

            $result = $this->getAgent()->send('backup.upload_remote', [
                'local_path' => $backup->local_path,
                'destination' => $config,
                'backup_type' => 'full',
            ]);

            if ($result['success'] ?? false) {
                $backup->update([
                    'status' => 'completed',
                    'remote_path' => $result['remote_path'] ?? null,
                ]);
                Notification::make()
                    ->title(__('Backup created and uploaded'))
                    ->body(__('Backup saved to :destination', ['destination' => $destination->name]))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['error'] ?? __('Upload failed'));
            }
        } catch (Exception $e) {
            $backup->update([
                'status' => 'completed',
                'error_message' => __('Local backup created, but upload failed: ').$e->getMessage(),
            ]);
            Notification::make()
                ->title(__('Backup created locally'))
                ->body(__('Upload to :destination failed: :error', [
                    'destination' => $destination->name,
                    'error' => $e->getMessage(),
                ]))
                ->warning()
                ->send();
        }
    }

    public ?int $selectedBackupIdForDelete = null;

    public ?string $selectedPathForDelete = null;

    public function confirmDeleteBackup(int $id): void
    {
        $this->selectedBackupIdForDelete = $id;
        $this->mountAction('deleteBackupAction');
    }

    public function deleteBackupAction(): Action
    {
        return Action::make('deleteBackupAction')
            ->requiresConfirmation()
            ->modalHeading(__('Delete Backup'))
            ->modalDescription(__('Are you sure you want to delete this backup? This action cannot be undone.'))
            ->modalIcon('heroicon-o-trash')
            ->modalIconColor('danger')
            ->modalSubmitActionLabel(__('Delete Backup'))
            ->color('danger')
            ->action(function (): void {
                $user = $this->getUser();
                $backup = Backup::where('id', $this->selectedBackupIdForDelete)->where('user_id', $user->id)->first();

                if (! $backup) {
                    Notification::make()->title(__('Backup not found'))->danger()->send();

                    return;
                }

                // Delete the file
                if ($backup->local_path) {
                    try {
                        $this->getAgent()->backupDelete($user->username, $backup->local_path);
                    } catch (Exception $e) {
                        // Continue anyway
                    }
                }

                $backup->delete();
                Notification::make()->title(__('Backup deleted'))->success()->send();
                $this->resetTable();
            });
    }

    public function confirmDeleteLocalFile(string $path): void
    {
        $this->selectedPathForDelete = $path;
        $this->mountAction('deleteLocalFileAction');
    }

    public function deleteLocalFileAction(): Action
    {
        return Action::make('deleteLocalFileAction')
            ->requiresConfirmation()
            ->modalHeading(__('Delete Backup File'))
            ->modalDescription(__('Are you sure you want to delete this backup file? This action cannot be undone.'))
            ->modalIcon('heroicon-o-trash')
            ->modalIconColor('danger')
            ->modalSubmitActionLabel(__('Delete'))
            ->color('danger')
            ->action(function (): void {
                $user = $this->getUser();

                try {
                    $result = $this->getAgent()->backupDelete($user->username, $this->selectedPathForDelete);
                    if ($result['success']) {
                        Notification::make()->title(__('Backup deleted'))->success()->send();
                    } else {
                        throw new Exception($result['error'] ?? 'Delete failed');
                    }
                } catch (Exception $e) {
                    Notification::make()->title(__('Delete failed'))->body($e->getMessage())->danger()->send();
                }

                $this->resetTable();
            });
    }

    public function downloadFromRemote(int $destinationId, string $remotePath): void
    {
        $user = $this->getUser();
        $destination = BackupDestination::find($destinationId);

        if (! $destination) {
            Notification::make()->title(__('Destination not found'))->danger()->send();

            return;
        }

        // Create a timestamped tar.gz filename
        // remotePath is like "2026-01-20_143000/user", extract the date part
        $pathParts = explode('/', trim($remotePath, '/'));
        $backupDate = $pathParts[0] ?? now()->format('Y-m-d_His');
        $timestamp = now()->format('Y-m-d_His');
        $filename = "backup_{$timestamp}.tar.gz";
        $localPath = "/home/{$user->username}/backups/{$filename}";

        try {
            Notification::make()
                ->title(__('Downloading backup...'))
                ->body(__('This may take a few minutes. Please wait.'))
                ->info()
                ->send();

            $config = array_merge($destination->config ?? [], ['type' => $destination->type]);

            // Use the new agent action that creates a tar.gz archive
            $result = $this->getAgent()->send('backup.download_user_archive', [
                'username' => $user->username,
                'remote_path' => $remotePath,
                'destination' => $config,
                'output_path' => $localPath,
            ]);

            if ($result['success'] ?? false) {
                // Create backup record
                $backup = Backup::create([
                    'user_id' => $user->id,
                    'name' => "Server Backup ({$backupDate})",
                    'filename' => $filename,
                    'type' => 'full',
                    'status' => 'completed',
                    'local_path' => $localPath,
                    'size_bytes' => $result['size'] ?? 0,
                    'completed_at' => now(),
                ]);

                // Format size for display
                $sizeFormatted = $this->formatBytes($result['size'] ?? 0);

                // Create download URL
                $downloadUrl = url('/jabali-panel/backup-download?path='.base64_encode($localPath));

                Notification::make()
                    ->title(__('Backup Ready'))
                    ->body(__('Your backup (:size) can be downloaded from My Backups or from your backups folder.', ['size' => $sizeFormatted]))
                    ->success()
                    ->persistent()
                    ->actions([
                        \Filament\Actions\Action::make('download')
                            ->label(__('Download'))
                            ->url($downloadUrl)
                            ->openUrlInNewTab()
                            ->button(),
                        \Filament\Actions\Action::make('close')
                            ->label(__('Close'))
                            ->close()
                            ->color('gray'),
                    ])
                    ->send();

                $this->setTab('local');
                $this->resetTable();
            } else {
                throw new Exception($result['error'] ?? 'Download failed');
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Download failed'))->body($e->getMessage())->danger()->send();
        }
    }

    public function restoreBackupAction(): Action
    {
        return Action::make('restoreBackup')
            ->label(__('Restore'))
            ->icon('heroicon-o-arrow-path')
            ->color('warning')
            ->requiresConfirmation()
            ->modalHeading(__('Restore Backup'))
            ->modalDescription(__('Select what you want to restore from this backup. Existing data may be overwritten.'))
            ->modalIcon('heroicon-o-arrow-path')
            ->modalIconColor('warning')
            ->modalSubmitActionLabel(__('Start Restore'))
            ->form(function () {
                $backup = $this->selectedBackupId ? Backup::find($this->selectedBackupId) : null;
                $manifest = $backup ? [
                    'domains' => $backup->domains ?? [],
                    'databases' => $backup->databases ?? [],
                    'mailboxes' => $backup->mailboxes ?? [],
                ] : [];

                return [
                    Section::make(__('Restore Options'))
                        ->description(__('Choose which types of data to restore'))
                        ->schema([
                            Grid::make(2)->schema([
                                Toggle::make('restore_files')
                                    ->label(__('Restore Website Files'))
                                    ->default(true)
                                    ->reactive()
                                    ->helperText(__('Restores all website files to their original locations')),
                                Toggle::make('restore_databases')
                                    ->label(__('Restore Databases'))
                                    ->default(true)
                                    ->reactive()
                                    ->helperText(__('Restores MySQL databases (overwrites existing data)')),
                                Toggle::make('restore_mailboxes')
                                    ->label(__('Restore Mailboxes'))
                                    ->default(true)
                                    ->helperText(__('Restores email accounts and messages')),
                            ]),
                        ]),
                    Section::make(__('Select Items'))
                        ->description(__('Leave empty to restore all items'))
                        ->schema([
                            CheckboxList::make('selected_domains')
                                ->label(__('Domains to Restore'))
                                ->options(fn () => array_combine($manifest['domains'] ?? [], $manifest['domains'] ?? []))
                                ->visible(fn ($get) => $get('restore_files') && ! empty($manifest['domains']))
                                ->helperText(__('Select specific domains or leave empty for all')),
                            CheckboxList::make('selected_databases')
                                ->label(__('Databases to Restore'))
                                ->options(fn () => array_combine($manifest['databases'] ?? [], $manifest['databases'] ?? []))
                                ->visible(fn ($get) => $get('restore_databases') && ! empty($manifest['databases']))
                                ->helperText(__('Select specific databases or leave empty for all')),
                            CheckboxList::make('selected_mailboxes')
                                ->label(__('Mailboxes to Restore'))
                                ->options(fn () => array_combine($manifest['mailboxes'] ?? [], $manifest['mailboxes'] ?? []))
                                ->visible(fn ($get) => $get('restore_mailboxes') && ! empty($manifest['mailboxes']))
                                ->helperText(__('Select specific mailboxes or leave empty for all')),
                        ]),
                ];
            })
            ->action(function (array $data) {
                $this->performRestore($data);
            });
    }

    public function startRestore(int $backupId): void
    {
        $this->selectedBackupId = $backupId;
        $this->mountAction('restoreBackupAction');
    }

    public function performRestore(array $data): void
    {
        $user = $this->getUser();
        $backup = Backup::find($this->selectedBackupId);

        if (! $backup || $backup->user_id !== $user->id) {
            Notification::make()->title(__('Backup not found'))->danger()->send();

            return;
        }

        $restore = BackupRestore::create([
            'backup_id' => $backup->id,
            'user_id' => $user->id,
            'restore_files' => $data['restore_files'] ?? true,
            'restore_databases' => $data['restore_databases'] ?? true,
            'restore_mailboxes' => $data['restore_mailboxes'] ?? true,
            'selected_domains' => ! empty($data['selected_domains']) ? $data['selected_domains'] : null,
            'selected_databases' => ! empty($data['selected_databases']) ? $data['selected_databases'] : null,
            'selected_mailboxes' => ! empty($data['selected_mailboxes']) ? $data['selected_mailboxes'] : null,
            'status' => 'pending',
        ]);

        try {
            $restore->markAsRunning();

            $result = $this->getAgent()->backupRestore($user->username, $backup->local_path, [
                'restore_files' => $data['restore_files'] ?? true,
                'restore_databases' => $data['restore_databases'] ?? true,
                'restore_mailboxes' => $data['restore_mailboxes'] ?? true,
                'selected_domains' => ! empty($data['selected_domains']) ? $data['selected_domains'] : null,
                'selected_databases' => ! empty($data['selected_databases']) ? $data['selected_databases'] : null,
                'selected_mailboxes' => ! empty($data['selected_mailboxes']) ? $data['selected_mailboxes'] : null,
            ]);

            if ($result['success']) {
                $restore->markAsCompleted($result['restored'] ?? []);
                Notification::make()
                    ->title(__('Restore completed'))
                    ->body(__('Restored: :domains domains, :databases databases, :mailboxes mailboxes', [
                        'domains' => $result['files_count'],
                        'databases' => $result['databases_count'],
                        'mailboxes' => $result['mailboxes_count'],
                    ]))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['error'] ?? 'Restore failed');
            }
        } catch (Exception $e) {
            $restore->markAsFailed($e->getMessage());
            Notification::make()->title(__('Restore failed'))->body($e->getMessage())->danger()->send();
        }

        $this->resetTable();
    }

    protected function addDestinationAction(): Action
    {
        return Action::make('addDestination')
            ->label(__('Add SFTP'))
            ->icon('heroicon-o-plus')
            ->color('primary')
            ->modalHeading(__('Add SFTP Destination'))
            ->modalDescription(__('Configure an SFTP server to store your backups remotely.'))
            ->form([
                TextInput::make('name')
                    ->label(__('Name'))
                    ->placeholder(__('My Backup Server'))
                    ->required(),
                Grid::make(2)->schema([
                    TextInput::make('host')
                        ->label(__('Host'))
                        ->placeholder(__('backup.example.com'))
                        ->required(),
                    TextInput::make('port')
                        ->label(__('Port'))
                        ->numeric()
                        ->default(22),
                ]),
                TextInput::make('username')
                    ->label(__('Username'))
                    ->required(),
                TextInput::make('password')
                    ->label(__('Password'))
                    ->password()
                    ->helperText(__('Leave empty if using SSH key')),
                Textarea::make('private_key')
                    ->label(__('SSH Private Key'))
                    ->rows(4)
                    ->helperText(__('Paste your private key here (optional)')),
                TextInput::make('path')
                    ->label(__('Remote Path'))
                    ->default('/backups')
                    ->helperText(__('Directory on the server to store backups')),
                FormActions::make([
                    Action::make('testConnection')
                        ->label(__('Test Connection'))
                        ->icon('heroicon-o-signal')
                        ->color('gray')
                        ->action(function ($get, $livewire) {
                            $config = [
                                'type' => 'sftp',
                                'host' => $get('host') ?? '',
                                'port' => (int) ($get('port') ?? 22),
                                'username' => $get('username') ?? '',
                                'password' => $get('password') ?? '',
                                'private_key' => $get('private_key') ?? '',
                                'path' => $get('path') ?? '/backups',
                            ];

                            try {
                                $result = $livewire->getAgent()->backupTestDestination($config);
                                if ($result['success']) {
                                    Notification::make()
                                        ->title(__('Connection successful'))
                                        ->success()
                                        ->send();
                                } else {
                                    Notification::make()
                                        ->title(__('Connection failed'))
                                        ->body($result['error'] ?? __('Could not connect'))
                                        ->danger()
                                        ->send();
                                }
                            } catch (Exception $e) {
                                Notification::make()
                                    ->title(__('Connection failed'))
                                    ->body($e->getMessage())
                                    ->danger()
                                    ->send();
                            }
                        }),
                ])->visible(fn ($get) => ! empty($get('host'))),
            ])
            ->action(function (array $data) {
                $user = $this->getUser();

                // Test connection first
                $config = [
                    'type' => 'sftp',
                    'host' => $data['host'] ?? '',
                    'port' => (int) ($data['port'] ?? 22),
                    'username' => $data['username'] ?? '',
                    'password' => $data['password'] ?? '',
                    'private_key' => $data['private_key'] ?? '',
                    'path' => $data['path'] ?? '/backups',
                ];

                try {
                    $result = $this->getAgent()->backupTestDestination($config);
                    if (! $result['success']) {
                        Notification::make()
                            ->title(__('Connection failed'))
                            ->body($result['error'] ?? __('Could not connect to SFTP server'))
                            ->danger()
                            ->send();

                        return;
                    }
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Connection failed'))
                        ->body($e->getMessage())
                        ->danger()
                        ->send();

                    return;
                }

                BackupDestination::create([
                    'user_id' => $user->id,
                    'name' => $data['name'],
                    'type' => 'sftp',
                    'config' => $config,
                    'is_server_backup' => false,
                    'is_active' => true,
                    'last_tested_at' => now(),
                    'test_status' => 'success',
                ]);

                Notification::make()->title(__('SFTP destination added'))->success()->send();
                $this->resetTable();
            });
    }

    public function testUserDestination(int $id): void
    {
        $user = $this->getUser();
        $destination = BackupDestination::where('id', $id)->where('user_id', $user->id)->first();

        if (! $destination) {
            Notification::make()->title(__('Destination not found'))->danger()->send();

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

    public function deleteUserDestination(int $id): void
    {
        $user = $this->getUser();
        BackupDestination::where('id', $id)->where('user_id', $user->id)->delete();
        Notification::make()->title(__('Destination deleted'))->success()->send();
        $this->resetTable();
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
