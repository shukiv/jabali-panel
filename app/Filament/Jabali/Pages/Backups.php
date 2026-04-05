<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\Backup;
use App\Models\BackupDestination;
use App\Services\Backup\BackupOrchestrator;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
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
use Illuminate\Support\Facades\Auth;
use Livewire\Attributes\Url;

class Backups extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-cloud-arrow-up';

    protected static ?int $navigationSort = 13;

    public static function getNavigationLabel(): string
    {
        return __('Backups');
    }

    protected string $view = 'filament.jabali.pages.backups';

    #[Url(as: 'tab')]
    public ?string $activeTab = 'backups';

    public function getTitle(): string|Htmlable
    {
        return __('Backups');
    }

    protected function getUser(): \App\Models\User
    {
        return Auth::user();
    }

    // ── Header Actions ──────────────────────────────────────────────────

    protected function getHeaderActions(): array
    {
        return [
            $this->createBackupAction(),
            $this->addDestinationAction(),
        ];
    }

    // ── Backup Action ───────────────────────────────────────────────────

    private function createBackupAction(): Action
    {
        $destinations = BackupDestination::where('user_id', $this->getUser()->id)
            ->where('is_active', true)
            ->pluck('name', 'id')
            ->toArray();

        return Action::make('createBackup')
            ->label(__('Create Backup'))
            ->icon('heroicon-o-cloud-arrow-up')
            ->color('primary')
            ->modalHeading(__('Create Backup'))
            ->form([
                TextInput::make('name')
                    ->label(__('Name'))
                    ->default('Backup '.now()->format('Y-m-d H:i'))
                    ->required(),
                Select::make('destination_id')
                    ->label(__('Destination'))
                    ->options(array_merge(['' => __('Local (default)')], $destinations))
                    ->placeholder(__('Select destination'))
                    ->visible(fn () => ! empty($destinations)),
                \Filament\Schemas\Components\Grid::make(3)->schema([
                    \Filament\Forms\Components\Toggle::make('include_files')->label(__('Files'))->default(true),
                    \Filament\Forms\Components\Toggle::make('include_databases')->label(__('Databases'))->default(true),
                    \Filament\Forms\Components\Toggle::make('include_mailboxes')->label(__('Mailboxes'))->default(true),
                ]),
            ])
            ->action(function (array $data): void {
                $user = $this->getUser();

                try {
                    $orchestrator = app(BackupOrchestrator::class);
                    $backup = $orchestrator->createUserBackup($user, $data);

                    if ($backup->status === 'failed') {
                        Notification::make()
                            ->title(__('Backup failed'))
                            ->body($backup->error_message ?? __('Unknown error'))
                            ->danger()
                            ->send();
                    } else {
                        Notification::make()
                            ->title(__('Backup created'))
                            ->body(__('Size: :size', ['size' => $backup->size_human]))
                            ->success()
                            ->send();
                    }
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Backup failed'))
                        ->body(SafeError::message($e))
                        ->danger()
                        ->send();
                }
            });
    }

    // ── Backups Table ─────────────────────────────────────────────────

    public function table(Table $table): Table
    {
        return $table
            ->query(Backup::where('user_id', $this->getUser()->id)->latest())
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->searchable()
                    ->limit(40),
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
                TextColumn::make('created_at')
                    ->label(__('Created'))
                    ->dateTime('M j, Y H:i')
                    ->sortable(),
            ])
            ->actions([
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->color('gray')
                    ->visible(fn (Backup $record) => $record->status === 'completed' && $record->snapshot_id)
                    ->url(fn (Backup $record) => route('filament.jabali.pages.backup-snapshot-download', ['id' => $record->id]))
                    ->openUrlInNewTab(),
                Action::make('browse')
                    ->label(__('Browse'))
                    ->icon('heroicon-o-folder-open')
                    ->color('gray')
                    ->visible(fn (Backup $record) => $record->status === 'completed' && $record->snapshot_id)
                    ->action(function (Backup $record): void {
                        try {
                            $repo = $record->destination
                                ? $record->destination->getResticRepoUrl()
                                : BackupDestination::defaultRepo();
                            $destConfig = $record->destination
                                ? array_merge($record->destination->config ?? [], ['type' => $record->destination->type])
                                : [];

                            $agent = app(\App\Services\Agent\AgentClient::class);
                            $result = $agent->send('backup.list_contents', [
                                'snapshot_id' => $record->snapshot_id,
                                'destination' => $destConfig,
                                'repo' => $repo,
                            ]);

                            $files = $result['files'] ?? [];
                            $domains = [];
                            $databases = [];

                            foreach ($files as $file) {
                                $parts = explode('/', $file);
                                if (str_contains($file, '/domains/') && count($parts) >= 4) {
                                    $domains[$parts[3]] = true;
                                } elseif (str_contains($file, '/databases/') && str_ends_with($file, '.sql.gz')) {
                                    $databases[] = basename($file, '.sql.gz');
                                }
                            }

                            $summary = [];
                            if (! empty($domains)) {
                                $summary[] = count($domains).' domain(s): '.implode(', ', array_keys($domains));
                            }
                            if (! empty($databases)) {
                                $summary[] = count($databases).' database(s): '.implode(', ', $databases);
                            }

                            Notification::make()
                                ->title(__('Snapshot Contents'))
                                ->body(! empty($summary) ? implode("\n", $summary) : __('Empty snapshot'))
                                ->info()
                                ->persistent()
                                ->send();
                        } catch (Exception $e) {
                            Notification::make()
                                ->title(__('Browse failed'))
                                ->body(SafeError::message($e))
                                ->danger()
                                ->send();
                        }
                    }),
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('warning')
                    ->visible(fn (Backup $record) => $record->status === 'completed' && $record->snapshot_id)
                    ->modalHeading(__('Restore Backup'))
                    ->modalDescription(__('This will restore your data from the selected backup. Existing files may be overwritten.'))
                    ->form([
                        \Filament\Schemas\Components\Grid::make(3)->schema([
                            \Filament\Forms\Components\Toggle::make('restore_files')->label(__('Files'))->default(true),
                            \Filament\Forms\Components\Toggle::make('restore_databases')->label(__('Databases'))->default(true),
                            \Filament\Forms\Components\Toggle::make('restore_mailboxes')->label(__('Mailboxes'))->default(true),
                        ]),
                    ])
                    ->action(function (Backup $record, array $data): void {
                        $user = $this->getUser();

                        try {
                            $orchestrator = app(BackupOrchestrator::class);
                            $result = $orchestrator->restoreBackup($user, $record, [
                                'restore_files' => $data['restore_files'] ?? true,
                                'restore_databases' => $data['restore_databases'] ?? true,
                                'restore_mailboxes' => $data['restore_mailboxes'] ?? true,
                            ]);

                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title(__('Restore completed'))
                                    ->body(__(':files domain(s), :dbs database(s), :mail mailbox(es)', [
                                        'files' => $result['result']['files_count'] ?? 0,
                                        'dbs' => $result['result']['databases_count'] ?? 0,
                                        'mail' => $result['result']['mailboxes_count'] ?? 0,
                                    ]))
                                    ->success()
                                    ->send();
                            } else {
                                Notification::make()
                                    ->title(__('Restore failed'))
                                    ->body($result['error'] ?? __('Unknown error'))
                                    ->danger()
                                    ->send();
                            }
                        } catch (Exception $e) {
                            Notification::make()
                                ->title(__('Restore failed'))
                                ->body(SafeError::message($e))
                                ->danger()
                                ->send();
                        }
                    }),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function (Backup $record): void {
                        try {
                            app(BackupOrchestrator::class)->deleteUserBackup($this->getUser(), $record);
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
            ->emptyStateDescription(__('Click "Create Backup" to create your first backup'))
            ->emptyStateIcon('heroicon-o-cloud-arrow-up')
            ->defaultSort('created_at', 'desc');
    }

    // ── Destination Actions ─────────────────────────────────────────────

    private function addDestinationAction(): Action
    {
        return Action::make('addDestination')
            ->label(__('Add SFTP'))
            ->icon('heroicon-o-plus')
            ->form([
                TextInput::make('name')
                    ->label(__('Name'))
                    ->placeholder(__('My Backup Server'))
                    ->required(),
                Grid::make(2)->schema([
                    TextInput::make('host')
                        ->label(__('Host'))
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
                    ->password(),
                Textarea::make('private_key')
                    ->label(__('SSH Private Key'))
                    ->rows(3)
                    ->helperText(__('Optional, alternative to password')),
                TextInput::make('path')
                    ->label(__('Remote Path'))
                    ->default('/backups'),
            ])
            ->action(function (array $data): void {
                $user = $this->getUser();

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
                    $dest = BackupDestination::create([
                        'user_id' => $user->id,
                        'name' => $data['name'],
                        'type' => 'sftp',
                        'config' => $config,
                        'is_server_backup' => false,
                        'is_active' => true,
                    ]);

                    $orchestrator = app(BackupOrchestrator::class);
                    $orchestrator->testDestination($dest);

                    Notification::make()->title(__('Destination added'))->success()->send();
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Failed'))
                        ->body(SafeError::message($e))
                        ->danger()
                        ->send();
                }
            });
    }

    public function testDestination(int $id): void
    {
        $user = $this->getUser();
        $destination = BackupDestination::where('id', $id)->where('user_id', $user->id)->first();

        if (! $destination) {
            return;
        }

        try {
            $orchestrator = app(BackupOrchestrator::class);
            $orchestrator->testDestination($destination);

            $destination->refresh();
            if ($destination->test_status === 'success') {
                Notification::make()->title(__('Connection successful'))->success()->send();
            } else {
                Notification::make()
                    ->title(__('Connection failed'))
                    ->body($destination->test_message ?? __('Unknown error'))
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
}
