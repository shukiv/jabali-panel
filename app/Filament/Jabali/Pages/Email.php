<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\AuditLog;
use App\Models\Autoresponder;
use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\Domain;
use App\Models\EmailDomain;
use App\Models\EmailForwarder;
use App\Models\Mailbox;
use App\Models\MailboxShare;
use App\Models\UserSetting;
use App\Services\Agent\AgentClient;
use App\Services\MailboxSharingService;
use App\Services\RoundcubeIdentityService;
use App\Services\System\MailRoutingSyncService;
use App\Support\ServerFacts;
use App\Support\WordList;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\DatePicker;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Infolists\Components\TextEntry;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\Log;
use Livewire\Attributes\Url;

class Email extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-envelope';

    protected static ?int $navigationSort = 3;

    public static function getNavigationLabel(): string
    {
        return __('Email');
    }

    protected string $view = 'filament.jabali.pages.email';

    #[Url(as: 'tab')]
    public ?string $activeTab = 'mailboxes';

    public string $credEmail = '';

    public string $credPassword = '';

    public array $spamFormData = [];

    protected ?AgentClient $agent = null;

    public function getTitle(): string|Htmlable
    {
        return __('Email Management');
    }

    public function mount(): void
    {
        // Normalize the tab value from URL
        $this->activeTab = $this->normalizeTabName($this->activeTab);

        if ($this->activeTab === 'spam') {
            $this->loadSpamSettings();
        }
    }

    public function updatedActiveTab(): void
    {
        $this->activeTab = $this->normalizeTabName($this->activeTab);
        if ($this->activeTab === 'spam') {
            $this->loadSpamSettings();
        }
        $this->resetTable();
    }

    protected function normalizeTabName(?string $tab): string
    {
        // Handle Filament's tab format "tabname::tab" or just "tabname"
        $tab = $tab ?? 'mailboxes';
        if (str_contains($tab, '::')) {
            $tab = explode('::', $tab)[0];
        }

        // Map to valid tab names
        return match ($tab) {
            'mailboxes', 'Mailboxes' => 'mailboxes',
            'forwarders', 'Forwarders' => 'forwarders',
            'autoresponders', 'Autoresponders' => 'autoresponders',
            'catchall', 'catch-all', 'Catch-All' => 'catchall',
            'disclaimer', 'Disclaimer' => 'disclaimer',
            'sharing', 'Sharing', 'shared', 'Shared Folders' => 'sharing',
            'logs', 'Logs' => 'logs',
            'spam', 'Spam' => 'spam',
            default => 'mailboxes',
        };
    }

    protected function getActiveTabIndex(): int
    {
        return match ($this->activeTab) {
            'mailboxes' => 1,
            'forwarders' => 2,
            'autoresponders' => 3,
            'catchall' => 4,
            'disclaimer' => 5,
            'sharing' => 6,
            'logs' => 7,
            'spam' => 8,
            default => 1,
        };
    }

    protected function getForms(): array
    {
        return ['emailForm', 'spamForm'];
    }

    public function emailForm(Schema $schema): Schema
    {
        return $schema->schema([
            Tabs::make(__('Email Sections'))
                ->contained()
                ->livewireProperty('activeTab')
                ->tabs([
                    'mailboxes' => Tab::make(__('Mailboxes'))
                        ->icon('heroicon-o-envelope')
                        ->schema([
                            View::make('filament.jabali.pages.email-tab-table'),
                        ]),
                    'forwarders' => Tab::make(__('Forwarders'))
                        ->icon('heroicon-o-arrow-right')
                        ->schema([
                            View::make('filament.jabali.pages.email-tab-table'),
                        ]),
                    'autoresponders' => Tab::make(__('Autoresponders'))
                        ->icon('heroicon-o-clock')
                        ->schema([
                            View::make('filament.jabali.pages.email-tab-table'),
                        ]),
                    'catchall' => Tab::make(__('Catch-All'))
                        ->icon('heroicon-o-inbox-stack')
                        ->schema([
                            View::make('filament.jabali.pages.email-tab-table'),
                        ]),
                    'disclaimer' => Tab::make(__('Disclaimer'))
                        ->icon('heroicon-o-document-text')
                        ->schema([
                            View::make('filament.jabali.pages.email-tab-table'),
                        ]),
                    'sharing' => Tab::make(__('Shared Folders'))
                        ->icon('heroicon-o-share')
                        ->schema([
                            View::make('filament.jabali.pages.email-tab-table'),
                        ]),
                    'logs' => Tab::make(__('Logs'))
                        ->icon('heroicon-o-document-text')
                        ->schema([
                            View::make('filament.jabali.pages.email-tab-table'),
                        ]),
                    'spam' => Tab::make(__('Spam Settings'))
                        ->icon('heroicon-o-shield-check')
                        ->schema([
                            View::make('filament.jabali.pages.email-tab-spam'),
                        ]),
                ]),
        ]);
    }

    public function spamForm(Schema $schema): Schema
    {
        return $schema
            ->statePath('spamFormData')
            ->schema([
                Section::make(__('Spam Settings'))
                    ->schema([
                        Textarea::make('whitelist')
                            ->label(__('Whitelist (one per line)'))
                            ->rows(6)
                            ->placeholder(__("friend@example.com\ntrusted.com")),
                        Textarea::make('blacklist')
                            ->label(__('Blacklist (one per line)'))
                            ->rows(6)
                            ->placeholder(__("spam@example.com\nbad-domain.com")),
                        TextInput::make('score')
                            ->label(__('Spam Score Threshold'))
                            ->numeric()
                            ->default(6.0)
                            ->helperText(__('Lower values are stricter, higher values are more permissive.')),
                    ])
                    ->columns(2),
            ]);
    }

    public function setTab(string $tab): void
    {
        $this->activeTab = $this->normalizeTabName($tab);
        if ($this->activeTab === 'spam') {
            $this->loadSpamSettings();
        }
        $this->resetTable();
    }

    public function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient;
        }

        return $this->agent;
    }

    public function getUsername(): string
    {
        return Auth::user()->username;
    }

    public function generateSecurePassword(int $length = 16): string
    {
        $lowercase = 'abcdefghijklmnopqrstuvwxyz';
        $uppercase = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ';
        $numbers = '0123456789';
        $special = '!@#$%^&*';

        // Ensure at least one of each required type
        $password = $lowercase[random_int(0, strlen($lowercase) - 1)]
            .$uppercase[random_int(0, strlen($uppercase) - 1)]
            .$numbers[random_int(0, strlen($numbers) - 1)]
            .$special[random_int(0, strlen($special) - 1)];

        // Fill the rest with random characters from all types
        $allChars = $lowercase.$uppercase.$numbers.$special;
        for ($i = strlen($password); $i < $length; $i++) {
            $password .= $allChars[random_int(0, strlen($allChars) - 1)];
        }

        // Shuffle the password to randomize position of required characters
        return str_shuffle($password);
    }

    protected function loadSpamSettings(): void
    {
        $settings = UserSetting::getForUser(Auth::id(), 'spam_settings', [
            'whitelist' => [],
            'blacklist' => [],
            'score' => 6.0,
        ]);

        $this->spamFormData = [
            'whitelist' => implode("\n", $settings['whitelist'] ?? []),
            'blacklist' => implode("\n", $settings['blacklist'] ?? []),
            'score' => $settings['score'] ?? 6.0,
        ];
    }

    public function saveSpamSettings(): void
    {
        $data = $this->spamForm->getState();
        $whitelist = $this->linesToArray($data['whitelist'] ?? '');
        $blacklist = $this->linesToArray($data['blacklist'] ?? '');
        $score = isset($data['score']) && $data['score'] !== '' ? (float) $data['score'] : null;

        UserSetting::setForUser(Auth::id(), 'spam_settings', [
            'whitelist' => $whitelist,
            'blacklist' => $blacklist,
            'score' => $score,
        ]);

        $result = $this->getAgent()->rspamdUserSettings($this->getUsername(), $whitelist, $blacklist, $score);
        if (! ($result['success'] ?? false)) {
            Notification::make()
                ->title(__('Failed to update spam settings'))
                ->body($result['error'] ?? '')
                ->danger()
                ->send();

            return;
        }

        Notification::make()
            ->title(__('Spam settings updated'))
            ->success()
            ->send();
    }

    protected function linesToArray(string $value): array
    {
        return collect(preg_split('/\\r\\n|\\r|\\n/', $value))
            ->map(fn ($line) => trim((string) $line))
            ->filter()
            ->values()
            ->toArray();
    }

    public function table(Table $table): Table
    {
        return match ($this->activeTab) {
            'mailboxes' => $this->mailboxesTable($table),
            'forwarders' => $this->forwardersTable($table),
            'autoresponders' => $this->autorespondersTable($table),
            'catchall' => $this->catchAllTable($table),
            'disclaimer' => $this->disclaimerTable($table),
            'sharing' => $this->sharingTable($table),
            'logs' => $this->emailLogsTable($table),
            'spam' => $this->mailboxesTable($table),
            default => $this->mailboxesTable($table),
        };
    }

    protected function mailboxesTable(Table $table): Table
    {
        return $table
            ->query(
                Mailbox::query()
                    ->whereHas('emailDomain.domain', fn (Builder $q) => $q->where('user_id', Auth::id()))
                    ->with('emailDomain.domain')
            )
            ->columns([
                TextColumn::make('email')
                    ->label(__('Email Address'))
                    ->icon('heroicon-o-envelope')
                    ->iconColor('primary')
                    ->description(fn (Mailbox $record) => $record->name)
                    ->searchable()
                    ->sortable(),
                TextColumn::make('quota_display')
                    ->label(__('Quota'))
                    ->getStateUsing(fn (Mailbox $record) => $record->quota_used_formatted.' / '.$record->quota_formatted)
                    ->description(fn (Mailbox $record) => $record->quota_percent.'% '.__('used'))
                    ->color(fn (Mailbox $record) => match (true) {
                        $record->quota_percent >= 90 => 'danger',
                        $record->quota_percent >= 80 => 'warning',
                        default => 'gray',
                    }),
                TextColumn::make('is_active')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (bool $state) => $state ? __('Active') : __('Suspended'))
                    ->color(fn (bool $state) => $state ? 'success' : 'danger'),
                TextColumn::make('last_login_at')
                    ->label(__('Last Login'))
                    ->since()
                    ->placeholder(__('Never'))
                    ->sortable(),
            ])
            ->recordActions([
                Action::make('webmail')
                    ->label(__('Webmail'))
                    ->icon('heroicon-o-envelope-open')
                    ->color('success')
                    ->url(fn (Mailbox $record) => route('webmail.sso', $record))
                    ->openUrlInNewTab(),
                Action::make('info')
                    ->label(__('Info'))
                    ->icon('heroicon-o-information-circle')
                    ->color('info')
                    ->modalHeading(fn (Mailbox $record) => __('Connection Settings'))
                    ->modalDescription(fn (Mailbox $record) => $record->email)
                    ->modalSubmitAction(false)
                    ->modalCancelActionLabel(__('Close'))
                    ->infolist(function (Mailbox $record): array {
                        $serverHostname = \App\Models\Setting::get('mail_hostname') ?: request()->getHost();

                        return [
                            Section::make(__('IMAP Settings'))
                                ->description(__('For receiving email in mail clients'))
                                ->icon('heroicon-o-inbox-arrow-down')
                                ->columns(3)
                                ->schema([
                                    TextEntry::make('imap_server')
                                        ->label(__('Server'))
                                        ->state($serverHostname)
                                        ->copyable(),
                                    TextEntry::make('imap_port')
                                        ->label(__('Port'))
                                        ->state('993')
                                        ->copyable(),
                                    TextEntry::make('imap_security')
                                        ->label(__('Security'))
                                        ->state('SSL/TLS')
                                        ->badge()
                                        ->color('success'),
                                ]),
                            Section::make(__('POP3 Settings'))
                                ->description(__('Alternative for receiving email'))
                                ->icon('heroicon-o-arrow-down-tray')
                                ->columns(3)
                                ->collapsed()
                                ->schema([
                                    TextEntry::make('pop3_server')
                                        ->label(__('Server'))
                                        ->state($serverHostname)
                                        ->copyable(),
                                    TextEntry::make('pop3_port')
                                        ->label(__('Port'))
                                        ->state('995')
                                        ->copyable(),
                                    TextEntry::make('pop3_security')
                                        ->label(__('Security'))
                                        ->state('SSL/TLS')
                                        ->badge()
                                        ->color('success'),
                                ]),
                            Section::make(__('SMTP Settings'))
                                ->description(__('For sending email'))
                                ->icon('heroicon-o-paper-airplane')
                                ->columns(3)
                                ->schema([
                                    TextEntry::make('smtp_server')
                                        ->label(__('Server'))
                                        ->state($serverHostname)
                                        ->copyable(),
                                    TextEntry::make('smtp_port')
                                        ->label(__('Port'))
                                        ->state('465')
                                        ->copyable(),
                                    TextEntry::make('smtp_security')
                                        ->label(__('Security'))
                                        ->state('SSL/TLS')
                                        ->badge()
                                        ->color('success'),
                                ]),
                            Section::make(__('Credentials'))
                                ->description(__('Use your email address and password'))
                                ->icon('heroicon-o-key')
                                ->columns(2)
                                ->schema([
                                    TextEntry::make('username')
                                        ->label(__('Username'))
                                        ->state($record->email)
                                        ->copyable(),
                                    TextEntry::make('password_hint')
                                        ->label(__('Password'))
                                        ->state(__('Your mailbox password')),
                                ]),
                        ];
                    }),
                Action::make('password')
                    ->label(__('Password'))
                    ->icon('heroicon-o-key')
                    ->color('warning')
                    ->modalHeading(__('Change Password'))
                    ->modalDescription(fn (Mailbox $record) => $record->email)
                    ->modalIcon('heroicon-o-key')
                    ->modalIconColor('warning')
                    ->modalSubmitActionLabel(__('Change Password'))
                    ->form([
                        TextInput::make('password')
                            ->label(__('New Password'))
                            ->password()
                            ->revealable()
                            ->required()
                            ->minLength(8)
                            ->rules(fn () => (bool) DnsSetting::get('passphrase_passwords') ? [] : [
                                'regex:/[a-z]/',
                                'regex:/[A-Z]/',
                                'regex:/[0-9]/',
                            ])
                            ->default(fn () => (bool) DnsSetting::get('passphrase_passwords') ? WordList::generate() : $this->generateSecurePassword())
                            ->suffixActions([
                                Action::make('generatePassword')
                                    ->icon('heroicon-o-arrow-path')
                                    ->tooltip(__('Generate secure password'))
                                    ->action(function ($set) {
                                        $password = (bool) DnsSetting::get('passphrase_passwords') ? WordList::generate() : $this->generateSecurePassword();
                                        $set('password', $password);
                                    }),
                                Action::make('copyPassword')
                                    ->icon('heroicon-o-clipboard-document')
                                    ->tooltip(__('Copy to clipboard'))
                                    ->action(function ($state, $livewire) {
                                        if ($state) {
                                            $escaped = addslashes($state);
                                            $livewire->js("navigator.clipboard.writeText('{$escaped}')");
                                            Notification::make()
                                                ->title(__('Copied to clipboard'))
                                                ->success()
                                                ->duration(2000)
                                                ->send();
                                        }
                                    }),
                            ])
                            ->helperText(fn () => (bool) DnsSetting::get('passphrase_passwords')
                                ? __('Password will be generated as easy-to-remember words')
                                : __('Minimum 8 characters with uppercase, lowercase, and numbers')),
                    ])
                    ->action(fn (Mailbox $record, array $data) => $this->changeMailboxPasswordDirect($record, $data['password'])),
                Action::make('toggle')
                    ->label(fn (Mailbox $record) => $record->is_active ? __('Suspend') : __('Enable'))
                    ->icon(fn (Mailbox $record) => $record->is_active ? 'heroicon-o-pause' : 'heroicon-o-play')
                    ->color('gray')
                    ->action(fn (Mailbox $record) => $this->toggleMailbox($record->id)),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Mailbox'))
                    ->modalDescription(fn (Mailbox $record) => __("Delete ':email'? All emails will be lost.", ['email' => $record->email]))
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->modalSubmitActionLabel(__('Delete Mailbox'))
                    ->form([
                        Toggle::make('delete_files')
                            ->label(__('Also delete all email files'))
                            ->default(false)
                            ->helperText(__('Warning: This cannot be undone')),
                    ])
                    ->action(fn (Mailbox $record, array $data) => $this->deleteMailboxDirect($record, $data['delete_files'] ?? false)),
            ])
            ->emptyStateHeading(__('No mailboxes yet'))
            ->emptyStateDescription(__('Enable email for a domain first, then create a mailbox.'))
            ->emptyStateIcon('heroicon-o-envelope')
            ->striped();
    }

    protected function forwardersTable(Table $table): Table
    {
        return $table
            ->query(
                EmailForwarder::query()
                    ->whereHas('emailDomain.domain', fn (Builder $q) => $q->where('user_id', Auth::id()))
                    ->with('emailDomain.domain')
            )
            ->columns([
                TextColumn::make('email')
                    ->label(__('From'))
                    ->icon('heroicon-o-arrow-right')
                    ->iconColor('primary')
                    ->searchable()
                    ->sortable(),
                TextColumn::make('destinations')
                    ->label(__('Forward To'))
                    ->badge()
                    ->separator(',')
                    ->color('gray'),
                TextColumn::make('is_active')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (bool $state) => $state ? __('Active') : __('Disabled'))
                    ->color(fn (bool $state) => $state ? 'success' : 'danger'),
            ])
            ->recordActions([
                Action::make('edit')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil')
                    ->color('info')
                    ->modalHeading(__('Edit Forwarder'))
                    ->modalDescription(fn (EmailForwarder $record) => $record->email)
                    ->modalIcon('heroicon-o-pencil')
                    ->modalIconColor('info')
                    ->modalSubmitActionLabel(__('Save Changes'))
                    ->fillForm(fn (EmailForwarder $record) => [
                        'destinations' => implode(', ', $record->destinations ?? []),
                    ])
                    ->form([
                        TextInput::make('destinations')
                            ->label(__('Forward To'))
                            ->required()
                            ->helperText(__('Comma-separated email addresses')),
                    ])
                    ->action(fn (EmailForwarder $record, array $data) => $this->updateForwarderDirect($record, $data['destinations'])),
                Action::make('toggle')
                    ->label(fn (EmailForwarder $record) => $record->is_active ? __('Disable') : __('Enable'))
                    ->icon(fn (EmailForwarder $record) => $record->is_active ? 'heroicon-o-pause' : 'heroicon-o-play')
                    ->color('gray')
                    ->action(fn (EmailForwarder $record) => $this->toggleForwarder($record->id)),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Forwarder'))
                    ->modalDescription(fn (EmailForwarder $record) => __("Delete forwarder ':email'?", ['email' => $record->email]))
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->modalSubmitActionLabel(__('Delete Forwarder'))
                    ->action(fn (EmailForwarder $record) => $this->deleteForwarderDirect($record)),
            ])
            ->emptyStateHeading(__('No forwarders yet'))
            ->emptyStateDescription(__('Create a forwarder to redirect emails to another address.'))
            ->emptyStateIcon('heroicon-o-arrow-right')
            ->striped();
    }

    protected function autorespondersTable(Table $table): Table
    {
        return $table
            ->query(
                Autoresponder::query()
                    ->whereHas('mailbox.emailDomain.domain', fn (Builder $q) => $q->where('user_id', Auth::id()))
                    ->with('mailbox.emailDomain.domain')
            )
            ->columns([
                TextColumn::make('mailbox.email')
                    ->label(__('Email'))
                    ->icon('heroicon-o-envelope')
                    ->iconColor('primary')
                    ->searchable()
                    ->sortable(),
                TextColumn::make('subject')
                    ->label(__('Subject'))
                    ->limit(30)
                    ->searchable(),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->getStateUsing(function (Autoresponder $record): string {
                        if (! $record->is_active) {
                            return __('Disabled');
                        }
                        if ($record->isCurrentlyActive()) {
                            return __('Active');
                        }
                        if ($record->start_date && now()->lt($record->start_date)) {
                            return __('Scheduled');
                        }

                        return __('Expired');
                    })
                    ->color(function (Autoresponder $record): string {
                        if (! $record->is_active) {
                            return 'gray';
                        }
                        if ($record->isCurrentlyActive()) {
                            return 'success';
                        }
                        if ($record->start_date && now()->lt($record->start_date)) {
                            return 'warning';
                        }

                        return 'danger';
                    }),
                TextColumn::make('start_date')
                    ->label(__('From'))
                    ->date('M d, Y')
                    ->placeholder(__('No start date')),
                TextColumn::make('end_date')
                    ->label(__('Until'))
                    ->date('M d, Y')
                    ->placeholder(__('No end date')),
            ])
            ->recordActions([
                Action::make('edit')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil')
                    ->color('info')
                    ->modalHeading(__('Edit Autoresponder'))
                    ->modalDescription(fn (Autoresponder $record) => $record->mailbox->email)
                    ->modalIcon('heroicon-o-clock')
                    ->modalIconColor('info')
                    ->modalSubmitActionLabel(__('Save Changes'))
                    ->fillForm(fn (Autoresponder $record) => [
                        'subject' => $record->subject,
                        'message' => $record->message,
                        'start_date' => $record->start_date?->format('Y-m-d'),
                        'end_date' => $record->end_date?->format('Y-m-d'),
                        'is_active' => $record->is_active,
                    ])
                    ->form([
                        TextInput::make('subject')
                            ->label(__('Subject'))
                            ->required()
                            ->maxLength(255),
                        Textarea::make('message')
                            ->label(__('Message'))
                            ->required()
                            ->rows(5)
                            ->helperText(__('The automatic reply message')),
                        DatePicker::make('start_date')
                            ->label(__('Start Date'))
                            ->helperText(__('Leave empty to start immediately')),
                        DatePicker::make('end_date')
                            ->label(__('End Date'))
                            ->helperText(__('Leave empty for no end date')),
                        Toggle::make('is_active')
                            ->label(__('Active'))
                            ->default(true),
                    ])
                    ->action(fn (Autoresponder $record, array $data) => $this->updateAutoresponder($record, $data)),
                Action::make('toggle')
                    ->label(fn (Autoresponder $record) => $record->is_active ? __('Disable') : __('Enable'))
                    ->icon(fn (Autoresponder $record) => $record->is_active ? 'heroicon-o-pause' : 'heroicon-o-play')
                    ->color('gray')
                    ->action(fn (Autoresponder $record) => $this->toggleAutoresponder($record)),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Autoresponder'))
                    ->modalDescription(fn (Autoresponder $record) => __("Delete autoresponder for ':email'?", ['email' => $record->mailbox->email]))
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->modalSubmitActionLabel(__('Delete'))
                    ->action(fn (Autoresponder $record) => $this->deleteAutoresponder($record)),
            ])
            ->emptyStateHeading(__('No autoresponders'))
            ->emptyStateDescription(__('Set up vacation messages for your mailboxes.'))
            ->emptyStateIcon('heroicon-o-clock')
            ->striped();
    }

    protected function catchAllTable(Table $table): Table
    {
        return $table
            ->query(
                EmailDomain::query()
                    ->whereHas('domain', fn (Builder $q) => $q->where('user_id', Auth::id()))
                    ->with('domain')
            )
            ->columns([
                TextColumn::make('domain.domain')
                    ->label(__('Domain'))
                    ->icon('heroicon-o-globe-alt')
                    ->iconColor('primary')
                    ->searchable()
                    ->sortable(),
                TextColumn::make('catch_all_enabled')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (bool $state) => $state ? __('Enabled') : __('Disabled'))
                    ->color(fn (bool $state) => $state ? 'success' : 'gray'),
                TextColumn::make('catch_all_address')
                    ->label(__('Forward To'))
                    ->placeholder(__('Not configured'))
                    ->icon('heroicon-o-envelope')
                    ->iconColor('info'),
            ])
            ->recordActions([
                Action::make('configure')
                    ->label(__('Configure'))
                    ->icon('heroicon-o-cog-6-tooth')
                    ->color('info')
                    ->modalHeading(__('Configure Catch-All'))
                    ->modalDescription(fn (EmailDomain $record) => $record->domain->domain)
                    ->modalIcon('heroicon-o-inbox-stack')
                    ->modalIconColor('info')
                    ->modalSubmitActionLabel(__('Save'))
                    ->fillForm(fn (EmailDomain $record) => [
                        'enabled' => $record->catch_all_enabled,
                        'address' => $record->catch_all_address,
                    ])
                    ->form([
                        Toggle::make('enabled')
                            ->label(__('Enable Catch-All'))
                            ->helperText(__('Receive emails sent to any non-existent address on this domain')),
                        Select::make('address')
                            ->label(__('Deliver To'))
                            ->options(function (EmailDomain $record) {
                                return Mailbox::where('email_domain_id', $record->id)
                                    ->pluck('local_part')
                                    ->mapWithKeys(fn ($local) => [
                                        $local.'@'.$record->domain->domain => $local.'@'.$record->domain->domain,
                                    ])
                                    ->toArray();
                            })
                            ->searchable()
                            ->helperText(__('Select a mailbox to receive catch-all emails')),
                    ])
                    ->action(fn (EmailDomain $record, array $data) => $this->updateCatchAll($record, $data)),
            ])
            ->emptyStateHeading(__('No email domains'))
            ->emptyStateDescription(__('Create a mailbox first to enable email for a domain.'))
            ->emptyStateIcon('heroicon-o-inbox-stack')
            ->striped();
    }

    protected function disclaimerTable(Table $table): Table
    {
        return $table
            ->query(
                EmailDomain::query()
                    ->whereHas('domain', fn (Builder $q) => $q->where('user_id', Auth::id()))
                    ->with('domain')
            )
            ->columns([
                TextColumn::make('domain.domain')
                    ->label(__('Domain'))
                    ->icon('heroicon-o-globe-alt')
                    ->iconColor('primary')
                    ->searchable()
                    ->sortable(),
                TextColumn::make('disclaimer_enabled')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (bool $state) => $state ? __('Enabled') : __('Disabled'))
                    ->color(fn (bool $state) => $state ? 'success' : 'gray'),
            ])
            ->recordActions([
                Action::make('configure')
                    ->label(__('Configure'))
                    ->icon('heroicon-o-cog-6-tooth')
                    ->color('info')
                    ->modalHeading(__('Email Disclaimer'))
                    ->modalDescription(fn (EmailDomain $record) => $record->domain->domain)
                    ->modalIcon('heroicon-o-document-text')
                    ->modalIconColor('info')
                    ->modalSubmitActionLabel(__('Save'))
                    ->fillForm(fn (EmailDomain $record) => [
                        'enabled' => $record->disclaimer_enabled,
                        'text' => $record->disclaimer_text ?? __('If you received this email by mistake, please notify the sender and delete it.'),
                    ])
                    ->form([
                        Toggle::make('enabled')
                            ->label(__('Enable Disclaimer'))
                            ->helperText(__('Append a disclaimer to all outbound emails from this domain')),
                        Textarea::make('text')
                            ->label(__('Disclaimer Text'))
                            ->rows(4)
                            ->helperText(__('This text will be appended to every outgoing email'))
                            ->required(),
                    ])
                    ->action(fn (EmailDomain $record, array $data) => $this->updateDisclaimer($record, $data)),
            ])
            ->emptyStateHeading(__('No email domains'))
            ->emptyStateDescription(__('Create a mailbox first to enable email for a domain.'))
            ->emptyStateIcon('heroicon-o-document-text')
            ->striped();
    }

    protected function emailLogsTable(Table $table): Table
    {
        // Read mail logs (last 100 entries)
        $logs = $this->getEmailLogs();

        return $table
            ->records(fn () => $logs)
            ->columns([
                TextColumn::make('timestamp')
                    ->label(__('Time'))
                    ->dateTime('M d, H:i:s')
                    ->sortable(),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->color(fn (array $record) => match ($record['status'] ?? '') {
                        'sent', 'delivered' => 'success',
                        'deferred' => 'warning',
                        'bounced', 'rejected', 'failed' => 'danger',
                        default => 'gray',
                    }),
                TextColumn::make('from')
                    ->label(__('From'))
                    ->limit(30)
                    ->searchable(),
                TextColumn::make('to')
                    ->label(__('To'))
                    ->limit(30)
                    ->searchable(),
                TextColumn::make('subject')
                    ->label(__('Subject'))
                    ->limit(40)
                    ->placeholder(__('(no subject)')),
            ])
            ->recordActions([
                Action::make('details')
                    ->label(__('Details'))
                    ->icon('heroicon-o-information-circle')
                    ->color('gray')
                    ->modalHeading(__('Email Details'))
                    ->modalSubmitAction(false)
                    ->modalCancelActionLabel(__('Close'))
                    ->infolist(fn (array $record): array => [
                        Section::make(__('Message Info'))
                            ->columns(2)
                            ->schema([
                                TextEntry::make('from')
                                    ->label(__('From'))
                                    ->state($record['from'] ?? '-')
                                    ->copyable(),
                                TextEntry::make('to')
                                    ->label(__('To'))
                                    ->state($record['to'] ?? '-')
                                    ->copyable(),
                                TextEntry::make('subject')
                                    ->label(__('Subject'))
                                    ->state($record['subject'] ?? '-'),
                                TextEntry::make('timestamp')
                                    ->label(__('Time'))
                                    ->state(isset($record['timestamp']) ? date('Y-m-d H:i:s', $record['timestamp']) : '-'),
                            ]),
                        Section::make(__('Delivery Status'))
                            ->schema([
                                TextEntry::make('status')
                                    ->label(__('Status'))
                                    ->state($record['status'] ?? '-')
                                    ->badge()
                                    ->color(match ($record['status'] ?? '') {
                                        'sent', 'delivered' => 'success',
                                        'deferred' => 'warning',
                                        'bounced', 'rejected', 'failed' => 'danger',
                                        default => 'gray',
                                    }),
                                TextEntry::make('message')
                                    ->label(__('Message'))
                                    ->state($record['message'] ?? '-'),
                            ]),
                    ]),
            ])
            ->emptyStateHeading(__('No email logs'))
            ->emptyStateDescription(__('Email activity will appear here once emails are sent or received.'))
            ->emptyStateIcon('heroicon-o-document-text')
            ->striped()
            ->defaultSort('timestamp', 'desc');
    }

    protected function getHeaderActions(): array
    {
        return [
            $this->createMailboxAction(),
            $this->createForwarderAction(),
            $this->createAutoresponderAction(),
            $this->createShareAction(),
            $this->showCredentialsAction(),
        ];
    }

    protected function showCredentialsAction(): Action
    {
        return Action::make('showCredentials')
            ->label(__('Credentials'))
            ->hidden()
            ->modalHeading(__('Mailbox Credentials'))
            ->modalDescription(__('Save these credentials! The password won\'t be shown again.'))
            ->modalIcon('heroicon-o-check-circle')
            ->modalIconColor('success')
            ->modalSubmitAction(false)
            ->modalCancelActionLabel(__('Done'))
            ->infolist([
                Section::make(__('Email Address'))
                    ->schema([
                        TextEntry::make('email')
                            ->hiddenLabel()
                            ->state(fn () => $this->credEmail)
                            ->copyable()
                            ->fontFamily('mono'),
                    ]),
                Section::make(__('Password'))
                    ->schema([
                        TextEntry::make('password')
                            ->hiddenLabel()
                            ->state(fn () => $this->credPassword)
                            ->copyable()
                            ->fontFamily('mono'),
                    ]),
            ]);
    }

    protected function getOrCreateEmailDomain(Domain $domain): EmailDomain
    {
        $emailDomain = $domain->emailDomain;

        if (! $emailDomain) {
            // Enable email for this domain on the server
            $this->getAgent()->emailEnableDomain($this->getUsername(), $domain->domain);

            // Create EmailDomain record
            $emailDomain = EmailDomain::create([
                'domain_id' => $domain->id,
                'is_active' => true,
            ]);

            $this->syncMailRouting();

            // Generate DKIM
            try {
                $dkimResult = $this->getAgent()->emailGenerateDkim($this->getUsername(), $domain->domain);
                if (isset($dkimResult['public_key'])) {
                    $selector = $dkimResult['selector'] ?? 'default';
                    $publicKey = $dkimResult['public_key'];

                    $emailDomain->update([
                        'dkim_selector' => $selector,
                        'dkim_public_key' => $publicKey,
                        'dkim_private_key' => $dkimResult['private_key'] ?? null,
                    ]);

                    // Add DKIM record to DNS
                    $dkimRecord = DnsRecord::where('domain_id', $domain->id)
                        ->where('name', "{$selector}._domainkey")
                        ->where('type', 'TXT')
                        ->first();

                    // Format the DKIM public key (remove headers and newlines)
                    $cleanKey = str_replace([
                        '-----BEGIN PUBLIC KEY-----',
                        '-----END PUBLIC KEY-----',
                        "\n",
                        "\r",
                    ], '', $publicKey);

                    $dkimContent = "v=DKIM1; k=rsa; p={$cleanKey}";

                    if (! $dkimRecord) {
                        DnsRecord::create([
                            'domain_id' => $domain->id,
                            'name' => "{$selector}._domainkey",
                            'type' => 'TXT',
                            'content' => $dkimContent,
                            'ttl' => 3600,
                        ]);
                    } else {
                        $dkimRecord->update(['content' => $dkimContent]);
                    }

                    // Regenerate DNS zone to include the new DKIM record
                    $this->regenerateDnsZone($domain);
                }
            } catch (Exception $e) {
                // DKIM generation failed, but email can still work
            }

            // Create autoconfig/autodiscover A records and SRV records
            $serverIp = ServerFacts::serverIp('127.0.0.1');
            $defaultTtl = 3600;

            $autoconfigRecords = [
                ['name' => 'autoconfig', 'type' => 'A', 'content' => $serverIp, 'ttl' => $defaultTtl],
                ['name' => 'autodiscover', 'type' => 'A', 'content' => $serverIp, 'ttl' => $defaultTtl],
                ['name' => '_imaps._tcp', 'type' => 'SRV', 'content' => "1 993 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0],
                ['name' => '_pop3s._tcp', 'type' => 'SRV', 'content' => "1 995 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0],
                ['name' => '_submission._tcp', 'type' => 'SRV', 'content' => "1 587 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0],
            ];

            foreach ($autoconfigRecords as $record) {
                DnsRecord::firstOrCreate(
                    [
                        'domain_id' => $domain->id,
                        'name' => $record['name'],
                        'type' => $record['type'],
                    ],
                    $record
                );
            }

            $this->regenerateDnsZone($domain);

            // Issue mail SSL certificate in the background
            \App\Jobs\IssueSslCertificate::dispatch($domain->id, 'mail')->delay(now()->addSeconds(120));
        }

        return $emailDomain;
    }

    protected function syncMailRouting(): void
    {
        try {
            app(MailRoutingSyncService::class)->sync();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Mail routing sync failed'))
                ->body($e->getMessage())
                ->warning()
                ->send();
        }
    }

    protected function regenerateDnsZone(Domain $domain): void
    {
        try {
            $records = DnsRecord::where('domain_id', $domain->id)->get()->toArray();
            $settings = \App\Models\DnsSetting::getAll();
            $hostname = gethostname() ?: 'localhost';
            $serverIp = ServerFacts::serverIp('127.0.0.1');
            $serverIpv6 = $settings['default_ipv6'] ?? null;

            $this->getAgent()->send('dns.sync_zone', [
                'domain' => $domain->domain,
                'records' => $records,
                'ns1' => $settings['ns1'] ?? "ns1.{$hostname}",
                'ns2' => $settings['ns2'] ?? "ns2.{$hostname}",
                'admin_email' => $settings['admin_email'] ?? "admin.{$hostname}",
                'default_ip' => $settings['default_ip'] ?? $serverIp,
                'default_ipv6' => $serverIpv6,
                'default_ttl' => $settings['default_ttl'] ?? 3600,
            ]);
        } catch (Exception $e) {
            // Log but don't fail - DNS zone regeneration is not critical
        }
    }

    // Mailbox Actions
    protected function createMailboxAction(): Action
    {
        return Action::make('createMailbox')
            ->label(__('New Mailbox'))
            ->icon('heroicon-o-plus-circle')
            ->color('success')
            ->visible(fn () => Domain::where('user_id', Auth::id())->exists())
            ->modalHeading(__('Create New Mailbox'))
            ->modalDescription(__('Create an email account for one of your domains'))
            ->modalIcon('heroicon-o-envelope')
            ->modalIconColor('success')
            ->modalSubmitActionLabel(__('Create Mailbox'))
            ->form([
                Select::make('domain_id')
                    ->label(__('Domain'))
                    ->options(fn () => Domain::where('user_id', Auth::id())->pluck('domain', 'id')->toArray())
                    ->required()
                    ->searchable()
                    ->live()
                    ->live(),
                TextInput::make('local_part')
                    ->label(__('Email Address'))
                    ->required(fn (Get $get): bool => filled($get('domain_id')))
                    ->visible(fn (Get $get): bool => filled($get('domain_id')))
                    ->regex('/^[a-zA-Z0-9._%+-]+$/')
                    ->maxLength(64)
                    ->helperText(__('The part before the @ symbol')),
                TextInput::make('name')
                    ->label(__('Display Name'))
                    ->visible(fn (Get $get): bool => filled($get('domain_id')))
                    ->maxLength(255),
                TextInput::make('password')
                    ->label(__('Password'))
                    ->password()
                    ->revealable()
                    ->required(fn (Get $get): bool => filled($get('domain_id')))
                    ->visible(fn (Get $get): bool => filled($get('domain_id')))
                    ->minLength(8)
                    ->rules(fn () => (bool) DnsSetting::get('passphrase_passwords') ? [] : [
                        'regex:/[a-z]/',
                        'regex:/[A-Z]/',
                        'regex:/[0-9]/',
                    ])
                    ->default(fn () => (bool) DnsSetting::get('passphrase_passwords') ? WordList::generate() : $this->generateSecurePassword())
                    ->suffixActions([
                        Action::make('generatePassword')
                            ->icon('heroicon-o-arrow-path')
                            ->tooltip(__('Generate secure password'))
                            ->action(function ($set) {
                                $password = (bool) DnsSetting::get('passphrase_passwords') ? WordList::generate() : $this->generateSecurePassword();
                                $set('password', $password);
                            }),
                        Action::make('copyPassword')
                            ->icon('heroicon-o-clipboard-document')
                            ->tooltip(__('Copy to clipboard'))
                            ->action(function ($state, $livewire) {
                                if ($state) {
                                    $escaped = addslashes($state);
                                    $livewire->js("navigator.clipboard.writeText('{$escaped}')");
                                    Notification::make()
                                        ->title(__('Copied to clipboard'))
                                        ->success()
                                        ->duration(2000)
                                        ->send();
                                }
                            }),
                    ])
                    ->helperText(fn () => (bool) DnsSetting::get('passphrase_passwords')
                        ? __('Password will be generated as easy-to-remember words')
                        : __('Minimum 8 characters with uppercase, lowercase, and numbers'))
                    ->helperText(__('Minimum 8 characters with uppercase, lowercase, and numbers')),
                TextInput::make('quota_mb')
                    ->label(__('Quota (MB)'))
                    ->numeric()
                    ->visible(fn (Get $get): bool => filled($get('domain_id')))
                    ->default(1024)
                    ->minValue(100)
                    ->maxValue(10240)
                    ->helperText(__('Storage limit in megabytes')),
            ])
            ->action(function (array $data): void {
                $limit = Auth::user()?->hostingPackage?->mailboxes_limit;
                if ($limit && Mailbox::where('user_id', Auth::id())->count() >= $limit) {
                    Notification::make()
                        ->title(__('Mailbox limit reached'))
                        ->body(__('Your hosting package allows up to :limit mailboxes.', ['limit' => $limit]))
                        ->warning()
                        ->send();

                    return;
                }

                $domain = Domain::where('user_id', Auth::id())->find($data['domain_id']);
                if (! $domain) {
                    Notification::make()->title(__('Domain not found'))->danger()->send();

                    return;
                }

                try {
                    // Get or create EmailDomain (enables email on server if needed)
                    $emailDomain = $this->getOrCreateEmailDomain($domain);

                    $email = $data['local_part'].'@'.$domain->domain;
                    $quotaBytes = (int) $data['quota_mb'] * 1024 * 1024;

                    if (Mailbox::where('email_domain_id', $emailDomain->id)->where('local_part', $data['local_part'])->exists()) {
                        Notification::make()->title(__('Mailbox already exists'))->danger()->send();

                        return;
                    }

                    $result = $this->getAgent()->mailboxCreate(
                        $this->getUsername(),
                        $email,
                        $data['password'],
                        $quotaBytes
                    );

                    Mailbox::create([
                        'email_domain_id' => $emailDomain->id,
                        'user_id' => Auth::id(),
                        'local_part' => $data['local_part'],
                        'password_hash' => $result['password_hash'] ?? '',
                        'password_encrypted' => Crypt::encryptString($data['password']),
                        'maildir_path' => $result['maildir_path'] ?? null,
                        'system_uid' => $result['uid'] ?? null,
                        'system_gid' => $result['gid'] ?? null,
                        'name' => $data['name'],
                        'quota_bytes' => $quotaBytes,
                        'is_active' => true,
                    ]);

                    AuditLog::logEmailAction('created', $email, [
                        'domain' => $domain->domain,
                        'quota_bytes' => $quotaBytes,
                    ]);

                    $this->syncMailRouting();

                    $this->credEmail = $email;
                    $this->credPassword = $data['password'];

                    Notification::make()->title(__('Mailbox created'))->success()->send();

                    $this->mountAction('showCredentials');
                } catch (Exception $e) {
                    Notification::make()->title(__('Error creating mailbox'))->body($e->getMessage())->danger()->send();
                }
            });
    }

    public function changeMailboxPasswordDirect(Mailbox $mailbox, string $password): void
    {
        try {
            $result = $this->getAgent()->mailboxChangePassword(
                $this->getUsername(),
                $mailbox->email,
                $password
            );

            $mailbox->update([
                'password_hash' => $result['password_hash'] ?? '',
                'password_encrypted' => Crypt::encryptString($password),
            ]);

            $this->credEmail = $mailbox->email;
            $this->credPassword = $password;

            AuditLog::logEmailAction('password_changed', $mailbox->email);

            Notification::make()->title(__('Password changed'))->success()->send();

            $this->mountAction('showCredentials');
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    public function toggleMailbox(int $mailboxId): void
    {
        $mailbox = Mailbox::with('emailDomain.domain')->find($mailboxId);
        if (! $mailbox) {
            Notification::make()->title(__('Mailbox not found'))->danger()->send();

            return;
        }

        try {
            $newStatus = ! $mailbox->is_active;
            $this->getAgent()->mailboxToggle($this->getUsername(), $mailbox->email, $newStatus);
            $mailbox->update(['is_active' => $newStatus]);

            $this->syncMailRouting();

            AuditLog::logEmailAction($newStatus ? 'enabled' : 'disabled', $mailbox->email);

            Notification::make()
                ->title($newStatus ? __('Mailbox enabled') : __('Mailbox disabled'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    public function deleteMailboxDirect(Mailbox $mailbox, bool $deleteFiles): void
    {
        try {
            $this->getAgent()->mailboxDelete(
                $this->getUsername(),
                $mailbox->email,
                $deleteFiles,
                $mailbox->maildir_path
            );

            $mailbox->delete();

            $this->syncMailRouting();

            AuditLog::logEmailAction('deleted', $mailbox->email, [
                'delete_files' => $deleteFiles,
            ]);

            Notification::make()->title(__('Mailbox deleted'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    // Forwarder Actions
    protected function createForwarderAction(): Action
    {
        return Action::make('createForwarder')
            ->label(__('New Forwarder'))
            ->icon('heroicon-o-arrow-right-circle')
            ->color('info')
            ->visible(fn () => Domain::where('user_id', Auth::id())->exists())
            ->modalHeading(__('Create New Forwarder'))
            ->modalDescription(__('Redirect emails from one address to another'))
            ->modalIcon('heroicon-o-arrow-right')
            ->modalIconColor('info')
            ->modalSubmitActionLabel(__('Create Forwarder'))
            ->form([
                Select::make('domain_id')
                    ->label(__('Domain'))
                    ->options(fn () => Domain::where('user_id', Auth::id())->pluck('domain', 'id')->toArray())
                    ->required()
                    ->searchable()
                    ->live(),
                TextInput::make('local_part')
                    ->label(__('Email Address'))
                    ->required(fn (Get $get): bool => filled($get('domain_id')))
                    ->visible(fn (Get $get): bool => filled($get('domain_id')))
                    ->regex('/^[a-zA-Z0-9._%+-]+$/')
                    ->maxLength(64)
                    ->helperText(__('The part before the @ symbol')),
                TextInput::make('destinations')
                    ->label(__('Forward To'))
                    ->required(fn (Get $get): bool => filled($get('domain_id')))
                    ->visible(fn (Get $get): bool => filled($get('domain_id')))
                    ->helperText(__('Comma-separated email addresses to forward to')),
            ])
            ->action(function (array $data): void {
                $domain = Domain::where('user_id', Auth::id())->find($data['domain_id']);
                if (! $domain) {
                    Notification::make()->title(__('Domain not found'))->danger()->send();

                    return;
                }

                $destinations = array_map('trim', explode(',', $data['destinations']));
                $destinations = array_filter($destinations, fn ($d) => filter_var($d, FILTER_VALIDATE_EMAIL));

                if (empty($destinations)) {
                    Notification::make()->title(__('Invalid destination emails'))->danger()->send();

                    return;
                }

                try {
                    // Get or create EmailDomain (enables email on server if needed)
                    $emailDomain = $this->getOrCreateEmailDomain($domain);

                    $email = $data['local_part'].'@'.$domain->domain;

                    if (EmailForwarder::where('email_domain_id', $emailDomain->id)->where('local_part', $data['local_part'])->exists()) {
                        Notification::make()->title(__('Forwarder already exists'))->danger()->send();

                        return;
                    }

                    if (Mailbox::where('email_domain_id', $emailDomain->id)->where('local_part', $data['local_part'])->exists()) {
                        Notification::make()->title(__('A mailbox with this address already exists'))->danger()->send();

                        return;
                    }

                    $this->getAgent()->send('email.forwarder_create', [
                        'username' => $this->getUsername(),
                        'email' => $email,
                        'destinations' => $destinations,
                    ]);

                    EmailForwarder::create([
                        'email_domain_id' => $emailDomain->id,
                        'user_id' => Auth::id(),
                        'local_part' => $data['local_part'],
                        'destinations' => $destinations,
                        'is_active' => true,
                    ]);

                    // Sync Roundcube identities for local mailboxes in destinations
                    $this->syncRoundcubeIdentitiesForForwarder($email, $destinations);

                    $this->syncMailRouting();

                    Notification::make()->title(__('Forwarder created'))->success()->send();
                } catch (Exception $e) {
                    Notification::make()->title(__('Error creating forwarder'))->body($e->getMessage())->danger()->send();
                }
            });
    }

    public function updateForwarderDirect(EmailForwarder $forwarder, string $destinationsString): void
    {
        $destinations = array_map('trim', explode(',', $destinationsString));
        $destinations = array_filter($destinations, fn ($d) => filter_var($d, FILTER_VALIDATE_EMAIL));

        if (empty($destinations)) {
            Notification::make()->title(__('Invalid destination emails'))->danger()->send();

            return;
        }

        try {
            $oldDestinations = $forwarder->destinations;

            $this->getAgent()->send('email.forwarder_update', [
                'username' => $this->getUsername(),
                'email' => $forwarder->email,
                'destinations' => $destinations,
            ]);

            $forwarder->update(['destinations' => $destinations]);

            // Update Roundcube identities: remove old, add new
            $this->removeRoundcubeIdentitiesForForwarder($forwarder->email, $oldDestinations);
            $this->syncRoundcubeIdentitiesForForwarder($forwarder->email, $destinations);

            $this->syncMailRouting();

            Notification::make()->title(__('Forwarder updated'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    public function toggleForwarder(int $forwarderId): void
    {
        $forwarder = EmailForwarder::with('emailDomain.domain')->find($forwarderId);
        if (! $forwarder) {
            Notification::make()->title(__('Forwarder not found'))->danger()->send();

            return;
        }

        try {
            $newStatus = ! $forwarder->is_active;
            $this->getAgent()->send('email.forwarder_toggle', [
                'username' => $this->getUsername(),
                'email' => $forwarder->email,
                'active' => $newStatus,
            ]);
            $forwarder->update(['is_active' => $newStatus]);

            $this->syncMailRouting();

            Notification::make()
                ->title($newStatus ? __('Forwarder enabled') : __('Forwarder disabled'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    public function deleteForwarderDirect(EmailForwarder $forwarder): void
    {
        try {
            $forwarderEmail = $forwarder->email;
            $forwarderDestinations = $forwarder->destinations;

            $this->getAgent()->send('email.forwarder_delete', [
                'username' => $this->getUsername(),
                'email' => $forwarderEmail,
            ]);

            $forwarder->delete();

            // Remove Roundcube identities for this forwarder
            $this->removeRoundcubeIdentitiesForForwarder($forwarderEmail, $forwarderDestinations);

            $this->syncMailRouting();

            Notification::make()->title(__('Forwarder deleted'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    /**
     * Sync Roundcube identities when a forwarder is created.
     * For each destination that is a local mailbox, add the forwarder address as an identity.
     */
    protected function syncRoundcubeIdentitiesForForwarder(string $forwarderEmail, array $destinations): void
    {
        try {
            $service = new RoundcubeIdentityService;

            foreach ($destinations as $destination) {
                // Check if destination is a local mailbox
                $mailbox = $this->findLocalMailbox($destination);
                if ($mailbox) {
                    $service->addIdentity($destination, $forwarderEmail, $mailbox->name ?? '');
                }
            }
        } catch (Exception $e) {
            Log::warning("Failed to sync Roundcube identities: {$e->getMessage()}");
        }
    }

    /**
     * Remove Roundcube identities when a forwarder is deleted.
     */
    protected function removeRoundcubeIdentitiesForForwarder(string $forwarderEmail, array $destinations): void
    {
        try {
            $service = new RoundcubeIdentityService;

            foreach ($destinations as $destination) {
                $mailbox = $this->findLocalMailbox($destination);
                if ($mailbox) {
                    $service->removeIdentity($destination, $forwarderEmail);
                }
            }
        } catch (Exception $e) {
            Log::warning("Failed to remove Roundcube identities: {$e->getMessage()}");
        }
    }

    /**
     * Find a local mailbox by email address.
     */
    protected function findLocalMailbox(string $email): ?Mailbox
    {
        $parts = explode('@', $email);
        if (count($parts) !== 2) {
            return null;
        }

        [$localPart, $domainName] = $parts;

        return Mailbox::whereHas('emailDomain.domain', function ($q) use ($domainName) {
            $q->where('domain', $domainName)->where('user_id', Auth::id());
        })->where('local_part', $localPart)->first();
    }

    // Autoresponder Actions
    protected function createAutoresponderAction(): Action
    {
        return Action::make('createAutoresponder')
            ->label(__('New Autoresponder'))
            ->icon('heroicon-o-clock')
            ->color('warning')
            ->visible(fn () => Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))->exists())
            ->modalHeading(__('Create Autoresponder'))
            ->modalDescription(__('Set up an automatic vacation reply'))
            ->modalIcon('heroicon-o-clock')
            ->modalIconColor('warning')
            ->modalSubmitActionLabel(__('Create'))
            ->form([
                Select::make('mailbox_id')
                    ->label(__('Mailbox'))
                    ->options(fn () => Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))
                        ->with('emailDomain.domain')
                        ->get()
                        ->mapWithKeys(fn ($m) => [$m->id => $m->email])
                        ->toArray())
                    ->required()
                    ->searchable(),
                TextInput::make('subject')
                    ->label(__('Subject'))
                    ->required()
                    ->default(__('Out of Office'))
                    ->maxLength(255),
                Textarea::make('message')
                    ->label(__('Message'))
                    ->required()
                    ->rows(5)
                    ->default(__("Thank you for your email. I am currently out of the office and will respond to your message upon my return.\n\nBest regards"))
                    ->helperText(__('The automatic reply message')),
                DatePicker::make('start_date')
                    ->label(__('Start Date'))
                    ->helperText(__('Leave empty to start immediately')),
                DatePicker::make('end_date')
                    ->label(__('End Date'))
                    ->helperText(__('Leave empty for no end date')),
            ])
            ->action(function (array $data): void {
                $mailbox = Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))
                    ->find($data['mailbox_id']);

                if (! $mailbox) {
                    Notification::make()->title(__('Mailbox not found'))->danger()->send();

                    return;
                }

                // Check if autoresponder already exists for this mailbox
                if (Autoresponder::where('mailbox_id', $mailbox->id)->exists()) {
                    Notification::make()
                        ->title(__('Autoresponder already exists'))
                        ->body(__('Edit the existing autoresponder instead.'))
                        ->danger()
                        ->send();

                    return;
                }

                try {
                    // Create autoresponder in database
                    $autoresponder = Autoresponder::create([
                        'mailbox_id' => $mailbox->id,
                        'subject' => $data['subject'],
                        'message' => $data['message'],
                        'start_date' => $data['start_date'] ?? null,
                        'end_date' => $data['end_date'] ?? null,
                        'is_active' => true,
                    ]);

                    // Configure on mail server via agent
                    $this->getAgent()->send('email.autoresponder_set', [
                        'username' => $this->getUsername(),
                        'email' => $mailbox->email,
                        'subject' => $data['subject'],
                        'message' => $data['message'],
                        'start_date' => $data['start_date'] ?? null,
                        'end_date' => $data['end_date'] ?? null,
                        'active' => true,
                    ]);

                    Notification::make()->title(__('Autoresponder created'))->success()->send();

                    $this->setTab('autoresponders');
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
                }
            });
    }

    public function updateAutoresponder(Autoresponder $autoresponder, array $data): void
    {
        try {
            $autoresponder->update([
                'subject' => $data['subject'],
                'message' => $data['message'],
                'start_date' => $data['start_date'] ?? null,
                'end_date' => $data['end_date'] ?? null,
                'is_active' => $data['is_active'] ?? true,
            ]);

            // Update on mail server
            $this->getAgent()->send('email.autoresponder_set', [
                'username' => $this->getUsername(),
                'email' => $autoresponder->mailbox->email,
                'subject' => $data['subject'],
                'message' => $data['message'],
                'start_date' => $data['start_date'] ?? null,
                'end_date' => $data['end_date'] ?? null,
                'active' => $data['is_active'] ?? true,
            ]);

            Notification::make()->title(__('Autoresponder updated'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    public function toggleAutoresponder(Autoresponder $autoresponder): void
    {
        try {
            $newStatus = ! $autoresponder->is_active;
            $autoresponder->update(['is_active' => $newStatus]);

            // Update on mail server
            $this->getAgent()->send('email.autoresponder_toggle', [
                'username' => $this->getUsername(),
                'email' => $autoresponder->mailbox->email,
                'active' => $newStatus,
            ]);

            Notification::make()
                ->title($newStatus ? __('Autoresponder enabled') : __('Autoresponder disabled'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    public function deleteAutoresponder(Autoresponder $autoresponder): void
    {
        try {
            // Remove from mail server
            $this->getAgent()->send('email.autoresponder_delete', [
                'username' => $this->getUsername(),
                'email' => $autoresponder->mailbox->email,
            ]);

            $autoresponder->delete();

            Notification::make()->title(__('Autoresponder deleted'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    protected function sharingTable(Table $table): Table
    {
        return $table
            ->query(
                MailboxShare::query()
                    ->whereHas('mailbox.emailDomain.domain', fn (Builder $q) => $q->where('user_id', Auth::id()))
                    ->with(['mailbox.emailDomain.domain', 'sharedWith.emailDomain.domain'])
            )
            ->columns([
                TextColumn::make('mailbox.email')
                    ->label(__('Owner Mailbox'))
                    ->icon('heroicon-o-envelope')
                    ->iconColor('primary')
                    ->searchable()
                    ->sortable(),
                TextColumn::make('sharedWith.email')
                    ->label(__('Share With'))
                    ->icon('heroicon-o-user')
                    ->iconColor('info')
                    ->searchable()
                    ->sortable(),
                TextColumn::make('folder')
                    ->label(__('Folder'))
                    ->searchable(),
                TextColumn::make('permission_level')
                    ->label(__('Permission Level'))
                    ->badge()
                    ->color(fn (MailboxShare $record): string => match ($record->permission_level) {
                        'Read' => 'gray',
                        'Read & Write' => 'info',
                        'Full Access' => 'warning',
                        'Admin' => 'danger',
                        default => 'gray',
                    }),
            ])
            ->recordActions([
                Action::make('editPermissions')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil')
                    ->color('info')
                    ->modalHeading(__('Permission Level'))
                    ->modalSubmitActionLabel(__('Save Changes'))
                    ->fillForm(fn (MailboxShare $record) => [
                        'acl_rights' => $record->acl_rights,
                    ])
                    ->form([
                        Select::make('acl_rights')
                            ->label(__('Permission Level'))
                            ->options([
                                'lrs' => __('Read'),
                                'lrswite' => __('Read & Write'),
                                'lrswitekx' => __('Full Access'),
                                'lrswitekxa' => __('Admin'),
                            ])
                            ->required(),
                    ])
                    ->action(function (MailboxShare $record, array $data): void {
                        // Verify ownership
                        if ($record->mailbox->emailDomain->domain->user_id !== Auth::id()) {
                            Notification::make()->title(__('Unauthorized'))->danger()->send();

                            return;
                        }

                        try {
                            $service = new MailboxSharingService($this->getAgent());
                            $service->updatePermissions($record, $data['acl_rights']);
                            Notification::make()->title(__('Permissions updated'))->success()->send();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
                        }
                    }),
                Action::make('revoke')
                    ->label(__('Revoke Share'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Revoke Share'))
                    ->modalDescription(fn (MailboxShare $record) => __("Revoke ':email' access to :folder?", [
                        'email' => $record->sharedWith->email,
                        'folder' => $record->folder,
                    ]))
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->modalSubmitActionLabel(__('Revoke Share'))
                    ->action(function (MailboxShare $record): void {
                        // Verify ownership
                        if ($record->mailbox->emailDomain->domain->user_id !== Auth::id()) {
                            Notification::make()->title(__('Unauthorized'))->danger()->send();

                            return;
                        }

                        try {
                            $service = new MailboxSharingService($this->getAgent());
                            $service->revokeShare($record);
                            Notification::make()->title(__('Share revoked'))->success()->send();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
                        }
                    }),
            ])
            ->emptyStateHeading(__('No shared folders'))
            ->emptyStateDescription(__('Share mailbox folders with other users in the same domain.'))
            ->emptyStateIcon('heroicon-o-share')
            ->striped();
    }

    protected function createShareAction(): Action
    {
        return Action::make('createShare')
            ->label(__('Share Folder'))
            ->icon('heroicon-o-share')
            ->color('success')
            ->visible(fn () => $this->activeTab === 'sharing' && Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))->exists())
            ->modalHeading(__('Share Folder'))
            ->modalDescription(__('Select a folder to share'))
            ->modalIcon('heroicon-o-share')
            ->modalIconColor('success')
            ->modalSubmitActionLabel(__('Share Folder'))
            ->form([
                Select::make('mailbox_id')
                    ->label(__('Owner Mailbox'))
                    ->options(fn () => Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))
                        ->with('emailDomain.domain')
                        ->get()
                        ->mapWithKeys(fn ($m) => [$m->id => $m->email])
                        ->toArray())
                    ->required()
                    ->searchable()
                    ->live(),
                Select::make('shared_with_mailbox_id')
                    ->label(__('Share With'))
                    ->options(function (Get $get) {
                        $mailboxId = $get('mailbox_id');
                        if (! $mailboxId) {
                            return [];
                        }
                        $mailbox = Mailbox::find($mailboxId);
                        if (! $mailbox) {
                            return [];
                        }

                        return Mailbox::where('email_domain_id', $mailbox->email_domain_id)
                            ->where('id', '!=', $mailboxId)
                            ->with('emailDomain.domain')
                            ->get()
                            ->mapWithKeys(fn ($m) => [$m->id => $m->email])
                            ->toArray();
                    })
                    ->required()
                    ->searchable(),
                TextInput::make('folder')
                    ->label(__('Folder'))
                    ->default('INBOX')
                    ->required()
                    ->regex('/^(INBOX|[A-Za-z0-9._-]+)$/'),
                Select::make('acl_rights')
                    ->label(__('Permission Level'))
                    ->options([
                        'lrs' => __('Read'),
                        'lrswite' => __('Read & Write'),
                        'lrswitekx' => __('Full Access'),
                        'lrswitekxa' => __('Admin'),
                    ])
                    ->default('lrs')
                    ->required(),
            ])
            ->action(function (array $data): void {
                $owner = Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))
                    ->find($data['mailbox_id']);

                if (! $owner) {
                    Notification::make()->title(__('Mailbox not found'))->danger()->send();

                    return;
                }

                $recipient = Mailbox::where('email_domain_id', $owner->email_domain_id)
                    ->find($data['shared_with_mailbox_id']);

                if (! $recipient) {
                    Notification::make()->title(__('Mailbox not found'))->danger()->send();

                    return;
                }

                try {
                    $service = new MailboxSharingService($this->getAgent());
                    $service->shareFolder($owner, $recipient, $data['folder'], $data['acl_rights']);
                    Notification::make()->title(__('Folder shared successfully'))->success()->send();
                    $this->setTab('sharing');
                } catch (\InvalidArgumentException $e) {
                    Notification::make()->title(__('Error'))->body(__($e->getMessage()))->danger()->send();
                } catch (Exception $e) {
                    Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
                }
            });
    }

    // Helper methods for counts
    public function getMailboxesCount(): int
    {
        return Mailbox::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))->count();
    }

    public function getForwardersCount(): int
    {
        return EmailForwarder::whereHas('emailDomain.domain', fn ($q) => $q->where('user_id', Auth::id()))->count();
    }

    public function getCatchAllCount(): int
    {
        return EmailDomain::whereHas('domain', fn ($q) => $q->where('user_id', Auth::id()))->count();
    }

    // Catch-all methods
    public function updateCatchAll(EmailDomain $emailDomain, array $data): void
    {
        try {
            $enabled = $data['enabled'] ?? false;
            $address = $data['address'] ?? null;

            if ($enabled && empty($address)) {
                Notification::make()
                    ->title(__('Error'))
                    ->body(__('Please select a mailbox to receive catch-all emails'))
                    ->danger()
                    ->send();

                return;
            }

            // Update in Postfix virtual alias maps
            $this->getAgent()->send('email.catchall_update', [
                'username' => $this->getUsername(),
                'domain' => $emailDomain->domain->domain,
                'enabled' => $enabled,
                'address' => $address,
            ]);

            $emailDomain->update([
                'catch_all_enabled' => $enabled,
                'catch_all_address' => $enabled ? $address : null,
            ]);

            $this->syncMailRouting();

            Notification::make()
                ->title($enabled ? __('Catch-all enabled') : __('Catch-all disabled'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    public function updateDisclaimer(EmailDomain $emailDomain, array $data): void
    {
        try {
            $enabled = $data['enabled'] ?? false;
            $text = $data['text'] ?? '';

            $this->getAgent()->send('email.disclaimer_update', [
                'domain' => $emailDomain->domain->domain,
                'enabled' => $enabled,
                'text' => $text,
            ]);

            $emailDomain->update([
                'disclaimer_enabled' => $enabled,
                'disclaimer_text' => $text,
            ]);

            Notification::make()
                ->title($enabled ? __('Disclaimer enabled') : __('Disclaimer disabled'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body($e->getMessage())->danger()->send();
        }
    }

    // Email Logs
    public function getEmailLogs(): array
    {
        try {
            $result = $this->getAgent()->send('email.get_logs', [
                'username' => $this->getUsername(),
                'limit' => 100,
            ]);

            return $result['logs'] ?? [];
        } catch (Exception $e) {
            return [];
        }
    }

    // Email Usage Stats
    public function getEmailUsageStats(): array
    {
        $domains = EmailDomain::whereHas('domain', fn ($q) => $q->where('user_id', Auth::id()))
            ->with(['mailboxes', 'domain'])
            ->get();

        $totalMailboxes = 0;
        $totalUsed = 0;
        $totalQuota = 0;

        foreach ($domains as $domain) {
            $totalMailboxes += $domain->mailboxes->count();
            $totalUsed += $domain->mailboxes->sum('quota_used_bytes');
            $totalQuota += $domain->mailboxes->sum('quota_bytes');
        }

        return [
            'domains' => $domains->count(),
            'mailboxes' => $totalMailboxes,
            'used_bytes' => $totalUsed,
            'quota_bytes' => $totalQuota,
            'used_formatted' => $this->formatBytes($totalUsed),
            'quota_formatted' => $this->formatBytes($totalQuota),
            'percent' => $totalQuota > 0 ? round(($totalUsed / $totalQuota) * 100, 1) : 0,
        ];
    }

    protected function formatBytes(int $bytes): string
    {
        if ($bytes < 1024) {
            return $bytes.' B';
        }
        if ($bytes < 1048576) {
            return round($bytes / 1024, 1).' KB';
        }
        if ($bytes < 1073741824) {
            return round($bytes / 1048576, 1).' MB';
        }

        return round($bytes / 1073741824, 1).' GB';
    }
}
