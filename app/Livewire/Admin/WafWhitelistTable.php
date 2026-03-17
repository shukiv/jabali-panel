<?php

declare(strict_types=1);

namespace App\Livewire\Admin;

use App\Models\Setting;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Support\Contracts\TranslatableContentDriver;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Support\Arr;
use Livewire\Attributes\On;
use Livewire\Component;

class WafWhitelistTable extends Component implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    #[On('waf-whitelist-updated')]
    public function refreshWhitelist(): void
    {
        $this->resetTable();
    }

    public function render()
    {
        return view('livewire.admin.waf-whitelist-table');
    }

    public function makeFilamentTranslatableContentDriver(): ?TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getWhitelistRules())
            ->paginated([10, 25, 50])
            ->defaultPaginationPageOption(10)
            ->columns([
                TextColumn::make('label')
                    ->label(__('Name'))
                    ->wrap(),
                TextColumn::make('match_type')
                    ->label(__('Match Type'))
                    ->formatStateUsing(fn (string $state): string => $this->matchTypeLabel($state))
                    ->toggleable(),
                TextColumn::make('match_value')
                    ->label(__('Match Value'))
                    ->wrap()
                    ->copyable(),
                TextColumn::make('rule_ids')
                    ->label(__('Rule IDs'))
                    ->fontFamily('mono')
                    ->copyable(),
            ])
            ->headerActions([
                Action::make('add')
                    ->label(__('Add Custom Rule'))
                    ->icon('heroicon-o-plus')
                    ->modalHeading(__('Add Whitelist Rule'))
                    ->form($this->whitelistRuleForm())
                    ->action(function (array $data): void {
                        $rules = $this->getWhitelistRules();
                        $rules[] = $this->normalizeRule($data);
                        $this->persistWhitelistRules($rules);

                        Notification::make()
                            ->title(__('Whitelist rule added'))
                            ->success()
                            ->send();
                    }),
            ])
            ->actions([
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function (array $record): void {
                        $rules = array_values(array_filter($this->getWhitelistRules(), function (array $rule) use ($record) {
                            return ($rule['__key'] ?? '') !== ($record['__key'] ?? '');
                        }));

                        $this->persistWhitelistRules($rules);

                        Notification::make()
                            ->title(__('Whitelist rule removed'))
                            ->success()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No whitelist rules'))
            ->emptyStateDescription(__('Add a custom whitelist rule to allow trusted traffic.'));
    }

    protected function whitelistRuleForm(): array
    {
        return [
            TextInput::make('label')
                ->label(__('Name'))
                ->placeholder(__('Rule 942100'))
                ->maxLength(80),
            Select::make('match_type')
                ->label(__('Match Type'))
                ->options([
                    'ip' => __('IP Address or CIDR'),
                    'uri_exact' => __('Exact URI'),
                    'uri_prefix' => __('URI Prefix'),
                    'host' => __('Host Header'),
                ])
                ->required(),
            TextInput::make('match_value')
                ->label(__('Match Value'))
                ->placeholder(__('Example: 203.0.113.10 or /wp-admin/admin-ajax.php'))
                ->required(),
            TextInput::make('rule_ids')
                ->label(__('Rule IDs'))
                ->placeholder(__('Example: 942100,949110'))
                ->helperText(__('Comma-separated ModSecurity rule IDs to disable for matches.'))
                ->required(),
        ];
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

            $rule['label'] = $this->normalizeLabel($rule);
            $rule['__key'] = $this->ruleKey($rule);
        }

        $rules = array_values(array_filter($rules, fn ($rule) => is_array($rule) && ($rule !== [])));

        if ($changed) {
            Setting::set('waf_whitelist_rules', json_encode($this->stripIds($rules), JSON_UNESCAPED_SLASHES));
        }

        return $rules;
    }

    protected function normalizeRule(array $data): array
    {
        $rule = [
            'label' => $data['label'] ?? null,
            'match_type' => $data['match_type'] ?? '',
            'match_value' => $data['match_value'] ?? '',
            'rule_ids' => $data['rule_ids'] ?? '',
        ];

        $rule['label'] = $this->normalizeLabel($rule);
        $rule['__key'] = $this->ruleKey($rule);

        return $rule;
    }

    protected function normalizeLabel(array $rule): string
    {
        $label = trim((string) ($rule['label'] ?? ''));
        if ($label !== '' && ! str_contains($label, '{rule}') && ! str_contains($label, ':rule')) {
            return $label;
        }

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

    protected function ruleKey(array $rule): string
    {
        return md5(json_encode(Arr::only($rule, ['label', 'match_type', 'match_value', 'rule_ids'])));
    }

    protected function stripIds(array $rules): array
    {
        return array_map(function (array $rule): array {
            return Arr::only($rule, ['label', 'match_type', 'match_value', 'rule_ids']);
        }, $rules);
    }

    protected function persistWhitelistRules(array $rules): void
    {
        $rules = $this->stripIds($rules);
        Setting::set('waf_whitelist_rules', json_encode(array_values($rules), JSON_UNESCAPED_SLASHES));

        try {
            $agent = app(AgentClient::class);
            $agent->wafApplySettings(
                Setting::get('waf_enabled', '0') === '1',
                (string) Setting::get('waf_paranoia', '1'),
                Setting::get('waf_audit_log', '1') === '1',
                $rules
            );
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Whitelist saved, but apply failed'))
                ->body(SafeError::message($e))
                ->warning()
                ->send();
        }

        $this->dispatch('waf-whitelist-updated');
        $this->dispatch('waf-blocked-updated');
    }

    protected function matchTypeLabel(string $type): string
    {
        return match ($type) {
            'ip' => __('IP Address or CIDR'),
            'uri_exact' => __('Exact URI'),
            'uri_prefix' => __('URI Prefix'),
            'host' => __('Host Header'),
            default => $type,
        };
    }
}
