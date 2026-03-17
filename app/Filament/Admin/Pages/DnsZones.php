<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Filament\Admin\Widgets\DnsPendingAddsTable;
use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\Domain;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use App\Support\ServerFacts;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
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
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Schema;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Support\Str;
use Livewire\Attributes\On;

class DnsZones extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-server-stack';

    protected static ?int $navigationSort = 7;

    public ?int $selectedDomainId = null;

    // Pending changes tracking
    public array $pendingEdits = [];    // [record_id => [field => value]]

    public array $pendingDeletes = [];  // [record_id, ...]

    public array $pendingAdds = [];     // [[field => value], ...]

    public static function getNavigationLabel(): string
    {
        return __('DNS Zones');
    }

    public function getTitle(): string|Htmlable
    {
        return __('DNS Zone Manager');
    }

    public function getAgent(): AgentClient
    {
        return app(AgentClient::class);
    }

    public function mount(): void
    {
        // Start with no domain selected
        $this->selectedDomainId = null;
    }

    public function form(Schema $form): Schema
    {
        return $form
            ->schema([
                Select::make('selectedDomainId')
                    ->label(__('Select Domain'))
                    ->options(fn () => Domain::orderBy('domain')->pluck('domain', 'id')->toArray())
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
                ->description(__('Choose a domain to manage DNS records.'))
                ->schema([
                    EmbeddedSchema::make('form'),
                ]),
            Section::make(__('Zone Status'))
                ->description(fn () => $this->getSelectedDomain()?->domain)
                ->icon('heroicon-o-signal')
                ->headerActions([
                    Action::make('rebuildZone')
                        ->label(__('Rebuild Zone'))
                        ->icon('heroicon-o-arrow-path')
                        ->color('gray')
                        ->action(fn () => $this->rebuildCurrentZone()),
                    Action::make('deleteZone')
                        ->label(__('Delete Zone'))
                        ->icon('heroicon-o-trash')
                        ->color('danger')
                        ->requiresConfirmation()
                        ->modalHeading(__('Delete DNS Zone'))
                        ->modalDescription(__('Delete DNS zone for this domain? All records will be removed.'))
                        ->action(fn () => $this->deleteCurrentZone()),
                ])
                ->schema([
                    Grid::make(['default' => 1, 'sm' => 3])->schema([
                        Text::make(fn () => (($this->getZoneStatus() ?? [])['zone_file_exists'] ?? false) ? __('Active') : __('Missing'))
                            ->badge()
                            ->color(fn () => (($this->getZoneStatus() ?? [])['zone_file_exists'] ?? false) ? 'success' : 'danger'),
                        Text::make(fn () => __(':count records', ['count' => ($this->getZoneStatus() ?? [])['records_count'] ?? 0]))
                            ->badge()
                            ->color('gray'),
                        Text::make(fn () => __('Owner: :owner', ['owner' => ($this->getZoneStatus() ?? [])['user'] ?? 'N/A']))
                            ->color('gray'),
                    ]),
                ])
                ->visible(fn () => $this->selectedDomainId !== null),
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
            EmbeddedTable::make()
                ->visible(fn () => $this->selectedDomainId !== null),
            EmptyState::make(__('No Domain Selected'))
                ->description(__('Select a domain from the dropdown above to manage DNS records.'))
                ->icon('heroicon-o-globe-alt')
                ->iconColor('gray')
                ->visible(fn () => $this->selectedDomainId === null),
        ]);
    }

    public function onDomainChange(): void
    {
        // Discard pending changes when switching domains
        $this->pendingEdits = [];
        $this->pendingDeletes = [];
        $this->pendingAdds = [];
        $this->resetTable();
    }

    public function updatedSelectedDomainId(): void
    {
        $this->onDomainChange();
    }

    // Pending changes helpers
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

    public function table(Table $table): Table
    {
        return $table
            ->query(
                DnsRecord::query()
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
                    ->fontFamily('mono')
                    ->color(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id) ? 'danger' : null)
                    ->searchable(),
                TextColumn::make('content')
                    ->label(__('Content'))
                    ->fontFamily('mono')
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
                TextColumn::make('domain.user.username')
                    ->label(__('Owner'))
                    ->placeholder(__('N/A'))
                    ->sortable(),
            ])
            ->filters([])
            ->headerActions([
                Action::make('resetToDefaults')
                    ->label(__('Reset to Defaults'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->requiresConfirmation()
                    ->modalHeading(__('Reset DNS Records'))
                    ->modalDescription(__('This will delete all existing DNS records and create default records. This action cannot be undone.'))
                    ->modalIcon('heroicon-o-exclamation-triangle')
                    ->modalIconColor('warning')
                    ->action(fn () => $this->resetToDefaults()),
                Action::make('saveChanges')
                    ->label(__('Save'))
                    ->icon('heroicon-o-check')
                    ->color('primary')
                    ->action(fn () => $this->saveChanges()),
            ])
            ->recordActions([
                Action::make('edit')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil')
                    ->color(fn (DnsRecord $record) => $this->isRecordPendingEdit($record->id) ? 'warning' : 'gray')
                    ->hidden(fn (DnsRecord $record) => $this->isRecordPendingDelete($record->id))
                    ->modalHeading(__('Edit DNS Record'))
                    ->modalDescription(__('Changes will be queued until you click "Save Changes".'))
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
                    ->form([
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
                            ->reactive(),
                        TextInput::make('name')
                            ->label(__('Name'))
                            ->placeholder(__('@ for root, or subdomain'))
                            ->required()
                            ->maxLength(255),
                        TextInput::make('content')
                            ->label(__('Content'))
                            ->required()
                            ->maxLength(1024),
                        TextInput::make('ttl')
                            ->label(__('TTL (seconds)'))
                            ->numeric()
                            ->minValue(60)
                            ->maxValue(86400),
                        TextInput::make('priority')
                            ->label(__('Priority'))
                            ->numeric()
                            ->visible(fn ($get) => in_array($get('type'), ['MX', 'SRV'])),
                    ])
                    ->action(function (DnsRecord $record, array $data): void {
                        // Queue the edit
                        $this->pendingEdits[$record->id] = [
                            'type' => $data['type'],
                            'name' => $data['name'],
                            'content' => $data['content'],
                            'ttl' => $data['ttl'] ?? 3600,
                            'priority' => $data['priority'] ?? null,
                        ];
                        Notification::make()
                            ->title(__('Edit queued'))
                            ->body(__('Click "Save Changes" to apply.'))
                            ->info()
                            ->send();
                    }),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Record'))
                    ->modalDescription(fn (DnsRecord $record) => __('Delete the :type record for :name?', ['type' => $record->type, 'name' => $record->name]))
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->modalSubmitActionLabel(__('Delete'))
                    ->action(function (DnsRecord $record): void {
                        if (! in_array($record->id, $this->pendingDeletes)) {
                            $this->pendingDeletes[] = $record->id;
                        }
                        unset($this->pendingEdits[$record->id]);
                    }),
            ])
            ->emptyStateHeading(__('No DNS records'))
            ->emptyStateDescription(__('Add DNS records to manage this domain\'s DNS configuration.'))
            ->emptyStateIcon('heroicon-o-server-stack')
            ->striped();
    }

    public function getSelectedDomain(): ?Domain
    {
        return $this->selectedDomainId ? Domain::find($this->selectedDomainId) : null;
    }

    public function getZoneStatus(): ?array
    {
        if (! $this->selectedDomainId) {
            return null;
        }

        $domain = Domain::find($this->selectedDomainId);
        if (! $domain) {
            return null;
        }

        $zoneFile = "/etc/bind/zones/db.{$domain->domain}";
        $recordsCount = DnsRecord::where('domain_id', $this->selectedDomainId)->count();

        return [
            'domain' => $domain->domain,
            'user' => $domain->user->username ?? 'N/A',
            'records_count' => $recordsCount,
            'zone_file_exists' => file_exists($zoneFile),
        ];
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

    public function saveChanges(bool $notify = true): void
    {
        if (! $this->hasPendingChanges()) {
            if ($notify) {
                Notification::make()->title(__('No changes to save'))->warning()->send();
            }

            return;
        }

        $domain = Domain::find($this->selectedDomainId);
        if (! $domain) {
            Notification::make()->title(__('Domain not found'))->danger()->send();

            return;
        }

        try {
            // Apply deletes
            foreach ($this->pendingDeletes as $recordId) {
                DnsRecord::where('id', $recordId)->delete();
            }

            // Apply edits
            foreach ($this->pendingEdits as $recordId => $data) {
                $record = DnsRecord::find($recordId);
                if ($record) {
                    $record->update($data);
                }
            }

            // Apply adds
            foreach ($this->pendingAdds as $data) {
                DnsRecord::create(array_merge(['domain_id' => $this->selectedDomainId], $this->sanitizePendingAdd($data)));
            }

            // Sync zone file
            $this->syncZoneFile($domain->domain);

            // Clear pending changes
            $this->pendingEdits = [];
            $this->pendingDeletes = [];
            $this->pendingAdds = [];

            // Reset table to refresh data
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
                ->body(SafeError::message($e))
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
        if (! $domain) {
            Notification::make()->title(__('Domain not found'))->danger()->send();

            return;
        }

        try {
            // Delete all existing records
            DnsRecord::where('domain_id', $this->selectedDomainId)->delete();

            // Create default records
            $settings = DnsSetting::getAll();
            $serverIp = $domain->ip_address ?: ($settings['default_ip'] ?? ServerFacts::serverIp('127.0.0.1'));
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

            // Sync zone file
            $this->syncZoneFile($domain->domain);

            // Clear any pending changes
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
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('syncAllZones')
                ->label(__('Sync All Zones'))
                ->icon('heroicon-o-arrow-path')
                ->color('primary')
                ->requiresConfirmation()
                ->modalDescription(__('This will regenerate all zone files from the database.'))
                ->action(function () {
                    $count = 0;
                    $domains = Domain::all();
                    $settings = DnsSetting::getAll();

                    foreach ($domains as $domain) {
                        try {
                            $records = DnsRecord::where('domain_id', $domain->id)->get()->toArray();
                            $this->getAgent()->send('dns.sync_zone', [
                                'domain' => $domain->domain,
                                'records' => $records,
                                'ns1' => $settings['ns1'] ?? 'ns1.example.com',
                                'ns2' => $settings['ns2'] ?? 'ns2.example.com',
                                'admin_email' => $settings['admin_email'] ?? 'admin.example.com',
                                'default_ttl' => $settings['default_ttl'] ?? 3600,
                            ]);
                            $count++;
                        } catch (Exception $e) {
                            // Continue with other zones
                        }
                    }

                    Notification::make()->title(__(':count zones synced', ['count' => $count]))->success()->send();
                }),
            $this->applyTemplateAction()
                ->visible(fn () => $this->selectedDomainId !== null),
            $this->addRecordAction()
                ->visible(fn () => $this->selectedDomainId !== null),
        ];
    }

    public function addRecordAction(): Action
    {
        return Action::make('addRecord')
            ->label(__('Add Record'))
            ->icon('heroicon-o-plus')
            ->color('primary')
            ->modalHeading(__('Add DNS Record'))
            ->modalDescription(__('The record will be queued until you click "Save Changes".'))
            ->modalIcon('heroicon-o-plus-circle')
            ->modalIconColor('primary')
            ->modalSubmitActionLabel(__('Queue Record'))
            ->modalWidth('lg')
            ->form([
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
                    ->reactive(),
                TextInput::make('name')
                    ->label(__('Name'))
                    ->placeholder(__('@ for root, or subdomain'))
                    ->required(),
                TextInput::make('content')
                    ->label(__('Content'))
                    ->required(),
                TextInput::make('ttl')
                    ->label(__('TTL'))
                    ->numeric()
                    ->default(3600),
                TextInput::make('priority')
                    ->label(__('Priority'))
                    ->numeric()
                    ->visible(fn ($get) => in_array($get('type'), ['MX', 'SRV'])),
            ])
            ->action(function (array $data) {
                // Queue the add
                $this->queuePendingAdd([
                    'type' => $data['type'],
                    'name' => $data['name'],
                    'content' => $data['content'],
                    'ttl' => $data['ttl'] ?? 3600,
                    'priority' => $data['priority'] ?? null,
                ]);
                Notification::make()
                    ->title(__('Record queued'))
                    ->body(__('Click "Save Changes" to apply.'))
                    ->info()
                    ->send();
            });
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
                        'none' => __('Remove All Email Records'),
                    ])
                    ->required()
                    ->reactive(),
                TextInput::make('verification_code')
                    ->label(__('Domain Verification Code (optional)'))
                    ->placeholder(__('e.g., google-site-verification=xxx'))
                    ->visible(fn ($get) => $get('template') && $get('template') !== 'none'),
            ])
            ->action(function (array $data) {
                $domain = Domain::find($this->selectedDomainId);
                if (! $domain) {
                    Notification::make()->title(__('Domain not found'))->danger()->send();

                    return;
                }

                $domainName = $domain->domain;
                $template = $data['template'];
                $verificationCode = $data['verification_code'] ?? null;

                // Queue deletion of existing MX and email-related records
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

                // Queue new records
                if ($template !== 'none') {
                    $records = $this->getTemplateRecords($template, $domainName, $verificationCode);
                    foreach ($records as $record) {
                        $this->queuePendingAdd($record);
                    }
                }

                if (! $this->hasPendingChanges()) {
                    Notification::make()
                        ->title(__('No changes to apply'))
                        ->warning()
                        ->send();

                    return;
                }

                $this->saveChanges(false);

                $message = $template === 'none'
                    ? __('Email records removed.')
                    : __('Email records for :provider have been applied.', ['provider' => ucfirst($template)]);

                Notification::make()
                    ->title(__('Template applied'))
                    ->body($message.' '.__('Changes may take up to 48 hours to propagate.'))
                    ->success()
                    ->send();
            });
    }

    protected function getTemplateRecords(string $template, string $domain, ?string $verificationCode): array
    {
        $settings = DnsSetting::getAll();
        $serverIp = $settings['default_ip'] ?? ServerFacts::serverIp('127.0.0.1');

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
                ['name' => '@', 'type' => 'MX', 'content' => str_replace('.', '-', $domain).'.mail.protection.outlook.com', 'ttl' => 3600, 'priority' => 0],
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
                ['name' => '@', 'type' => 'MX', 'content' => 'mail.'.$domain, 'ttl' => 3600, 'priority' => 10],
                ['name' => 'mail', 'type' => 'A', 'content' => $serverIp, 'ttl' => 3600, 'priority' => null],
                ['name' => '@', 'type' => 'TXT', 'content' => 'v=spf1 mx a ~all', 'ttl' => 3600, 'priority' => null],
            ],
            default => [],
        };

        if ($verificationCode) {
            $records[] = ['name' => '@', 'type' => 'TXT', 'content' => $verificationCode, 'ttl' => 3600, 'priority' => null];
        }

        return $records;
    }

    public function rebuildCurrentZone(): void
    {
        if (! $this->selectedDomainId) {
            return;
        }

        $domain = Domain::find($this->selectedDomainId);
        if (! $domain) {
            return;
        }

        try {
            $this->syncZoneFile($domain->domain);
            Notification::make()->title(__('Zone rebuilt for :domain', ['domain' => $domain->domain]))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function deleteCurrentZone(): void
    {
        if (! $this->selectedDomainId) {
            return;
        }

        $domain = Domain::find($this->selectedDomainId);
        if (! $domain) {
            return;
        }

        try {
            $this->getAgent()->send('dns.delete_zone', ['domain' => $domain->domain]);
            DnsRecord::where('domain_id', $this->selectedDomainId)->delete();

            Notification::make()->title(__('Zone deleted for :domain', ['domain' => $domain->domain]))->success()->send();

            $this->selectedDomainId = null;
            $this->pendingEdits = [];
            $this->pendingDeletes = [];
            $this->pendingAdds = [];
        } catch (Exception $e) {
            Notification::make()->title(__('Failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    protected function syncZoneFile(string $domain): void
    {
        $records = DnsRecord::whereHas('domain', fn ($q) => $q->where('domain', $domain))->get()->toArray();
        $settings = DnsSetting::getAll();
        $defaultIp = $settings['default_ip'] ?? ServerFacts::serverIp('127.0.0.1');

        $this->getAgent()->send('dns.sync_zone', [
            'domain' => $domain,
            'records' => $records,
            'ns1' => $settings['ns1'] ?? 'ns1.example.com',
            'ns2' => $settings['ns2'] ?? 'ns2.example.com',
            'admin_email' => $settings['admin_email'] ?? 'admin.example.com',
            'default_ip' => $defaultIp,
            'default_ipv6' => $settings['default_ipv6'] ?? null,
            'default_ttl' => $settings['default_ttl'] ?? 3600,
        ]);
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
