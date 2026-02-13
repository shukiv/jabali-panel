<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Filament\Jabali\Widgets\DnsPendingAddsTable;
use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\Domain;
use App\Services\Agent\AgentClient;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Action as TableAction;
use Filament\Actions\ActionGroup;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\EmbeddedSchema;
use Filament\Schemas\Components\EmbeddedTable;
use Filament\Schemas\Components\EmptyState;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Filament\Support\Enums\FontFamily;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Str;
use Livewire\Attributes\On;

class DnsRecords extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-server-stack';

    protected static ?int $navigationSort = 11;

    public static function getNavigationLabel(): string
    {
        return __('DNS Records');
    }

    public ?int $selectedDomainId = null;

    protected ?AgentClient $agent = null;

    // Pending changes tracking
    public array $pendingEdits = [];

    public array $pendingDeletes = [];

    public array $pendingAdds = [];

    public function getTitle(): string|Htmlable
    {
        return __('DNS Records');
    }

    public function getAgent(): AgentClient
    {
        return $this->agent ??= new AgentClient;
    }

    public function mount(): void
    {
        $this->selectedDomainId = null;
    }

    public function form(Schema $form): Schema
    {
        return $form
            ->schema([
                Select::make('selectedDomainId')
                    ->label(__('Select Domain'))
                    ->options(fn () => Domain::where('user_id', Auth::id())->orderBy('domain')->pluck('domain', 'id')->toArray())
                    ->searchable()
                    ->preload()
                    ->live()
                    ->afterStateUpdated(fn () => $this->onDomainChange())
                    ->placeholder(__('Select a domain to manage DNS records')),
            ]);
    }

    public function content(Schema $schema): Schema
    {
        return $schema->schema([
            Section::make(__('Select Domain'))
                ->description(__('Choose a domain to manage its DNS records.'))
                ->schema([
                    EmbeddedSchema::make('form'),
                ])
                ->visible(fn () => $this->hasDomains()),
            Section::make(__('New Records to Add'))
                ->description(__('These records will be created when you save changes.'))
                ->icon('heroicon-o-plus-circle')
                ->iconColor('success')
                ->collapsible()
                ->schema([
                    EmbeddedTable::make(DnsPendingAddsTable::class, fn () => [
                        'records' => $this->pendingAdds,
                    ]),
                ])
                ->headerActions([
                    Action::make('clearPending')
                        ->label(__('Clear All'))
                        ->icon('heroicon-o-trash')
                        ->color('danger')
                        ->size('sm')
                        ->requiresConfirmation()
                        ->action(fn () => $this->clearPendingAdds()),
                ])
                ->visible(fn () => $this->selectedDomainId !== null && count($this->pendingAdds) > 0),
            Section::make(__('Important'))
                ->description(__('Incorrect DNS changes can make your website or email unreachable. Changes may take up to 48 hours to propagate globally.'))
                ->icon('heroicon-o-exclamation-triangle')
                ->iconColor('warning')
                ->compact()
                ->visible(fn () => $this->selectedDomainId !== null),
            Section::make(__('Unsaved Changes'))
                ->description(fn () => __('You have :count pending change(s). Click Save to apply them.', ['count' => $this->getPendingChangesCount()]))
                ->icon('heroicon-o-clock')
                ->iconColor('info')
                ->compact()
                ->visible(fn () => $this->selectedDomainId !== null && $this->hasPendingChanges()),
            EmbeddedTable::make()
                ->visible(fn () => $this->selectedDomainId !== null),
            EmptyState::make(__('No Domain Selected'))
                ->description(__('Select a domain from the dropdown above to view and manage its DNS records.'))
                ->icon('heroicon-o-cursor-arrow-rays')
                ->iconColor('gray')
                ->visible(fn () => $this->hasDomains() && $this->selectedDomainId === null),
            EmptyState::make(__('No Domains Found'))
                ->description(__('You need to add a domain before you can manage DNS records.'))
                ->icon('heroicon-o-globe-alt')
                ->iconColor('gray')
                ->footer([
                    Action::make('addDomain')
                        ->label(__('Add Domain'))
                        ->icon('heroicon-o-plus')
                        ->url(route('filament.jabali.pages.domains')),
                ])
                ->visible(fn () => ! $this->hasDomains()),
        ]);
    }

    public function onDomainChange(): void
    {
        $this->pendingEdits = [];
        $this->pendingDeletes = [];
        $this->pendingAdds = [];
        $this->resetTable();
    }

    public function updatedSelectedDomainId(): void
    {
        $this->onDomainChange();
    }

    public function hasPendingChanges(): bool
    {
        return count($this->pendingEdits) > 0 || count($this->pendingDeletes) > 0 || count($this->pendingAdds) > 0;
    }

    public function getPendingChangesCount(): int
    {
        return count($this->pendingEdits) + count($this->pendingDeletes) + count($this->pendingAdds);
    }

    public function isRecordPendingDelete(int $recordId): bool
    {
        return in_array($recordId, $this->pendingDeletes);
    }

    public function isRecordPendingEdit(int $recordId): bool
    {
        return isset($this->pendingEdits[$recordId]);
    }

    public function clearPendingAdds(): void
    {
        $this->pendingAdds = [];
        Notification::make()->title(__('Pending records cleared'))->success()->send();
    }

    #[On('dns-pending-add-remove')]
    public function removePendingAddFromTable(string $key): void
    {
        $this->removePendingAdd($key);
    }

    public function removePendingAdd(int|string $identifier): void
    {
        if (is_int($identifier)) {
            unset($this->pendingAdds[$identifier]);
            $this->pendingAdds = array_values($this->pendingAdds);
            Notification::make()->title(__('Pending record removed'))->success()->send();

            return;
        }

        $this->pendingAdds = array_values(array_filter(
            $this->pendingAdds,
            fn (array $record): bool => ($record['key'] ?? null) !== $identifier
        ));
        Notification::make()->title(__('Pending record removed'))->success()->send();
    }

    protected function queuePendingAdd(array $record): void
    {
        $record['key'] ??= (string) Str::uuid();
        $this->pendingAdds[] = $record;
    }

    protected function sanitizePendingAdd(array $record): array
    {
        unset($record['key']);

        return $record;
    }

    protected function hasDomains(): bool
    {
        return Domain::query()->where('user_id', Auth::id())->exists();
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(
                DnsRecord::query()
                    ->whereHas('domain', fn (Builder $query) => $query->where('user_id', Auth::id()))
                    ->when($this->selectedDomainId, fn (Builder $query) => $query->where('domain_id', $this->selectedDomainId))
                    ->orderByRaw("CASE type
                        WHEN 'NS' THEN 1
                        WHEN 'A' THEN 2
                        WHEN 'AAAA' THEN 3
                        WHEN 'CNAME' THEN 4
                        WHEN 'MX' THEN 5
                        WHEN 'TXT' THEN 6
                        WHEN 'SRV' THEN 7
                        WHEN 'CAA' THEN 8
                        ELSE 9 END")
                    ->orderBy('name')
            )
            ->columns([
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->color(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id) ? 'danger' : match ($record->type) {
                        'A', 'AAAA' => 'info',
                        'CNAME' => 'primary',
                        'MX' => 'warning',
                        'TXT' => 'success',
                        'NS' => 'danger',
                        'SRV' => 'primary',
                        'CAA' => 'warning',
                        default => 'gray',
                    })
                    ->sortable(),
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->fontFamily(FontFamily::Mono)
                    ->color(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id) ? 'danger' : null)
                    ->searchable(),
                TextColumn::make('content')
                    ->label(__('Content'))
                    ->fontFamily(FontFamily::Mono)
                    ->limit(50)
                    ->tooltip(fn ($record) => $record->content)
                    ->color(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id) ? 'danger' : ($this->isRecordPendingEdit($record->id) ? 'warning' : null))
                    ->searchable(),
                TextColumn::make('ttl')
                    ->label(__('TTL'))
                    ->color(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id) ? 'danger' : null)
                    ->sortable(),
                TextColumn::make('priority')
                    ->label(__('Priority'))
                    ->placeholder(__('-'))
                    ->color(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id) ? 'danger' : null)
                    ->sortable(),
            ])
            ->filters([])
            ->headerActions([
                TableAction::make('resetToDefaults')
                    ->label(__('Reset to Defaults'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->requiresConfirmation()
                    ->modalHeading(__('Reset DNS Records'))
                    ->modalDescription(__('This will delete all existing DNS records and create default records. This action cannot be undone.'))
                    ->modalIcon('heroicon-o-exclamation-triangle')
                    ->modalIconColor('warning')
                    ->action(fn () => $this->resetToDefaults()),
                TableAction::make('discardChanges')
                    ->label(__('Discard'))
                    ->icon('heroicon-o-x-mark')
                    ->color('gray')
                    ->visible(fn () => $this->hasPendingChanges())
                    ->action(fn () => $this->discardChanges()),
                TableAction::make('saveChanges')
                    ->label(fn () => $this->hasPendingChanges()
                        ? __('Save (:count changes)', ['count' => $this->getPendingChangesCount()])
                        : __('Save'))
                    ->icon('heroicon-o-check')
                    ->color('primary')
                    ->action(fn () => $this->saveChanges()),
            ])
            ->actions([
                ActionGroup::make([
                    TableAction::make('edit')
                        ->label(__('Edit'))
                        ->icon('heroicon-o-pencil')
                        ->color(fn (DnsRecord $record) => $this->isRecordPendingEdit($record->id) ? 'warning' : 'gray')
                        ->hidden(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id))
                        ->modalHeading(__('Edit DNS Record'))
                        ->modalDescription(__('Changes will be queued until you click Save.'))
                        ->modalIcon('heroicon-o-pencil-square')
                        ->modalIconColor('primary')
                        ->modalSubmitActionLabel(__('Queue Changes'))
                        ->fillForm(fn (DnsRecord $record) => [
                            'type' => $this->pendingEdits[$record->id]['type'] ?? $record->type,
                            'name' => $this->pendingEdits[$record->id]['name'] ?? $record->name,
                            'content' => $this->pendingEdits[$record->id]['content'] ?? $record->content,
                            'ttl' => $this->pendingEdits[$record->id]['ttl'] ?? $record->ttl,
                            'priority' => $this->pendingEdits[$record->id]['priority'] ?? $record->priority,
                        ])
                        ->form($this->getRecordFormSchema())
                        ->action(function (DnsRecord $record, array $data): void {
                            if ($record->domain->user_id !== Auth::id()) {
                                Notification::make()->title(__('Access denied'))->danger()->send();

                                return;
                            }
                            $this->pendingEdits[$record->id] = [
                                'type' => $data['type'],
                                'name' => $data['name'],
                                'content' => $data['content'],
                                'ttl' => $data['ttl'] ?? 3600,
                                'priority' => $data['priority'] ?? null,
                            ];
                            Notification::make()
                                ->title(__('Edit queued'))
                                ->body(__('Click Save to apply.'))
                                ->info()
                                ->send();
                        }),
                    TableAction::make('undoEdit')
                        ->label(__('Undo Edit'))
                        ->icon('heroicon-o-arrow-uturn-left')
                        ->color('warning')
                        ->visible(fn (DnsRecord $record) => $this->isRecordPendingEdit($record->id))
                        ->action(function (DnsRecord $record): void {
                            unset($this->pendingEdits[$record->id]);
                            Notification::make()->title(__('Edit undone'))->success()->send();
                        }),
                    TableAction::make('undoDelete')
                        ->label(__('Undo Delete'))
                        ->icon('heroicon-o-arrow-uturn-left')
                        ->color('success')
                        ->visible(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id))
                        ->action(function (DnsRecord $record): void {
                            $this->pendingDeletes = array_values(array_diff($this->pendingDeletes, [$record->id]));
                            Notification::make()->title(__('Delete undone'))->success()->send();
                        }),
                    TableAction::make('delete')
                        ->label(__('Delete'))
                        ->icon('heroicon-o-trash')
                        ->color('danger')
                        ->hidden(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id))
                        ->requiresConfirmation()
                        ->modalHeading(__('Delete Record'))
                        ->modalDescription(fn (DnsRecord $record) => __('Delete the :type record for :name?', ['type' => $record->type, 'name' => $record->name]))
                        ->modalIcon('heroicon-o-trash')
                        ->modalIconColor('danger')
                        ->modalSubmitActionLabel(__('Queue Delete'))
                        ->action(function (DnsRecord $record): void {
                            if ($record->domain->user_id !== Auth::id()) {
                                Notification::make()->title(__('Access denied'))->danger()->send();

                                return;
                            }
                            if (! in_array($record->id, $this->pendingDeletes)) {
                                $this->pendingDeletes[] = $record->id;
                            }
                            unset($this->pendingEdits[$record->id]);
                            Notification::make()
                                ->title(__('Delete queued'))
                                ->body(__('Click Save to apply.'))
                                ->warning()
                                ->send();
                        }),
                ]),
            ])
            ->emptyStateHeading(__('No DNS records'))
            ->emptyStateDescription(__('Add DNS records to manage your domain\'s DNS configuration.'))
            ->emptyStateIcon('heroicon-o-server-stack')
            ->striped();
    }

    protected function getRecordFormSchema(): array
    {
        return [
            Select::make('type')
                ->label(__('Record Type'))
                ->options([
                    'A' => __('A - IPv4 Address'),
                    'AAAA' => __('AAAA - IPv6 Address'),
                    'CNAME' => __('CNAME - Canonical Name'),
                    'MX' => __('MX - Mail Exchange'),
                    'TXT' => __('TXT - Text Record'),
                    'NS' => __('NS - Nameserver'),
                    'SRV' => __('SRV - Service'),
                    'CAA' => __('CAA - Certificate Authority'),
                ])
                ->required()
                ->live(),
            TextInput::make('name')
                ->label(__('Name'))
                ->placeholder(__('@ for root, or subdomain (e.g., www, mail)'))
                ->required()
                ->maxLength(255),
            TextInput::make('content')
                ->label(__('Content'))
                ->required()
                ->maxLength(1024),
            TextInput::make('ttl')
                ->label(__('TTL (seconds)'))
                ->numeric()
                ->default(3600)
                ->minValue(60)
                ->maxValue(86400),
            TextInput::make('priority')
                ->label(__('Priority'))
                ->numeric()
                ->visible(fn ($get) => in_array($get('type'), ['MX', 'SRV']))
                ->default(10),
        ];
    }

    public function getSelectedDomainName(): ?string
    {
        return Domain::find($this->selectedDomainId)?->domain;
    }

    public function addRecordAction(): Action
    {
        return Action::make('addRecord')
            ->label(__('Add Record'))
            ->icon('heroicon-o-plus')
            ->color('primary')
            ->modalHeading(__('Add DNS Record'))
            ->modalDescription(__('The record will be queued until you click Save.'))
            ->modalIcon('heroicon-o-plus-circle')
            ->modalIconColor('primary')
            ->modalSubmitActionLabel(__('Queue Record'))
            ->modalWidth('lg')
            ->form($this->getRecordFormSchema())
            ->action(function (array $data) {
                $this->queuePendingAdd([
                    'type' => $data['type'],
                    'name' => $data['name'],
                    'content' => $data['content'],
                    'ttl' => $data['ttl'] ?? 3600,
                    'priority' => $data['priority'] ?? null,
                ]);
                Notification::make()
                    ->title(__('Record queued'))
                    ->body(__('Click Save to apply.'))
                    ->info()
                    ->send();
            });
    }

    public function saveChanges(bool $notify = true): void
    {
        if (! $this->hasPendingChanges()) {
            if ($notify) {
                Notification::make()->title(__('No changes to save'))->warning()->send();
            }

            return;
        }

        $domain = Domain::find($this->selectedDomainId);
        if (! $domain || $domain->user_id !== Auth::id()) {
            Notification::make()->title(__('Access denied'))->danger()->send();

            return;
        }

        try {
            foreach ($this->pendingDeletes as $recordId) {
                DnsRecord::where('id', $recordId)
                    ->whereHas('domain', fn ($q) => $q->where('user_id', Auth::id()))
                    ->delete();
            }

            foreach ($this->pendingEdits as $recordId => $data) {
                $record = DnsRecord::find($recordId);
                if ($record && $record->domain->user_id === Auth::id()) {
                    $record->update($data);
                }
            }

            foreach ($this->pendingAdds as $data) {
                DnsRecord::create(array_merge(['domain_id' => $this->selectedDomainId], $this->sanitizePendingAdd($data)));
            }

            $this->syncZoneFile($domain->domain);

            $this->pendingEdits = [];
            $this->pendingDeletes = [];
            $this->pendingAdds = [];

            $this->resetTable();

            if ($notify) {
                Notification::make()
                    ->title(__('Changes saved'))
                    ->body(__('DNS records updated. Changes may take up to 48 hours to propagate.'))
                    ->success()
                    ->send();
            }

        } catch (Exception $e) {
            Notification::make()
                ->title(__('Failed to save changes'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function discardChanges(): void
    {
        $this->pendingEdits = [];
        $this->pendingDeletes = [];
        $this->pendingAdds = [];
        Notification::make()->title(__('Changes discarded'))->success()->send();
    }

    public function resetToDefaults(): void
    {
        $domain = Domain::find($this->selectedDomainId);
        if (! $domain || $domain->user_id !== Auth::id()) {
            Notification::make()->title(__('Access denied'))->danger()->send();

            return;
        }

        try {
            DnsRecord::where('domain_id', $this->selectedDomainId)->delete();

            $settings = DnsSetting::getAll();
            $serverIp = $domain->ip_address ?: ($settings['default_ip'] ?? trim(shell_exec("hostname -I | awk '{print $1}'") ?? '') ?: '127.0.0.1');
            $serverIpv6 = $domain->ipv6_address ?: ($settings['default_ipv6'] ?? null);
            $ns1 = $settings['ns1'] ?? 'ns1.'.$domain->domain;
            $ns2 = $settings['ns2'] ?? 'ns2.'.$domain->domain;

            $defaultRecords = [
                ['name' => '@', 'type' => 'NS', 'content' => $ns1, 'ttl' => 3600, 'priority' => null],
                ['name' => '@', 'type' => 'NS', 'content' => $ns2, 'ttl' => 3600, 'priority' => null],
                ['name' => '@', 'type' => 'A', 'content' => $serverIp, 'ttl' => 3600, 'priority' => null],
                ['name' => 'www', 'type' => 'A', 'content' => $serverIp, 'ttl' => 3600, 'priority' => null],
                ['name' => 'mail', 'type' => 'A', 'content' => $serverIp, 'ttl' => 3600, 'priority' => null],
                ['name' => '@', 'type' => 'MX', 'content' => 'mail.'.$domain->domain, 'ttl' => 3600, 'priority' => 10],
                ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 mx a ~all', 'ttl' => 3600, 'priority' => null],
            ];

            if (! empty($serverIpv6)) {
                $defaultRecords[] = ['name' => '@', 'type' => 'AAAA', 'content' => $serverIpv6, 'ttl' => 3600, 'priority' => null];
                $defaultRecords[] = ['name' => 'www', 'type' => 'AAAA', 'content' => $serverIpv6, 'ttl' => 3600, 'priority' => null];
                $defaultRecords[] = ['name' => 'mail', 'type' => 'AAAA', 'content' => $serverIpv6, 'ttl' => 3600, 'priority' => null];
            }

            $defaultRecords = $this->appendNameserverRecords(
                $defaultRecords,
                $domain->domain,
                $ns1,
                $ns2,
                $serverIp,
                $serverIpv6,
                3600
            );

            foreach ($defaultRecords as $record) {
                DnsRecord::create(array_merge(['domain_id' => $this->selectedDomainId], $record));
            }

            $this->syncZoneFile($domain->domain);

            $this->pendingEdits = [];
            $this->pendingDeletes = [];
            $this->pendingAdds = [];

            $this->resetTable();

            Notification::make()
                ->title(__('DNS records reset'))
                ->body(__('Default records have been created for :domain', ['domain' => $domain->domain]))
                ->success()
                ->send();

        } catch (Exception $e) {
            Notification::make()
                ->title(__('Failed to reset records'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function applyTemplateAction(): Action
    {
        return Action::make('applyTemplate')
            ->label(__('Apply Template'))
            ->icon('heroicon-o-document-duplicate')
            ->color('gray')
            ->modalHeading(__('Apply Email Template'))
            ->modalDescription(__('This will apply the selected email DNS records immediately.'))
            ->modalIcon('heroicon-o-envelope')
            ->modalIconColor('warning')
            ->modalSubmitActionLabel(__('Apply Template'))
            ->modalWidth('lg')
            ->form([
                Select::make('template')
                    ->label(__('Email Provider'))
                    ->options([
                        'google' => __('Google Workspace (Gmail)'),
                        'microsoft' => __('Microsoft 365 (Outlook)'),
                        'zoho' => __('Zoho Mail'),
                        'protonmail' => __('ProtonMail'),
                        'fastmail' => __('Fastmail'),
                        'local' => __('Local Mail Server (This Server)'),
                    ])
                    ->required()
                    ->live(),
                TextInput::make('verification_code')
                    ->label(__('Domain Verification Code (optional)'))
                    ->placeholder(__('e.g., google-site-verification=xxx')),
            ])
            ->action(function (array $data) {
                $domain = Domain::find($this->selectedDomainId);
                if (! $domain || $domain->user_id !== Auth::id()) {
                    Notification::make()->title(__('Access denied'))->danger()->send();

                    return;
                }

                $domainName = $domain->domain;
                $template = $data['template'];
                $verificationCode = $data['verification_code'] ?? null;

                $recordsToDelete = DnsRecord::where('domain_id', $this->selectedDomainId)
                    ->where(function ($query) {
                        $query->where('type', 'MX')
                            ->orWhere(function ($q) {
                                $q->where('type', 'A')->where('name', 'mail');
                            })
                            ->orWhere(function ($q) {
                                $q->where('type', 'CNAME')->where('name', 'autodiscover');
                            })
                            ->orWhere(function ($q) {
                                $q->where('type', 'TXT')
                                    ->where(function ($inner) {
                                        $inner->where('content', 'like', '%spf%')
                                            ->orWhere('content', 'like', '%v=spf1%')
                                            ->orWhere('content', 'like', '%google-site-verification%')
                                            ->orWhere('content', 'like', '%MS=%')
                                            ->orWhere('content', 'like', '%zoho-verification%')
                                            ->orWhere('content', 'like', '%protonmail-verification%')
                                            ->orWhere('name', 'like', '%_domainkey%');
                                    });
                            });
                    })
                    ->pluck('id')
                    ->toArray();

                foreach ($recordsToDelete as $id) {
                    if (! in_array($id, $this->pendingDeletes)) {
                        $this->pendingDeletes[] = $id;
                    }
                    unset($this->pendingEdits[$id]);
                }

                $records = $this->getTemplateRecords($template, $domain, $verificationCode);
                foreach ($records as $record) {
                    $this->queuePendingAdd($record);
                }

                if (! $this->hasPendingChanges()) {
                    Notification::make()
                        ->title(__('No changes to apply'))
                        ->warning()
                        ->send();

                    return;
                }

                $this->saveChanges(false);

                Notification::make()
                    ->title(__('Template applied'))
                    ->body(__('Email records for :provider have been applied. Changes may take up to 48 hours to propagate.', ['provider' => ucfirst($template)]))
                    ->success()
                    ->send();
            });
    }

    protected function getTemplateRecords(string $template, Domain $domain, ?string $verificationCode): array
    {
        $settings = DnsSetting::getAll();
        $serverIp = $domain->ip_address ?: ($settings['default_ip'] ?? trim(shell_exec("hostname -I | awk '{print $1}'") ?? '') ?: '127.0.0.1');
        $serverIpv6 = $domain->ipv6_address ?: ($settings['default_ipv6'] ?? null);
        $domainName = $domain->domain;

        $records = match ($template) {
            'google' => [
                ['name' => '@', 'type' => 'MX', 'content' => 'aspmx.l.google.com', 'ttl' => 3600, 'priority' => 1],
                ['name' => '@', 'type' => 'MX', 'content' => 'alt1.aspmx.l.google.com', 'ttl' => 3600, 'priority' => 5],
                ['name' => '@', 'type' => 'MX', 'content' => 'alt2.aspmx.l.google.com', 'ttl' => 3600, 'priority' => 5],
                ['name' => '@', 'type' => 'MX', 'content' => 'alt3.aspmx.l.google.com', 'ttl' => 3600, 'priority' => 10],
                ['name' => '@', 'type' => 'MX', 'content' => 'alt4.aspmx.l.google.com', 'ttl' => 3600, 'priority' => 10],
                ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 include:_spf.google.com ~all', 'ttl' => 3600, 'priority' => null],
            ],
            'microsoft' => [
                ['name' => '@', 'type' => 'MX', 'content' => str_replace('.', '-', $domainName).'.mail.protection.outlook.com', 'ttl' => 3600, 'priority' => 0],
                ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 include:spf.protection.outlook.com ~all', 'ttl' => 3600, 'priority' => null],
                ['name' => 'autodiscover', 'type' => 'CNAME', 'content' => 'autodiscover.outlook.com', 'ttl' => 3600, 'priority' => null],
            ],
            'zoho' => [
                ['name' => '@', 'type' => 'MX', 'content' => 'mx.zoho.com', 'ttl' => 3600, 'priority' => 10],
                ['name' => '@', 'type' => 'MX', 'content' => 'mx2.zoho.com', 'ttl' => 3600, 'priority' => 20],
                ['name' => '@', 'type' => 'MX', 'content' => 'mx3.zoho.com', 'ttl' => 3600, 'priority' => 50],
                ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 include:zoho.com ~all', 'ttl' => 3600, 'priority' => null],
            ],
            'protonmail' => [
                ['name' => '@', 'type' => 'MX', 'content' => 'mail.protonmail.ch', 'ttl' => 3600, 'priority' => 10],
                ['name' => '@', 'type' => 'MX', 'content' => 'mailsec.protonmail.ch', 'ttl' => 3600, 'priority' => 20],
                ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 include:_spf.protonmail.ch mx ~all', 'ttl' => 3600, 'priority' => null],
            ],
            'fastmail' => [
                ['name' => '@', 'type' => 'MX', 'content' => 'in1-smtp.messagingengine.com', 'ttl' => 3600, 'priority' => 10],
                ['name' => '@', 'type' => 'MX', 'content' => 'in2-smtp.messagingengine.com', 'ttl' => 3600, 'priority' => 20],
                ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 include:spf.messagingengine.com ~all', 'ttl' => 3600, 'priority' => null],
            ],
            'local' => [
                ['name' => '@', 'type' => 'MX', 'content' => 'mail.'.$domainName, 'ttl' => 3600, 'priority' => 10],
                ['name' => 'mail', 'type' => 'A', 'content' => $serverIp, 'ttl' => 3600, 'priority' => null],
                ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 mx a ~all', 'ttl' => 3600, 'priority' => null],
            ],
            default => [],
        };

        if ($template === 'local' && ! empty($serverIpv6)) {
            $records[] = ['name' => 'mail', 'type' => 'AAAA', 'content' => $serverIpv6, 'ttl' => 3600, 'priority' => null];
        }

        if ($verificationCode) {
            $records[] = ['name' => '@', 'type' => 'TXT', 'content' => $verificationCode, 'ttl' => 3600, 'priority' => null];
        }

        return $records;
    }

    protected function syncZoneFile(string $domain): void
    {
        try {
            $records = DnsRecord::whereHas('domain', fn ($q) => $q->where('domain', $domain))->get();
            $settings = DnsSetting::getAll();
            $defaultIp = $settings['default_ip'] ?? trim(shell_exec("hostname -I | awk '{print $1}'") ?? '') ?: '127.0.0.1';
            $this->getAgent()->send('dns.sync_zone', [
                'domain' => $domain,
                'records' => $records->toArray(),
                'ns1' => $settings['ns1'] ?? 'ns1.example.com',
                'ns2' => $settings['ns2'] ?? 'ns2.example.com',
                'admin_email' => $settings['admin_email'] ?? 'admin.example.com',
                'default_ip' => $defaultIp,
                'default_ipv6' => $settings['default_ipv6'] ?? null,
                'default_ttl' => $settings['default_ttl'] ?? 3600,
            ]);
        } catch (Exception $e) {
            Notification::make()->title(__('Warning: Zone file sync failed'))->body($e->getMessage())->warning()->send();
        }
    }

    protected function getHeaderActions(): array
    {
        return [
            $this->applyTemplateAction()
                ->visible(fn () => $this->selectedDomainId !== null),
            $this->addRecordAction()
                ->visible(fn () => $this->selectedDomainId !== null),
        ];
    }

    /**
     * @param  array<int, array<string, mixed>>  $records
     * @return array<int, array<string, mixed>>
     */
    protected function appendNameserverRecords(
        array $records,
        string $domain,
        string $ns1,
        string $ns2,
        string $ipv4,
        ?string $ipv6,
        int $ttl
    ): array {
        $labels = $this->getNameserverLabels($domain, [$ns1, $ns2]);

        if ($labels === []) {
            return $records;
        }

        $existingA = array_map(
            fn (array $record): string => $record['name'] ?? '',
            array_filter($records, fn (array $record): bool => ($record['type'] ?? '') === 'A')
        );
        $existingAAAA = array_map(
            fn (array $record): string => $record['name'] ?? '',
            array_filter($records, fn (array $record): bool => ($record['type'] ?? '') === 'AAAA')
        );

        foreach ($labels as $label) {
            if (! in_array($label, $existingA, true)) {
                $records[] = ['name' => $label, 'type' => 'A', 'content' => $ipv4, 'ttl' => $ttl, 'priority' => null];
            }

            if ($ipv6 && ! in_array($label, $existingAAAA, true)) {
                $records[] = ['name' => $label, 'type' => 'AAAA', 'content' => $ipv6, 'ttl' => $ttl, 'priority' => null];
            }
        }

        return $records;
    }

    /**
     * @param  array<int, string>  $nameservers
     * @return array<int, string>
     */
    protected function getNameserverLabels(string $domain, array $nameservers): array
    {
        $domain = rtrim($domain, '.');
        $labels = [];

        foreach ($nameservers as $nameserver) {
            $nameserver = rtrim($nameserver ?? '', '.');

            if ($nameserver === '') {
                continue;
            }

            if ($nameserver === $domain) {
                $label = '@';
            } elseif (str_ends_with($nameserver, '.'.$domain)) {
                $label = substr($nameserver, 0, -strlen('.'.$domain));
            } else {
                continue;
            }

            if ($label !== '@') {
                $labels[] = $label;
            }
        }

        return array_values(array_unique($labels));
    }
}
