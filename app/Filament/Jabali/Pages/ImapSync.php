<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Jobs\RunImapSync;
use App\Models\ImapSyncTask;
use App\Models\Mailbox;
use App\Services\Agent\AgentClient;
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
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Actions as FormActions;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;

class ImapSync extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-envelope';

    protected static bool $shouldRegisterNavigation = false;

    protected string $view = 'filament.jabali.pages.imap-sync';

    // Single sync form
    public ?string $sourceHost = null;

    public int $sourcePort = 993;

    public string $sourceEncryption = 'ssl';

    public ?string $sourceEmail = null;

    public ?string $sourcePassword = null;

    public ?int $targetMailboxId = null;

    public array $selectedFolders = [];

    public array $availableFolders = [];

    public bool $isConnected = false;

    // Bulk sync
    public ?string $bulkAccounts = null;

    public ?string $bulkSourceHost = null;

    public int $bulkSourcePort = 993;

    public string $bulkSourceEncryption = 'ssl';

    // Status tracking
    public bool $isProcessing = false;

    public array $statusLog = [];

    public ?string $syncJobId = null;

    public ?string $syncLogPath = null;

    public ?string $syncStatus = null;

    public int $pollCount = 0;

    public function getTitle(): string|Htmlable
    {
        return __('IMAP Sync');
    }

    public function getSubheading(): ?string
    {
        return __('Sync emails from an external IMAP server to your Jabali mailbox');
    }

    protected function getForms(): array
    {
        return ['migrationForm'];
    }

    public function migrationForm(Schema $schema): Schema
    {
        return $schema->schema([
            // Single Sync Section
            Section::make(__('Single Sync'))
                ->description(__('Sync emails from a single IMAP account'))
                ->icon('heroicon-o-envelope')
                ->collapsed(false)
                ->schema([
                    Grid::make(2)->schema([
                        TextInput::make('sourceHost')
                            ->label(__('Source IMAP Server'))
                            ->placeholder('mail.example.com')
                            ->required()
                            ->disabled(fn () => $this->isProcessing),
                        Grid::make(2)->schema([
                            TextInput::make('sourcePort')
                                ->label(__('Source Port'))
                                ->numeric()
                                ->minValue(1)
                                ->maxValue(65535)
                                ->default(993)
                                ->required()
                                ->disabled(fn () => $this->isProcessing),
                            Select::make('sourceEncryption')
                                ->label(__('Encryption'))
                                ->options([
                                    'ssl' => 'SSL',
                                    'tls' => 'TLS',
                                    'none' => __('None'),
                                ])
                                ->default('ssl')
                                ->required()
                                ->disabled(fn () => $this->isProcessing),
                        ]),
                    ]),
                    Grid::make(2)->schema([
                        TextInput::make('sourceEmail')
                            ->label(__('Source Email'))
                            ->email()
                            ->required()
                            ->disabled(fn () => $this->isProcessing),
                        TextInput::make('sourcePassword')
                            ->label(__('Source Password'))
                            ->password()
                            ->revealable()
                            ->required()
                            ->disabled(fn () => $this->isProcessing),
                    ]),
                    FormActions::make([
                        Action::make('testConnection')
                            ->label(__('Test Connection'))
                            ->icon('heroicon-o-signal')
                            ->color('gray')
                            ->disabled(fn () => $this->isProcessing)
                            ->action('testConnection'),
                    ]),
                    // Show after successful connection
                    CheckboxList::make('selectedFolders')
                        ->label(__('Select Folders'))
                        ->helperText(__('Leave empty to sync all folders'))
                        ->options(fn () => array_combine($this->availableFolders, $this->availableFolders) ?: [])
                        ->columns(3)
                        ->disabled(fn () => $this->isProcessing)
                        ->visible(fn () => $this->isConnected && ! empty($this->availableFolders)),
                    Select::make('targetMailboxId')
                        ->label(__('Target Mailbox'))
                        ->options(fn () => $this->getMailboxOptions())
                        ->required()
                        ->searchable()
                        ->disabled(fn () => $this->isProcessing)
                        ->visible(fn () => $this->isConnected),
                    FormActions::make([
                        Action::make('startSync')
                            ->label(__('Start Sync'))
                            ->icon('heroicon-o-play')
                            ->color('primary')
                            ->disabled(fn () => $this->isProcessing)
                            ->requiresConfirmation()
                            ->modalHeading(__('Start IMAP Sync'))
                            ->modalDescription(__('This will start syncing emails from the source server. Continue?'))
                            ->action('startSync'),
                    ])->visible(fn () => $this->isConnected),
                ]),

            // Bulk Sync Section
            Section::make(__('Bulk Sync'))
                ->description(__('Sync multiple IMAP accounts at once'))
                ->icon('heroicon-o-squares-2x2')
                ->collapsed(true)
                ->schema([
                    Grid::make(2)->schema([
                        TextInput::make('bulkSourceHost')
                            ->label(__('Source IMAP Server'))
                            ->placeholder('mail.example.com')
                            ->required()
                            ->disabled(fn () => $this->isProcessing),
                        Grid::make(2)->schema([
                            TextInput::make('bulkSourcePort')
                                ->label(__('Source Port'))
                                ->numeric()
                                ->minValue(1)
                                ->maxValue(65535)
                                ->default(993)
                                ->required()
                                ->disabled(fn () => $this->isProcessing),
                            Select::make('bulkSourceEncryption')
                                ->label(__('Encryption'))
                                ->options([
                                    'ssl' => 'SSL',
                                    'tls' => 'TLS',
                                    'none' => __('None'),
                                ])
                                ->default('ssl')
                                ->required()
                                ->disabled(fn () => $this->isProcessing),
                        ]),
                    ]),
                    Textarea::make('bulkAccounts')
                        ->label(__('Accounts'))
                        ->helperText(__('Enter one account per line in format: source_email:source_password:destination_email'))
                        ->placeholder("user1@old-server.com:password1:user1@example.com\nuser2@old-server.com:password2:user2@example.com")
                        ->rows(6)
                        ->required()
                        ->disabled(fn () => $this->isProcessing),
                    FormActions::make([
                        Action::make('startBulkSync')
                            ->label(__('Start Bulk Sync'))
                            ->icon('heroicon-o-play')
                            ->color('primary')
                            ->disabled(fn () => $this->isProcessing)
                            ->requiresConfirmation()
                            ->modalHeading(__('Start Bulk Sync'))
                            ->modalDescription(__('This will start syncing all listed accounts. Continue?'))
                            ->action('startBulkSync'),
                    ]),
                ]),

            // Sync History Section
            Section::make(__('Sync History'))
                ->description(__('View status of current and past sync operations'))
                ->icon('heroicon-o-clock')
                ->collapsed(fn () => ImapSyncTask::forUser(Auth::id())->count() === 0)
                ->schema([
                    \Filament\Schemas\Components\View::make('filament.jabali.pages.imap-sync-history'),
                ]),
        ]);
    }

    public function testConnection(): void
    {
        if (empty($this->sourceHost) || empty($this->sourceEmail) || empty($this->sourcePassword)) {
            Notification::make()
                ->title(__('Please fill in all connection fields'))
                ->danger()
                ->send();

            return;
        }

        try {
            $agent = app(AgentClient::class);
            $result = $agent->send('email.imap_test_connection', [
                'host' => $this->sourceHost,
                'port' => $this->sourcePort,
                'encryption' => $this->sourceEncryption,
                'email' => $this->sourceEmail,
                'password' => $this->sourcePassword,
            ]);

            if ($result['success'] ?? false) {
                $this->isConnected = true;
                $this->availableFolders = $result['folders'] ?? [];
                Notification::make()
                    ->title(__('Connection successful'))
                    ->body(__(':count folders found', ['count' => count($this->availableFolders)]))
                    ->success()
                    ->send();
            } else {
                $this->isConnected = false;
                $this->availableFolders = [];
                Notification::make()
                    ->title(__('Connection failed'))
                    ->body($result['error'] ?? __('Unable to connect to IMAP server'))
                    ->danger()
                    ->send();
            }
        } catch (Exception $e) {
            $this->isConnected = false;
            $this->availableFolders = [];
            Log::error('IMAP test connection failed', ['error' => $e->getMessage()]);
            Notification::make()
                ->title(__('Connection failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function startSync(): void
    {
        if (empty($this->targetMailboxId)) {
            Notification::make()
                ->title(__('Please select a target mailbox'))
                ->danger()
                ->send();

            return;
        }

        $mailbox = Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))
            ->where('id', $this->targetMailboxId)
            ->first();

        if (! $mailbox) {
            Notification::make()
                ->title(__('Invalid target mailbox'))
                ->danger()
                ->send();

            return;
        }

        $logPath = storage_path('logs/imap-sync/'.Str::uuid().'.log');

        $task = ImapSyncTask::create([
            'user_id' => Auth::id(),
            'mailbox_id' => $mailbox->id,
            'source_host' => $this->sourceHost,
            'source_port' => $this->sourcePort,
            'source_encryption' => $this->sourceEncryption,
            'source_email' => $this->sourceEmail,
            'source_password_encrypted' => \Illuminate\Support\Facades\Crypt::encryptString($this->sourcePassword),
            'folders' => ! empty($this->selectedFolders) ? $this->selectedFolders : null,
            'status' => 'pending',
            'log_path' => $logPath,
        ]);

        RunImapSync::dispatch($task->id);

        $this->isProcessing = true;
        $this->syncJobId = (string) $task->id;
        $this->syncLogPath = $logPath;
        $this->syncStatus = 'pending';

        Notification::make()
            ->title(__('IMAP sync started'))
            ->body(__('Syncing emails from :source to :dest', [
                'source' => $this->sourceEmail,
                'dest' => $mailbox->email,
            ]))
            ->success()
            ->send();
    }

    public function startBulkSync(): void
    {
        if (empty($this->bulkSourceHost) || empty($this->bulkAccounts)) {
            Notification::make()
                ->title(__('Please fill in all required fields'))
                ->danger()
                ->send();

            return;
        }

        $lines = array_filter(array_map('trim', explode("\n", $this->bulkAccounts)));
        if (empty($lines)) {
            Notification::make()
                ->title(__('No accounts provided'))
                ->danger()
                ->send();

            return;
        }

        $batchId = Str::uuid()->toString();
        $created = 0;
        $errors = [];

        foreach ($lines as $index => $line) {
            $parts = explode(':', $line, 3);
            if (count($parts) !== 3) {
                $errors[] = __('Line :line: Invalid format (expected source_email:password:destination_email)', ['line' => $index + 1]);

                continue;
            }

            [$srcEmail, $srcPassword, $destEmail] = $parts;
            $srcEmail = trim($srcEmail);
            $srcPassword = trim($srcPassword);
            $destEmail = trim($destEmail);

            // Find destination mailbox
            $mailbox = Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))
                ->where('is_active', true)
                ->where('imap_enabled', true)
                ->whereHas('emailDomain', function ($q) use ($destEmail) {
                    $parts = explode('@', $destEmail);
                    if (count($parts) === 2) {
                        $q->where('domain_name', $parts[1]);
                    }
                })
                ->where('local_part', explode('@', $destEmail)[0] ?? '')
                ->first();

            if (! $mailbox) {
                $errors[] = __('Line :line: Destination mailbox :email not found', ['line' => $index + 1, 'email' => $destEmail]);

                continue;
            }

            $logPath = storage_path('logs/imap-sync/'.$batchId.'/'.Str::slug($srcEmail).'.log');

            $task = ImapSyncTask::create([
                'user_id' => Auth::id(),
                'mailbox_id' => $mailbox->id,
                'source_host' => $this->bulkSourceHost,
                'source_port' => $this->bulkSourcePort,
                'source_encryption' => $this->bulkSourceEncryption,
                'source_email' => $srcEmail,
                'source_password_encrypted' => \Illuminate\Support\Facades\Crypt::encryptString($srcPassword),
                'batch_id' => $batchId,
                'status' => 'pending',
                'log_path' => $logPath,
            ]);

            RunImapSync::dispatch($task->id);
            $created++;
        }

        if (! empty($errors)) {
            Notification::make()
                ->title(__(':count errors found', ['count' => count($errors)]))
                ->body(implode("\n", array_slice($errors, 0, 5)))
                ->danger()
                ->send();
        }

        if ($created > 0) {
            $this->isProcessing = true;
            Notification::make()
                ->title(__('Bulk sync started'))
                ->body(__(':count sync tasks queued', ['count' => $created]))
                ->success()
                ->send();
        }
    }

    public function pollSyncLog(): void
    {
        $this->pollCount++;

        // Check if any active tasks remain
        $activeTasks = ImapSyncTask::forUser(Auth::id())->active()->count();
        if ($activeTasks === 0 && $this->isProcessing) {
            $this->isProcessing = false;
            $this->syncStatus = 'completed';
        }

        // Read log file for single sync
        if ($this->syncLogPath && File::exists($this->syncLogPath)) {
            $lines = array_filter(explode("\n", File::get($this->syncLogPath)));
            $this->statusLog = array_map(fn ($line) => json_decode($line, true) ?? ['message' => $line, 'status' => 'info', 'time' => ''], $lines);
        }

        // Stop polling after too many checks with no active tasks
        if ($this->pollCount > 720 && $activeTasks === 0) {
            $this->isProcessing = false;
        }
    }

    public function cancelTask(int $taskId): void
    {
        $task = ImapSyncTask::forUser(Auth::id())->where('id', $taskId)->first();
        if ($task && in_array($task->status, ['pending', 'running'])) {
            $task->update([
                'status' => 'cancelled',
                'completed_at' => now(),
            ]);
            Notification::make()
                ->title(__('Sync task cancelled'))
                ->success()
                ->send();
        }
    }

    public function retryTask(int $taskId): void
    {
        $task = ImapSyncTask::forUser(Auth::id())->where('id', $taskId)->first();
        if ($task && in_array($task->status, ['failed', 'cancelled'])) {
            $logPath = storage_path('logs/imap-sync/'.Str::uuid().'.log');

            $newTask = ImapSyncTask::create([
                'user_id' => $task->user_id,
                'mailbox_id' => $task->mailbox_id,
                'source_host' => $task->source_host,
                'source_port' => $task->source_port,
                'source_encryption' => $task->source_encryption,
                'source_email' => $task->source_email,
                'source_password_encrypted' => $task->source_password_encrypted,
                'folders' => $task->folders,
                'batch_id' => $task->batch_id,
                'status' => 'pending',
                'log_path' => $logPath,
            ]);

            RunImapSync::dispatch($newTask->id);

            $this->isProcessing = true;
            Notification::make()
                ->title(__('Sync task restarted'))
                ->success()
                ->send();
        }
    }

    public function getSyncHistoryProperty(): \Illuminate\Database\Eloquent\Collection
    {
        return ImapSyncTask::forUser(Auth::id())
            ->with('mailbox.emailDomain')
            ->orderByDesc('created_at')
            ->limit(50)
            ->get();
    }

    protected function getMailboxOptions(): array
    {
        return Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))
            ->where('is_active', true)
            ->where('imap_enabled', true)
            ->with('emailDomain')
            ->get()
            ->mapWithKeys(fn ($m) => [$m->id => $m->local_part.'@'.$m->emailDomain->domain_name])
            ->toArray();
    }

    public function updatedSourceHost(): void
    {
        $this->resetConnection();
    }

    public function updatedSourceEmail(): void
    {
        $this->resetConnection();
    }

    public function updatedSourcePassword(): void
    {
        $this->resetConnection();
    }

    public function updatedSourcePort(): void
    {
        $this->resetConnection();
    }

    public function updatedSourceEncryption(): void
    {
        $this->resetConnection();
    }

    protected function resetConnection(): void
    {
        $this->isConnected = false;
        $this->availableFolders = [];
        $this->selectedFolders = [];
    }
}
