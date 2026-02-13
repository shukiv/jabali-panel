<?php

declare(strict_types=1);

namespace App\Livewire\Admin;

use App\Models\Setting;
use App\Services\Agent\AgentClient;
use Exception;
use Filament\Actions\Action;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Toggle;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Filament\Support\Contracts\TranslatableContentDriver;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Pagination\LengthAwarePaginator;
use Illuminate\Support\Str;
use Livewire\Attributes\On;
use Livewire\Component;

class SecurityWafPanel extends Component implements HasForms, HasTable, HasActions
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    public bool $wafInstalled = false;

    public array $wafFormData = [];

    public array $auditEntries = [];

    public bool $auditLoaded = false;

    public function makeFilamentTranslatableContentDriver(): ?TranslatableContentDriver
    {
        return null;
    }

    protected function getForms(): array
    {
        return ['wafForm'];
    }

    public function mount(): void
    {
        $this->wafInstalled = $this->detectWaf();
        $this->wafFormData = [
            'enabled' => Setting::get('waf_enabled', '0') === '1',
            'paranoia' => Setting::get('waf_paranoia', '1'),
            'audit_log' => Setting::get('waf_audit_log', '1') === '1',
        ];

        $this->loadAuditLogs(false);
    }

    protected function detectWaf(): bool
    {
        $paths = [
            '/etc/nginx/modsec/main.conf',
            '/etc/nginx/modsecurity.conf',
            '/etc/modsecurity/modsecurity.conf',
            '/etc/modsecurity/modsecurity.conf-recommended',
        ];

        foreach ($paths as $path) {
            if (file_exists($path)) {
                return true;
            }
        }

        return false;
    }

    public function wafForm(Schema $schema): Schema
    {
        return $schema
            ->statePath('wafFormData')
            ->schema([
                Section::make(__('WAF Settings'))
                    ->headerActions([
                        Action::make('saveWafSettings')
                            ->label(__('Save WAF Settings'))
                            ->icon('heroicon-o-check')
                            ->color('primary')
                            ->action('saveWafSettings'),
                    ])
                    ->extraAttributes(['class' => 'mb-8'])
                    ->schema([
                        Toggle::make('enabled')
                            ->label(__('Enable ModSecurity'))
                            ->disabled(fn () => ! $this->wafInstalled)
                            ->helperText(fn () => $this->wafInstalled ? null : __('ModSecurity is not installed. Install it to enable WAF.')),
                        Select::make('paranoia')
                            ->label(__('Paranoia Level'))
                            ->options([
                                '1' => '1 - Basic',
                                '2' => '2 - Moderate',
                                '3' => '3 - Strict',
                                '4' => '4 - Very Strict',
                            ])
                            ->default('1'),
                        Toggle::make('audit_log')
                            ->label(__('Enable Audit Log')),
                    ])
                    ->columns(2),
            ]);
    }

    public function saveWafSettings(): void
    {
        $data = $this->wafForm->getState();
        $requestedEnabled = ! empty($data['enabled']);
        if ($requestedEnabled && ! $this->wafInstalled) {
            $requestedEnabled = false;
        }

        Setting::set('waf_enabled', $requestedEnabled ? '1' : '0');
        Setting::set('waf_paranoia', (string) ($data['paranoia'] ?? '1'));
        Setting::set('waf_audit_log', ! empty($data['audit_log']) ? '1' : '0');
        $whitelistRules = $this->getWhitelistRules();

        try {
            $agent = new AgentClient;
            $agent->wafApplySettings(
                $requestedEnabled,
                (string) ($data['paranoia'] ?? '1'),
                ! empty($data['audit_log']),
                $whitelistRules
            );

            if (! $this->wafInstalled && ! empty($data['enabled'])) {
                Notification::make()
                    ->title(__('ModSecurity is not installed'))
                    ->body(__('WAF was disabled automatically. Install ModSecurity to enable it.'))
                    ->warning()
                    ->send();

                return;
            }

            Notification::make()
                ->title(__('WAF settings applied'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('WAF settings saved, but apply failed'))
                ->body($e->getMessage())
                ->warning()
                ->send();
        }
    }

    public function loadAuditLogs(bool $notify = true): void
    {
        try {
            $agent = new AgentClient;
            $response = $agent->wafAuditLogList();
            $entries = $response['entries'] ?? [];
            if (! is_array($entries)) {
                $entries = [];
            }

            $this->auditEntries = $this->normalizeAuditEntries($this->markWhitelisted($entries));
            $this->auditLoaded = true;

            if ($notify) {
                Notification::make()
                    ->title(__('WAF logs refreshed'))
                    ->success()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Failed to load WAF logs'))
                ->body($e->getMessage())
                ->warning()
                ->send();
        }
    }

    protected function getWhitelistRules(): array
    {
        $raw = Setting::get('waf_whitelist_rules', '[]');
        $rules = json_decode($raw, true);
        if (! is_array($rules)) {
            return [];
        }

        $changed = false;

        foreach ($rules as &$rule) {
            if (! is_array($rule)) {
                $rule = [];
                $changed = true;
                continue;
            }

            if (array_key_exists('enabled', $rule)) {
                unset($rule['enabled']);
                $changed = true;
            }

            $label = (string) ($rule['label'] ?? '');
            if ($label === '' || str_contains($label, '{rule}') || str_contains($label, ':rule')) {
                $rule['label'] = $this->defaultWhitelistLabel($rule);
                $changed = true;
            }
        }

        $rules = array_values(array_filter($rules, fn ($rule) => is_array($rule) && ($rule !== [])));

        if ($changed) {
            Setting::set('waf_whitelist_rules', json_encode($rules, JSON_UNESCAPED_SLASHES));
        }

        return $rules;
    }

    protected function defaultWhitelistLabel(array $rule): string
    {
        $ids = trim((string) ($rule['rule_ids'] ?? ''));
        if ($ids !== '') {
            return __('Rule :id', ['id' => $ids]);
        }

        $matchValue = trim((string) ($rule['match_value'] ?? ''));
        if ($matchValue !== '') {
            return $matchValue;
        }

        return __('Whitelist rule');
    }

    protected function markWhitelisted(array $entries): array
    {
        $rules = $this->getWhitelistRules();

        foreach ($entries as &$entry) {
            $entry['whitelisted'] = $this->matchesWhitelist($entry, $rules);
        }

        return $entries;
    }

    protected function normalizeAuditEntries(array $entries): array
    {
        return array_map(function (array $entry): array {
            $entry['__key'] = md5(implode('|', [
                (string) ($entry['timestamp'] ?? ''),
                (string) ($entry['rule_id'] ?? ''),
                (string) ($entry['uri'] ?? ''),
                (string) ($entry['remote_ip'] ?? ''),
                (string) ($entry['host'] ?? ''),
            ]));

            return $entry;
        }, $entries);
    }

    protected function matchesWhitelist(array $entry, array $rules): bool
    {
        $ruleId = (string) ($entry['rule_id'] ?? '');
        $uri = (string) ($entry['uri'] ?? '');
        $uriPath = $this->stripQueryString($uri);
        $host = (string) ($entry['host'] ?? '');
        $ip = (string) ($entry['remote_ip'] ?? '');

        foreach ($rules as $rule) {
            if (! is_array($rule)) {
                continue;
            }

            $idsRaw = (string) ($rule['rule_ids'] ?? '');
            $ids = preg_split('/[,\s]+/', $idsRaw, -1, PREG_SPLIT_NO_EMPTY) ?: [];
            $ids = array_map('trim', $ids);
            if ($ruleId !== '' && ! empty($ids) && ! in_array($ruleId, $ids, true)) {
                continue;
            }

            $matchType = (string) ($rule['match_type'] ?? '');
            $matchValue = (string) ($rule['match_value'] ?? '');

            if ($matchType === 'ip' && $matchValue !== '' && $this->ipMatches($ip, $matchValue)) {
                return true;
            }
            if ($matchType === 'uri_exact' && $matchValue !== '' && ($uri === $matchValue || $uriPath === $matchValue)) {
                return true;
            }
            if ($matchType === 'uri_prefix' && $matchValue !== '' && (str_starts_with($uri, $matchValue) || str_starts_with($uriPath, $matchValue))) {
                return true;
            }
            if ($matchType === 'host' && $matchValue !== '' && $host === $matchValue) {
                return true;
            }
        }

        return false;
    }

    protected function ruleMatchesEntry(array $rule, array $entry): bool
    {
        if (! is_array($rule)) {
            return false;
        }

        $ruleId = (string) ($entry['rule_id'] ?? '');
        $uri = (string) ($entry['uri'] ?? '');
        $uriPath = $this->stripQueryString($uri);
        $host = (string) ($entry['host'] ?? '');
        $ip = (string) ($entry['remote_ip'] ?? '');

        $idsRaw = (string) ($rule['rule_ids'] ?? '');
        $ids = preg_split('/[,\s]+/', $idsRaw, -1, PREG_SPLIT_NO_EMPTY) ?: [];
        $ids = array_map('trim', $ids);
        if ($ruleId !== '' && ! empty($ids) && ! in_array($ruleId, $ids, true)) {
            return false;
        }

        $matchType = (string) ($rule['match_type'] ?? '');
        $matchValue = (string) ($rule['match_value'] ?? '');

        if ($matchType === 'ip' && $matchValue !== '' && $this->ipMatches($ip, $matchValue)) {
            return true;
        }
        if ($matchType === 'uri_exact' && $matchValue !== '' && ($uri === $matchValue || $uriPath === $matchValue)) {
            return true;
        }
        if ($matchType === 'uri_prefix' && $matchValue !== '' && (str_starts_with($uri, $matchValue) || str_starts_with($uriPath, $matchValue))) {
            return true;
        }
        if ($matchType === 'host' && $matchValue !== '' && $host === $matchValue) {
            return true;
        }

        return false;
    }

    protected function ipMatches(string $ip, string $rule): bool
    {
        if ($ip === '' || $rule === '') {
            return false;
        }

        if (! str_contains($rule, '/')) {
            return $ip === $rule;
        }

        [$subnet, $bits] = array_pad(explode('/', $rule, 2), 2, null);
        $bits = is_numeric($bits) ? (int) $bits : null;
        if ($bits === null || $bits < 0 || $bits > 32) {
            return $ip === $rule;
        }

        $ipLong = ip2long($ip);
        $subnetLong = ip2long($subnet);
        if ($ipLong === false || $subnetLong === false) {
            return $ip === $rule;
        }

        $mask = -1 << (32 - $bits);

        return ($ipLong & $mask) === ($subnetLong & $mask);
    }

    protected function formatUriForDisplay(array $record): string
    {
        $host = (string) ($record['host'] ?? '');
        $uri = (string) ($record['uri'] ?? '');

        if ($host !== '' && $uri !== '') {
            return $host.$uri;
        }

        return $uri !== '' ? $uri : $host;
    }

    public function whitelistEntry(array $record): void
    {
        $rules = $this->getWhitelistRules();
        $matchType = 'uri_prefix';
        $rawUri = (string) ($record['uri'] ?? '');
        $matchValue = $this->stripQueryString($rawUri);
        if ($matchValue === '') {
            $matchType = 'ip';
            $matchValue = (string) ($record['remote_ip'] ?? '');
        }

        if ($matchValue === '' || empty($record['rule_id'])) {
            Notification::make()
                ->title(__('Unable to whitelist entry'))
                ->body(__('Missing URI/IP or rule ID for this entry.'))
                ->warning()
                ->send();
            return;
        }

        $rules[] = [
            'label' => __('Rule :rule', ['rule' => $record['rule_id'] ?? '']),
            'match_type' => $matchType,
            'match_value' => $matchValue,
            'rule_ids' => $record['rule_id'] ?? '',
        ];

        Setting::set('waf_whitelist_rules', json_encode(array_values($rules), JSON_UNESCAPED_SLASHES));

        try {
            $agent = new AgentClient;
            $agent->wafApplySettings(
                Setting::get('waf_enabled', '0') === '1',
                (string) Setting::get('waf_paranoia', '1'),
                Setting::get('waf_audit_log', '1') === '1',
                $rules
            );
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Whitelist saved, but apply failed'))
                ->body($e->getMessage())
                ->warning()
                ->send();
        }

        $this->loadAuditLogs(false);
        $this->resetTable();
        $this->dispatch('waf-whitelist-updated');
        $this->dispatch('waf-blocked-updated');

        Notification::make()
            ->title(__('Rule whitelisted'))
            ->success()
            ->send();
    }

    protected function stripQueryString(string $uri): string
    {
        $pos = strpos($uri, '?');
        if ($pos === false) {
            return $uri;
        }

        return substr($uri, 0, $pos);
    }

    public function removeWhitelistEntry(array $record): void
    {
        $rules = $this->getWhitelistRules();
        $beforeCount = count($rules);

        $rules = array_values(array_filter($rules, function (array $rule) use ($record): bool {
            return ! $this->ruleMatchesEntry($rule, $record);
        }));

        if (count($rules) === $beforeCount) {
            Notification::make()
                ->title(__('No matching whitelist rule found'))
                ->warning()
                ->send();
            return;
        }

        Setting::set('waf_whitelist_rules', json_encode(array_values($rules), JSON_UNESCAPED_SLASHES));

        try {
            $agent = new AgentClient;
            $agent->wafApplySettings(
                Setting::get('waf_enabled', '0') === '1',
                (string) Setting::get('waf_paranoia', '1'),
                Setting::get('waf_audit_log', '1') === '1',
                $rules
            );
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Whitelist updated, but apply failed'))
                ->body($e->getMessage())
                ->warning()
                ->send();
        }

        $this->loadAuditLogs(false);
        $this->resetTable();
        $this->dispatch('waf-whitelist-updated');
        $this->dispatch('waf-blocked-updated');

        Notification::make()
            ->title(__('Whitelist removed'))
            ->success()
            ->send();
    }

    public function table(Table $table): Table
    {
        return $table
            ->persistColumnsInSession(false)
            ->paginated([25, 50, 100])
            ->defaultPaginationPageOption(25)
            ->records(function (?array $filters, ?string $search, int|string $page, int|string $recordsPerPage, ?string $sortColumn, ?string $sortDirection) {
                if (! $this->auditLoaded) {
                    $this->loadAuditLogs(false);
                }

                $records = $this->auditEntries;

                $records = $this->filterRecords($records, $search);
                $records = $this->sortRecords($records, $sortColumn, $sortDirection);

                return $this->paginateRecords($records, $page, $recordsPerPage);
            })
            ->columns([
                TextColumn::make('timestamp')
                    ->label(__('Time'))
                    ->formatStateUsing(function (array $record): string {
                        $timestamp = (int) ($record['timestamp'] ?? 0);
                        return $timestamp > 0 ? date('Y-m-d H:i:s', $timestamp) : '';
                    })
                    ->sortable(),
                TextColumn::make('rule_id')
                    ->label(__('Rule ID'))
                    ->fontFamily('mono')
                    ->sortable()
                    ->searchable(),
                TextColumn::make('event_type')
                    ->label(__('Type'))
                    ->badge()
                    ->getStateUsing(function (array $record): string {
                        if (! empty($record['blocked'])) {
                            return __('Blocked');
                        }

                        $severity = (int) ($record['severity'] ?? 0);
                        if ($severity >= 4) {
                            return __('Error');
                        }

                        return __('Warning');
                    })
                    ->color(function (array $record): string {
                        if (! empty($record['blocked'])) {
                            return 'danger';
                        }

                        $severity = (int) ($record['severity'] ?? 0);
                        if ($severity >= 4) {
                            return 'warning';
                        }

                        return 'gray';
                    })
                    ->sortable()
                    ->toggleable(),
                TextColumn::make('message')
                    ->label(__('Message'))
                    ->wrap()
                    ->limit(80)
                    ->searchable(),
                TextColumn::make('uri')
                    ->label(__('URI'))
                    ->getStateUsing(fn (array $record): string => $this->formatUriForDisplay($record))
                    ->formatStateUsing(fn (string $state): string => Str::limit($state, 52, '…'))
                    ->tooltip(fn (array $record): string => $this->formatUriForDisplay($record))
                    ->copyable()
                    ->copyableState(fn (array $record): string => $this->formatUriForDisplay($record))
                    ->extraAttributes([
                        'class' => 'inline-block max-w-[240px] truncate',
                        'style' => 'max-width:240px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;display:inline-block;',
                    ])
                    ->wrap(false)
                    ->searchable(),
                TextColumn::make('remote_ip')
                    ->label(__('Source IP'))
                    ->fontFamily('mono')
                    ->copyable()
                    ->toggleable(isToggledHiddenByDefault: false),
                TextColumn::make('host')
                    ->label(__('Host'))
                    ->toggleable(isToggledHiddenByDefault: true),
                TextColumn::make('whitelisted')
                    ->label(__('Whitelisted'))
                    ->badge()
                    ->formatStateUsing(fn (array $record): string => ! empty($record['whitelisted']) ? __('Yes') : __('No'))
                    ->color(fn (array $record): string => ! empty($record['whitelisted']) ? 'success' : 'gray'),
            ])
            ->recordActions([
                \Filament\Actions\Action::make('whitelist')
                    ->label(__('Whitelist'))
                    ->icon('heroicon-o-check-badge')
                    ->color('primary')
                    ->visible(fn (array $record): bool => empty($record['whitelisted']))
                    ->action(fn (array $record) => $this->whitelistEntry($record)),
                \Filament\Actions\Action::make('removeWhitelist')
                    ->label(__('Remove whitelist'))
                    ->icon('heroicon-o-x-mark')
                    ->color('danger')
                    ->visible(fn (array $record): bool => ! empty($record['whitelisted']))
                    ->requiresConfirmation()
                    ->action(fn (array $record) => $this->removeWhitelistEntry($record)),
            ])
            ->emptyStateHeading(__('No blocked rules found'))
            ->emptyStateDescription(__('No ModSecurity denials found in the audit log.'))
            ->headerActions([
                \Filament\Actions\Action::make('refresh')
                    ->label(__('Refresh Logs'))
                    ->icon('heroicon-o-arrow-path')
                    ->action(fn () => $this->loadAuditLogs()),
            ]);
    }

    #[On('waf-blocked-updated')]
    public function refreshBlockedTable(): void
    {
        $this->loadAuditLogs(false);
        $this->resetTable();
    }



    protected function filterRecords(array $records, ?string $search): array
    {
        if (! $search) {
            return $records;
        }

        return array_values(array_filter($records, function (array $record) use ($search) {
            $haystack = implode(' ', array_filter([
                (string) ($record['rule_id'] ?? ''),
                (string) ($record['message'] ?? ''),
                (string) ($record['uri'] ?? ''),
                (string) ($record['remote_ip'] ?? ''),
                (string) ($record['host'] ?? ''),
            ]));

            return str_contains(Str::lower($haystack), Str::lower($search));
        }));
    }

    protected function sortRecords(array $records, ?string $sortColumn, ?string $sortDirection): array
    {
        $direction = $sortDirection === 'asc' ? 'asc' : 'desc';

        if (! $sortColumn) {
            return $records;
        }

        usort($records, function (array $a, array $b) use ($sortColumn, $direction): int {
            $aValue = $a[$sortColumn] ?? null;
            $bValue = $b[$sortColumn] ?? null;

            if (is_numeric($aValue) && is_numeric($bValue)) {
                $result = (float) $aValue <=> (float) $bValue;
            } else {
                $result = strcmp((string) $aValue, (string) $bValue);
            }

            return $direction === 'asc' ? $result : -$result;
        });

        return $records;
    }

    protected function paginateRecords(array $records, int|string $page, int|string $recordsPerPage): LengthAwarePaginator
    {
        $page = max(1, (int) $page);
        $perPage = max(1, (int) $recordsPerPage);

        $total = count($records);
        $items = array_slice($records, ($page - 1) * $perPage, $perPage);

        return new LengthAwarePaginator(
            $items,
            $total,
            $perPage,
            $page,
            [
                'path' => request()->url(),
                'pageName' => $this->getTablePaginationPageName(),
            ],
        );
    }

    public function render()
    {
        return view('livewire.admin.security-waf-panel');
    }
}
