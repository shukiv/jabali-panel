<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Settings;

use App\Models\Domain;
use App\Services\Agent\AgentClient;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class DnssecTable extends Component implements HasTable, HasSchemas, HasActions
{
    use InteractsWithTable;
    use InteractsWithSchemas;
    use InteractsWithActions;

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    protected function getAgent(): AgentClient
    {
        return new AgentClient();
    }

    protected function getDnssecStatus(string $domain): array
    {
        try {
            $result = $this->getAgent()->dnsGetDnssecStatus($domain);
            if ($result['success'] ?? false) {
                return [
                    'enabled' => $result['enabled'] ?? false,
                    'keys' => $result['keys'] ?? [],
                    'message' => $result['message'] ?? '',
                ];
            }
        } catch (\Exception $e) {
            // Silently fail
        }

        return ['enabled' => false, 'keys' => [], 'message' => ''];
    }

    protected function getDsRecords(string $domain): ?array
    {
        try {
            $result = $this->getAgent()->dnsGetDsRecords($domain);
            if ($result['success'] ?? false) {
                return $result;
            }
        } catch (\Exception $e) {
            // Silently fail
        }

        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(
                Domain::query()->orderBy('domain')
            )
            ->columns([
                TextColumn::make('domain')
                    ->label(__('Domain'))
                    ->searchable()
                    ->sortable(),
                TextColumn::make('user.username')
                    ->label(__('Owner'))
                    ->sortable(),
                IconColumn::make('dnssec_status')
                    ->label(__('DNSSEC'))
                    ->state(function (Domain $record): bool {
                        $status = $this->getDnssecStatus($record->domain);
                        return $status['enabled'] ?? false;
                    })
                    ->boolean()
                    ->trueIcon('heroicon-o-shield-check')
                    ->falseIcon('heroicon-o-shield-exclamation')
                    ->trueColor('success')
                    ->falseColor('gray'),
                TextColumn::make('keys_info')
                    ->label(__('Keys'))
                    ->state(function (Domain $record): string {
                        $status = $this->getDnssecStatus($record->domain);
                        if (!($status['enabled'] ?? false)) {
                            return '-';
                        }
                        $keys = $status['keys'] ?? [];
                        if (empty($keys)) {
                            return '-';
                        }
                        $ksk = collect($keys)->firstWhere('type', 'KSK');
                        $zsk = collect($keys)->firstWhere('type', 'ZSK');
                        $info = [];
                        if ($ksk) {
                            $info[] = "KSK: {$ksk['key_id']}";
                        }
                        if ($zsk) {
                            $info[] = "ZSK: {$zsk['key_id']}";
                        }
                        return implode(', ', $info) ?: '-';
                    })
                    ->fontFamily('mono')
                    ->color('gray'),
            ])
            ->actions([
                Action::make('enable')
                    ->label(__('Enable'))
                    ->icon('heroicon-o-shield-check')
                    ->color('success')
                    ->requiresConfirmation()
                    ->modalHeading(fn (Domain $record): string => __('Enable DNSSEC for :domain', ['domain' => $record->domain]))
                    ->modalDescription(__('This will generate DNSSEC keys and sign the zone. After enabling, you must add the DS record to your domain registrar.'))
                    ->modalIcon('heroicon-o-shield-check')
                    ->modalIconColor('success')
                    ->visible(function (Domain $record): bool {
                        $status = $this->getDnssecStatus($record->domain);
                        return !($status['enabled'] ?? false);
                    })
                    ->action(function (Domain $record): void {
                        try {
                            $result = $this->getAgent()->dnsEnableDnssec($record->domain);
                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title(__('DNSSEC Enabled'))
                                    ->body(__('DNSSEC has been enabled for :domain. Add the DS record to your registrar to complete setup.', ['domain' => $record->domain]))
                                    ->success()
                                    ->send();
                                $this->resetTable();
                            } else {
                                throw new \Exception($result['error'] ?? __('Unknown error'));
                            }
                        } catch (\Exception $e) {
                            Notification::make()
                                ->title(__('Failed to enable DNSSEC'))
                                ->body($e->getMessage())
                                ->danger()
                                ->send();
                        }
                    }),
                Action::make('viewDs')
                    ->label(__('DS Record'))
                    ->icon('heroicon-o-clipboard-document')
                    ->color('gray')
                    ->visible(function (Domain $record): bool {
                        $status = $this->getDnssecStatus($record->domain);
                        return $status['enabled'] ?? false;
                    })
                    ->modalHeading(fn (Domain $record): string => __('DS Records for :domain', ['domain' => $record->domain]))
                    ->modalDescription(__('Add one of these DS records to your domain registrar to complete DNSSEC setup.'))
                    ->modalContent(function (Domain $record) {
                        $dsRecords = $this->getDsRecords($record->domain);
                        return view('filament.admin.components.dnssec-ds-records', ['dsRecords' => $dsRecords, 'domain' => $record->domain]);
                    })
                    ->modalSubmitAction(false)
                    ->modalCancelActionLabel(__('Close')),
                Action::make('disable')
                    ->label(__('Disable'))
                    ->icon('heroicon-o-shield-exclamation')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(fn (Domain $record): string => __('Disable DNSSEC for :domain', ['domain' => $record->domain]))
                    ->modalDescription(__('Are you sure? Remember to remove the DS records from your registrar FIRST to avoid DNS resolution issues.'))
                    ->modalIcon('heroicon-o-exclamation-triangle')
                    ->modalIconColor('danger')
                    ->visible(function (Domain $record): bool {
                        $status = $this->getDnssecStatus($record->domain);
                        return $status['enabled'] ?? false;
                    })
                    ->action(function (Domain $record): void {
                        try {
                            $result = $this->getAgent()->dnsDisableDnssec($record->domain);
                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title(__('DNSSEC Disabled'))
                                    ->body(__('DNSSEC has been disabled for :domain.', ['domain' => $record->domain]))
                                    ->success()
                                    ->send();
                                $this->resetTable();
                            } else {
                                throw new \Exception($result['error'] ?? __('Unknown error'));
                            }
                        } catch (\Exception $e) {
                            Notification::make()
                                ->title(__('Failed to disable DNSSEC'))
                                ->body($e->getMessage())
                                ->danger()
                                ->send();
                        }
                    }),
            ])
            ->striped()
            ->paginated([10, 25, 50])
            ->emptyStateHeading(__('No domains'))
            ->emptyStateDescription(__('Add domains to manage their DNSSEC settings.'))
            ->emptyStateIcon('heroicon-o-globe-alt');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
