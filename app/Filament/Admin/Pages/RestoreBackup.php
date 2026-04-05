<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\FileBrowser\Support\PathSanitizer;
use App\Models\Backup;
use App\Models\BackupDestination;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Backup\BackupOrchestrator;
use App\Services\FileBrowser\BackupSnapshotAdapter;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Placeholder;
use Filament\Forms\Components\Select;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\View;
use Filament\Schemas\Components\Wizard;
use Filament\Schemas\Components\Wizard\Step;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\HtmlString;

class RestoreBackup extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static ?string $slug = 'backups/restore/{backupId}';

    protected static bool $shouldRegisterNavigation = false;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedArrowPath;

    protected string $view = 'filament.admin.pages.restore-backup';

    // ── Wizard State ────────────────────────────────────────────────────

    public ?int $backupId = null;

    public ?string $selectedUser = null;

    public array $contents = [];

    public array $selectedDatabases = [];

    public array $selectedDbUsers = [];

    public array $selectedMailboxes = [];

    public string $activeSection = 'files';

    public string $currentPath = '';

    public array $selectedPaths = [];

    public array $directoryItems = [];

    public string $conflictMode = 'overwrite';

    public string $restoreMode = 'full';

    public bool $restoreInProgress = false;

    // ── Mount ───────────────────────────────────────────────────────────

    public function mount(?int $backupId = null): void
    {
        $this->backupId = $backupId ?? (int) request()->route('backupId');
        $backup = $this->getBackup();

        if (! $backup || ! $backup->snapshot_id) {
            $this->redirect('/jabali-admin/backups');

            return;
        }

        // Auto-select user for per-account backups
        if ($backup->user_id && $backup->user) {
            $this->selectedUser = $backup->user->username;
        }
    }

    public function getTitle(): string|Htmlable
    {
        return __('Restore Wizard');
    }

    // ── Header Actions ──────────────────────────────────────────────────

    protected function getHeaderActions(): array
    {
        return [
            Action::make('back')
                ->label(__('Back to Backups'))
                ->icon('heroicon-o-arrow-left')
                ->color('gray')
                ->url('/jabali-admin/backups'),
        ];
    }

    // ── Schema ─────────────────────────────────────────────────────────

    protected function getForms(): array
    {
        return ['restoreForm'];
    }

    public function restoreForm(Schema $schema): Schema
    {
        return $schema->schema([
            Wizard::make([
                Step::make(__('Account'))
                    ->icon(Heroicon::OutlinedUser)
                    ->schema([
                        Select::make('selectedUser')
                            ->label(__('Restore for user'))
                            ->options(fn () => User::where('is_active', true)->pluck('username', 'username')->toArray())
                            ->required()
                            ->live()
                            ->searchable()
                            ->disabled(fn () => $this->getBackup()?->user_id !== null),
                        Placeholder::make('backup_info')
                            ->label(__('Backup'))
                            ->content(function (): HtmlString {
                                $backup = $this->getBackup();
                                if (! $backup) {
                                    return new HtmlString('-');
                                }

                                return new HtmlString(
                                    e($backup->name)
                                    .' &middot; '
                                    .e($backup->created_at?->format('M j, Y H:i'))
                                    .($backup->size_bytes > 0 ? ' &middot; '.e(\App\Support\Formatter::bytes($backup->size_bytes)) : '')
                                );
                            }),
                    ])
                    ->afterValidation(function (): void {
                        $this->selectedPaths = [];
                        $this->selectedDatabases = [];
                        $this->selectedDbUsers = [];
                        $this->selectedMailboxes = [];
                        $this->loadContents();
                    }),

                Step::make(__('Restore Mode'))
                    ->icon(Heroicon::OutlinedArrowsPointingOut)
                    ->schema([
                        Select::make('restoreMode')
                            ->label(__('How would you like to restore?'))
                            ->options([
                                'full' => __('Full account restore — restore everything'),
                                'selective' => __('Selective restore — choose specific items'),
                            ])
                            ->default('full')
                            ->required()
                            ->live(),
                        View::make('filament.admin.pages.restore-backup-contents'),
                    ])
                    ->afterValidation(function (): void {
                        if ($this->restoreMode === 'selective') {
                            $this->currentPath = $this->selectedUser ?? '';
                            $this->refreshDirectory();
                        }
                    }),

                Step::make(__('Select Items'))
                    ->icon(Heroicon::OutlinedListBullet)
                    ->visible(fn () => $this->restoreMode === 'selective')
                    ->schema([
                        View::make('filament.admin.pages.restore-backup-browser'),
                    ]),

                Step::make(__('Confirm'))
                    ->icon(Heroicon::OutlinedCheckCircle)
                    ->schema([
                        Placeholder::make('restore_user')
                            ->label(__('User'))
                            ->content(fn () => $this->selectedUser ?? '-'),
                        Placeholder::make('restore_backup')
                            ->label(__('Backup'))
                            ->content(fn () => $this->getBackup()?->name ?? '-'),
                        Placeholder::make('restore_mode_info')
                            ->label(__('Mode'))
                            ->content(fn () => $this->restoreMode === 'full' ? __('Full account restore') : __('Selective restore')),
                        View::make('filament.admin.pages.restore-backup-confirm')
                            ->visible(fn () => $this->restoreMode === 'selective'),
                        Select::make('conflictMode')
                            ->label(__('Conflict resolution'))
                            ->options([
                                'overwrite' => __('Overwrite existing files'),
                                'skip' => __('Skip existing files'),
                            ])
                            ->default('overwrite'),
                    ]),
            ])
                ->submitAction(view('filament.admin.pages.restore-backup-submit')),
        ]);
    }

    // ── Step 2: Load Contents ───────────────────────────────────────────

    public function loadContents(): void
    {
        $backup = $this->getBackup();
        if (! $backup) {
            return;
        }

        try {
            $repo = $backup->destination
                ? $backup->destination->getResticRepoUrl()
                : BackupDestination::defaultRepo();
            $destConfig = $backup->destination
                ? array_merge($backup->destination->config ?? [], ['type' => $backup->destination->type])
                : [];

            $agent = app(AgentClient::class);
            $result = $agent->send('backup.list_contents', [
                'snapshot_id' => $backup->snapshot_id,
                'destination' => $destConfig,
                'repo' => $repo,
            ]);

            $files = $result['files'] ?? [];
            $domains = [];
            $databases = [];
            $mailboxes = [];
            $hasDbUsers = false;

            foreach ($files as $file) {
                $parts = explode('/', $file);

                if (str_contains($file, "home/{$this->selectedUser}/domains/") && count($parts) >= 5) {
                    $domIdx = array_search('domains', $parts);
                    if ($domIdx !== false && isset($parts[$domIdx + 1])) {
                        $domains[$parts[$domIdx + 1]] = true;
                    }
                } elseif (str_contains($file, "home/{$this->selectedUser}/.jabali-backup/databases/") && str_ends_with($file, '.sql.gz')) {
                    $databases[] = basename($file, '.sql.gz');
                } elseif (str_contains($file, "home/{$this->selectedUser}/.jabali-backup/databases/users.sql")) {
                    $hasDbUsers = true;
                } elseif (str_starts_with($file, 'var/mail/vhosts/') && count($parts) >= 5) {
                    $mailboxes["{$parts[4]}@{$parts[3]}"] = true;
                }
            }

            // Extract MySQL user names from users.sql in the snapshot
            $dbUsers = [];
            if ($hasDbUsers) {
                try {
                    $usersPath = "home/{$this->selectedUser}/.jabali-backup/databases/users.sql";
                    $usersResult = $agent->send('backup.read_snapshot_file', [
                        'snapshot_id' => $backup->snapshot_id,
                        'file_path' => $usersPath,
                        'destination' => $destConfig,
                        'repo' => $repo,
                    ]);
                    if ($usersResult['success'] ?? false) {
                        preg_match_all('/CREATE\s+USER\s+[\'"`]([^\'"`]+)[\'"`]@/i', $usersResult['content'] ?? '', $matches);
                        $dbUsers = array_values(array_unique($matches[1] ?? []));
                    }
                } catch (\Throwable) {
                    // Non-critical — just won't show user selection
                }
            }

            $this->contents = [
                'domains' => array_keys($domains),
                'databases' => array_values(array_unique($databases)),
                'mailboxes' => array_keys($mailboxes),
                'db_users' => $dbUsers,
                'has_db_users' => $hasDbUsers,
                'total_files' => count($files),
            ];

            $this->selectedDatabases = $this->contents['databases'];
            $this->selectedDbUsers = $this->contents['db_users'];
            $this->selectedMailboxes = $this->contents['mailboxes'];
        } catch (Exception $e) {
            $this->contents = [];
            Notification::make()->title(__('Failed to load contents'))->body(SafeError::message($e))->danger()->send();
        }
    }

    // ── Step 3: File Browser ────────────────────────────────────────────

    public function selectSectionAndRefresh(string $section): void
    {
        $this->activeSection = $section;
        if ($section === 'files') {
            $this->currentPath = $this->selectedUser ?? '';
            $this->refreshDirectory();
        }
    }

    public function navigateTo(string $path): void
    {
        try {
            $this->currentPath = PathSanitizer::clean($path);
        } catch (\RuntimeException) {
            return;
        }

        $this->refreshDirectory();
    }

    public function refreshDirectory(): void
    {
        $backup = $this->getBackup();
        if (! $backup || ! $this->selectedUser) {
            $this->directoryItems = [];

            return;
        }

        try {
            $adapter = $this->buildAdapter();
            $result = $adapter->files()->list($this->currentPath);
            $items = $result['items'] ?? [];

            // Add parent navigation if not at user root
            if (! empty($this->currentPath) && $this->currentPath !== $this->selectedUser) {
                $parentPath = dirname($this->currentPath);
                if ($parentPath === '.') {
                    $parentPath = '';
                }
                array_unshift($items, [
                    'name' => '..',
                    'path' => $parentPath,
                    'is_dir' => true,
                    'is_parent' => true,
                ]);
            }

            $this->directoryItems = $items;
        } catch (Exception $e) {
            $this->directoryItems = [];
        }
    }

    // ── Step 4: Execute Restore ─────────────────────────────────────────

    public function executeRestore(): void
    {
        if ($this->restoreInProgress) {
            return;
        }

        $user = User::where('username', $this->selectedUser)->first();
        if (! $user) {
            Notification::make()->title(__('User not found'))->danger()->send();

            return;
        }

        $backup = $this->getBackup();
        if (! $backup) {
            Notification::make()->title(__('Backup not found'))->danger()->send();

            return;
        }

        $this->restoreInProgress = true;

        try {
            $orchestrator = app(BackupOrchestrator::class);

            if ($this->restoreMode === 'full') {
                $options = [
                    'restore_files' => true,
                    'restore_databases' => true,
                    'restore_mailboxes' => true,
                    'conflict_mode' => $this->conflictMode,
                ];
            } else {
                $options = [
                    'restore_files' => ! empty($this->selectedPaths),
                    'restore_databases' => ! empty($this->selectedDatabases),
                    'restore_mailboxes' => ! empty($this->selectedMailboxes),
                    'conflict_mode' => $this->conflictMode,
                    'selected_domains' => ! empty($this->selectedPaths)
                        ? array_filter($this->selectedPaths, fn ($p) => ! str_contains($p, '/')) ?: null
                        : null,
                    'selected_databases' => $this->selectedDatabases ?: null,
                    'selected_db_users' => ! empty($this->selectedDatabases) ? ($this->selectedDbUsers ?: []) : [],
                    'selected_mailboxes' => $this->selectedMailboxes ?: null,
                    'selected_files' => ! empty($this->selectedPaths)
                        ? array_filter($this->selectedPaths, fn ($p) => str_contains($p, '/'))
                        : null,
                ];
            }

            $result = $orchestrator->restoreBackup($user, $backup, $options);

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

                $this->redirect('/jabali-admin/backups');
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
        } finally {
            $this->restoreInProgress = false;
        }
    }

    // ── Helpers ──────────────────────────────────────────────────────────

    private function buildAdapter(): BackupSnapshotAdapter
    {
        $backup = $this->getBackup();
        if (! $backup) {
            throw new Exception('Backup not found');
        }

        $repo = $backup->destination
            ? $backup->destination->getResticRepoUrl()
            : BackupDestination::defaultRepo();
        $destConfig = $backup->destination
            ? array_merge($backup->destination->config ?? [], ['type' => $backup->destination->type])
            : [];

        return new BackupSnapshotAdapter(
            app(AgentClient::class),
            $backup->snapshot_id,
            $this->selectedUser ?? 'admin',
            $repo,
            $destConfig,
        );
    }

    private function getBackup(): ?Backup
    {
        return Backup::find($this->backupId);
    }
}
