<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Models\ServerImport;
use App\Models\ServerImportAccount;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Checkbox;
use Filament\Forms\Components\FileUpload;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
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
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Storage;
use Livewire\Attributes\Url;

class HestiaCpMigration extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-server-stack';

    protected static ?string $navigationLabel = null;

    protected static bool $shouldRegisterNavigation = false;

    protected static ?string $slug = 'hestiacp-migration';

    protected string $view = 'filament.admin.pages.hestiacp-migration';

    #[Url(as: 'hestiacp-step')]
    public ?string $wizardStep = null;

    public bool $step1Complete = false;

    public ?int $importId = null;

    public ?string $name = null;

    public string $importMethod = 'backup_file';

    public ?string $backupPath = null;

    public ?string $backupFilePath = null;

    public bool $importFiles = true;

    public bool $importDatabases = true;

    public bool $importEmails = true;

    public bool $importSsl = true;

    public static function getNavigationLabel(): string
    {
        return __('HestiaCP Migration');
    }

    public function getTitle(): string|Htmlable
    {
        return __('HestiaCP Migration');
    }

    public function getSubheading(): ?string
    {
        return __('Migrate HestiaCP / VestaCP accounts into Jabali');
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
                ->modalDescription(__('This will reset the HestiaCP migration wizard. Are you sure?'))
                ->action('resetMigration'),
        ];
    }

    public function mount(): void
    {
        $this->restoreFromSession();
        $this->restoreFromImport();
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
                ->persistStepInQueryString('hestiacp-step'),
        ]);
    }

    protected function getConnectStep(): Step
    {
        return Step::make(__('Upload'))
            ->id('connect')
            ->icon('heroicon-o-arrow-up-tray')
            ->description(__('Upload a HestiaCP or VestaCP backup'))
            ->schema([
                Section::make(__('Source'))
                    ->description(__('Upload a HestiaCP / VestaCP backup archive (.tar, .tar.gz, .tgz).'))
                    ->icon('heroicon-o-server')
                    ->schema([
                        Grid::make(['default' => 1, 'sm' => 2])->schema([
                            TextInput::make('name')
                                ->label(__('Import Name'))
                                ->default(fn (): string => $this->name ?: ('HestiaCP Import '.now()->format('Y-m-d H:i')))
                                ->maxLength(255)
                                ->required(),
                        ]),

                        FileUpload::make('backupPath')
                            ->label(__('HestiaCP / VestaCP Backup Archive'))
                            ->helperText(__('Upload a HestiaCP or VestaCP backup archive (.tar, .tar.gz, .tgz).'))
                            ->disk('backups')
                            ->directory('hestiacp-migrations')
                            ->acceptedFileTypes(['application/x-tar', 'application/gzip', 'application/x-gzip', 'application/x-compressed-tar'])
                            ->preserveFilenames(),
                        TextInput::make('backupFilePath')
                            ->label(__('Backup File Path'))
                            ->placeholder('/var/backups/jabali/hestiacp-migrations/user.2024-01-01.tar')
                            ->helperText(__('Use this if the backup file already exists on the server.')),
                        Text::make(__('Tip: Upload backups to /var/backups/jabali/hestiacp-migrations/'))->color('gray'),

                        FormActions::make([
                            Action::make('discoverAccounts')
                                ->label(__('Discover Accounts'))
                                ->icon('heroicon-o-magnifying-glass')
                                ->color('primary')
                                ->action('discoverAccounts'),
                        ])->alignEnd(),
                    ]),

                Section::make(__('Discovery'))
                    ->description(__('Once accounts are discovered, proceed to select which ones to import.'))
                    ->icon('heroicon-o-user-group')
                    ->schema([
                        Text::make(__('Discovered accounts will appear in the next step.'))->color('gray'),
                    ]),
            ])
            ->afterValidation(function () {
                $import = $this->getImport();
                $hasAccounts = $import?->accounts()->exists() ?? false;

                if (! $hasAccounts) {
                    Notification::make()
                        ->title(__('No accounts discovered'))
                        ->body(__('Click "Discover Accounts" to continue.'))
                        ->danger()
                        ->send();

                    throw new Exception(__('No accounts discovered'));
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
            ->description(__('Choose which HestiaCP accounts to migrate'))
            ->schema([
                Section::make(__('HestiaCP Accounts'))
                    ->description(fn (): string => $this->getAccountsStepDescription())
                    ->icon('heroicon-o-user-group')
                    ->headerActions([
                        Action::make('refreshAccounts')
                            ->label(__('Refresh'))
                            ->icon('heroicon-o-arrow-path')
                            ->color('gray')
                            ->action('refreshAccountsTable'),
                        Action::make('selectAll')
                            ->label(__('Select All'))
                            ->icon('heroicon-o-check')
                            ->color('primary')
                            ->action('selectAllAccounts')
                            ->visible(fn (): bool => $this->getSelectedAccountsCount() < $this->getDiscoveredAccountsCount()),
                        Action::make('deselectAll')
                            ->label(__('Deselect All'))
                            ->icon('heroicon-o-x-mark')
                            ->color('gray')
                            ->action('deselectAllAccounts')
                            ->visible(fn (): bool => $this->getSelectedAccountsCount() > 0),
                    ])
                    ->schema([
                        View::make('filament.admin.pages.hestiacp-accounts-table'),
                    ]),
            ])
            ->afterValidation(function () {
                if ($this->getSelectedAccountsCount() === 0) {
                    Notification::make()
                        ->title(__('No accounts selected'))
                        ->body(__('Please select at least one account to migrate.'))
                        ->danger()
                        ->send();

                    throw new Exception(__('No accounts selected'));
                }

                $this->saveToSession();
            });
    }

    protected function getConfigureStep(): Step
    {
        return Step::make(__('Configure'))
            ->id('configure')
            ->icon('heroicon-o-cog')
            ->description(__('Choose what to import and map accounts'))
            ->schema([
                Section::make(__('What to Import'))
                    ->description(__('Select which parts of each account to import.'))
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

                Section::make(__('Account Mappings'))
                    ->description(fn (): string => __(':count account(s) selected', ['count' => $this->getSelectedAccountsCount()]))
                    ->icon('heroicon-o-arrow-right')
                    ->schema([
                        View::make('filament.admin.pages.hestiacp-account-config-table'),
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
                $this->dispatch('hestiacp-config-updated');
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
                        ->modalDescription(__('This will migrate :count account(s). Continue?', ['count' => $this->getSelectedAccountsCount()]))
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
                        View::make('filament.admin.pages.hestiacp-migration-status-table'),
                    ]),
            ]);
    }

    public function discoverAccounts(): void
    {
        try {
            $import = $this->upsertImportForDiscovery();

            if (! $import->backup_path) {
                throw new Exception(__('Please upload a HestiaCP backup archive or enter its full path.'));
            }

            $backupFullPath = $this->resolveBackupFullPath($import->backup_path);
            if (! $backupFullPath) {
                throw new Exception(__('Backup file not found: :path', ['path' => $import->backup_path]));
            }

            $result = $this->getAgent()->importDiscover(
                $import->id,
                'hestiacp',
                $import->import_method,
                $backupFullPath,
                null,
                null,
                null,
                null,
            );

            if (! ($result['success'] ?? false)) {
                throw new Exception((string) ($result['error'] ?? __('Discovery failed')));
            }

            $accounts = $result['accounts'] ?? [];
            if (! is_array($accounts) || $accounts === []) {
                throw new Exception(__('No accounts were discovered.'));
            }

            $import->accounts()->delete();
            $createdIds = [];

            foreach ($accounts as $account) {
                if (! is_array($account)) {
                    continue;
                }

                $username = trim((string) ($account['username'] ?? ''));
                if ($username === '' || ! preg_match('/^[a-z][a-z0-9_]{0,31}$/', $username)) {
                    continue;
                }

                $record = ServerImportAccount::create([
                    'server_import_id' => $import->id,
                    'source_username' => $username,
                    'target_username' => $username,
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

                $createdIds[] = $record->id;
            }

            if ($createdIds === []) {
                throw new Exception(__('No valid accounts were discovered.'));
            }

            $import->update([
                'discovered_accounts' => $accounts,
                'selected_accounts' => [],
                'status' => 'ready',
                'progress' => 0,
                'current_task' => null,
                'errors' => [],
            ]);

            $this->importId = $import->id;
            $this->step1Complete = true;
            $this->saveToSession();

            $this->dispatch('hestiacp-accounts-updated');

            Notification::make()
                ->title(__('Accounts discovered'))
                ->body(__('Found :count account(s).', ['count' => count($createdIds)]))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Discovery failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    protected function resolveBackupFullPath(?string $path): ?string
    {
        $path = trim((string) ($path ?? ''));
        if ($path === '') {
            return null;
        }

        if (str_contains($path, "\0") || str_contains($path, '..')) {
            return null;
        }

        $localRoot = realpath(Storage::disk('local')->path('')) ?: null;
        $backupRoot = realpath(Storage::disk('backups')->path('')) ?: null;
        $allowedAbsRoots = array_values(array_filter([
            $localRoot,
            $backupRoot,
            realpath('/var/backups/jabali') ?: null,
        ]));

        $isWithin = static function (string $candidate, string $root): bool {
            $root = rtrim($root, DIRECTORY_SEPARATOR).DIRECTORY_SEPARATOR;
            $candidate = rtrim($candidate, DIRECTORY_SEPARATOR);

            return str_starts_with($candidate.DIRECTORY_SEPARATOR, $root);
        };

        $resolveAllowed = static function (string $candidate) use ($allowedAbsRoots, $isWithin): ?string {
            $real = realpath($candidate);
            if ($real === false) {
                return null;
            }

            foreach ($allowedAbsRoots as $root) {
                if ($isWithin($real, $root)) {
                    return $real;
                }
            }

            return null;
        };

        if (str_starts_with($path, '/')) {
            return $resolveAllowed($path);
        }

        $localCandidate = Storage::disk('local')->path($path);
        if ($resolved = $resolveAllowed($localCandidate)) {
            return $resolved;
        }

        $backupCandidate = Storage::disk('backups')->path($path);
        if ($resolved = $resolveAllowed($backupCandidate)) {
            return $resolved;
        }

        return null;
    }

    public function selectAllAccounts(): void
    {
        $import = $this->getImport();
        if (! $import) {
            return;
        }

        $ids = $import->accounts()->pluck('id')->all();
        $import->update(['selected_accounts' => $ids]);

        $this->dispatch('hestiacp-selection-updated');
    }

    public function deselectAllAccounts(): void
    {
        $import = $this->getImport();
        if (! $import) {
            return;
        }

        $import->update(['selected_accounts' => []]);

        $this->dispatch('hestiacp-selection-updated');
    }

    public function refreshAccountsTable(): void
    {
        $this->dispatch('hestiacp-accounts-updated');
        $this->dispatch('hestiacp-config-updated');
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
                ->title(__('No accounts selected'))
                ->body(__('Please select at least one account to migrate.'))
                ->danger()
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
    }

    public function resetMigration(): void
    {
        if ($this->importId) {
            ServerImport::whereKey($this->importId)->delete();
        }

        session()->forget('hestiacp_migration.import_id');

        $this->wizardStep = null;
        $this->step1Complete = false;
        $this->importId = null;
        $this->name = null;
        $this->importMethod = 'backup_file';
        $this->backupPath = null;
        $this->backupFilePath = null;
        $this->importFiles = true;
        $this->importDatabases = true;
        $this->importEmails = true;
        $this->importSsl = true;
    }

    protected function getAgent(): AgentClient
    {
        return app(AgentClient::class);
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
        $name = trim((string) ($this->name ?: ''));
        if ($name === '') {
            $name = 'HestiaCP Import '.now()->format('Y-m-d H:i');
        }

        $backupPath = filled($this->backupFilePath)
            ? trim((string) $this->backupFilePath)
            : $this->backupPath;

        $attributes = [
            'name' => $name,
            'source_type' => 'hestiacp',
            'import_method' => 'backup_file',
            'import_options' => [
                'files' => $this->importFiles,
                'databases' => $this->importDatabases,
                'emails' => $this->importEmails,
                'ssl' => $this->importSsl,
            ],
            'status' => 'discovering',
            'progress' => 0,
            'current_task' => null,
            'backup_path' => $backupPath ?: null,
            'remote_host' => null,
            'remote_port' => null,
            'remote_user' => null,
        ];

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

    protected function getDiscoveredAccountsCount(): int
    {
        $import = $this->getImport();

        return $import ? $import->accounts()->count() : 0;
    }

    protected function getSelectedAccountsCount(): int
    {
        $import = $this->getImport();
        $selected = $import?->selected_accounts ?? [];

        return is_array($selected) ? count($selected) : 0;
    }

    protected function getAccountsStepDescription(): string
    {
        $selected = $this->getSelectedAccountsCount();
        $total = $this->getDiscoveredAccountsCount();

        if ($total === 0) {
            return __('No accounts discovered yet.');
        }

        if ($selected === 0) {
            return __(':count accounts discovered', ['count' => $total]);
        }

        return __(':selected of :count accounts selected', ['selected' => $selected, 'count' => $total]);
    }

    protected function saveToSession(): void
    {
        if ($this->importId) {
            session()->put('hestiacp_migration.import_id', $this->importId);
        }

        session()->save();
    }

    protected function restoreFromSession(): void
    {
        $this->importId = session('hestiacp_migration.import_id');
    }

    protected function restoreFromImport(): void
    {
        $import = $this->getImport();
        if (! $import) {
            return;
        }

        $this->name = $import->name;
        $this->importMethod = 'backup_file';

        $backupPath = is_string($import->backup_path) ? trim($import->backup_path) : null;
        if ($backupPath && str_starts_with($backupPath, '/')) {
            $this->backupFilePath = $backupPath;
            $this->backupPath = null;
        } else {
            $this->backupPath = $backupPath;
            $this->backupFilePath = null;
        }

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
