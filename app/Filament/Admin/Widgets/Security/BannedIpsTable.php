<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Security;

use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class BannedIpsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $jails = [];

    protected function reloadBannedIps(): void
    {
        try {
            $agent = app(AgentClient::class);
            $result = $agent->send('fail2ban.status', []);
            if ($result['success'] ?? false) {
                $this->jails = $result['jails'] ?? [];
                $this->resetTable();
            }
        } catch (\Exception $e) {
            // Keep existing data on error
        }
    }

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    protected function getBannedIpRecords(): array
    {
        $records = [];
        foreach ($this->jails as $jail) {
            $jailName = $jail['name'] ?? '';
            $bannedIps = $jail['banned_ips'] ?? [];
            foreach ($bannedIps as $ip) {
                $records[] = [
                    'id' => "{$jailName}_{$ip}",
                    'jail' => $jailName,
                    'ip' => $ip,
                ];
            }
        }

        return $records;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getBannedIpRecords())
            ->columns([
                TextColumn::make('jail')
                    ->label(__('Service'))
                    ->badge()
                    ->color('danger')
                    ->formatStateUsing(fn (string $state): string => ucfirst($state)),
                TextColumn::make('ip')
                    ->label(__('IP Address'))
                    ->icon('heroicon-o-globe-alt')
                    ->fontFamily('mono')
                    ->copyable()
                    ->searchable(),
            ])
            ->actions([
                Action::make('unban')
                    ->label(__('Unban'))
                    ->icon('heroicon-o-lock-open')
                    ->color('success')
                    ->requiresConfirmation()
                    ->modalHeading(__('Unban IP Address'))
                    ->modalDescription(fn (array $record): string => __('Are you sure you want to unban :ip from :jail?', [
                        'ip' => $record['ip'] ?? '',
                        'jail' => ucfirst($record['jail'] ?? ''),
                    ]))
                    ->action(function (array $record): void {
                        try {
                            $agent = app(AgentClient::class);
                            $result = $agent->send('fail2ban.unban_ip', [
                                'jail' => $record['jail'],
                                'ip' => $record['ip'],
                            ]);

                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title(__('IP Unbanned'))
                                    ->body(__(':ip has been unbanned from :jail', [
                                        'ip' => $record['ip'],
                                        'jail' => ucfirst($record['jail']),
                                    ]))
                                    ->success()
                                    ->send();

                                // Reload banned IPs data directly
                                $this->reloadBannedIps();
                            } else {
                                throw new \Exception($result['error'] ?? __('Failed to unban IP'));
                            }
                        } catch (\Exception $e) {
                            Notification::make()
                                ->title(__('Error'))
                                ->body(SafeError::message($e))
                                ->danger()
                                ->send();
                        }
                    }),
            ])
            ->striped()
            ->emptyStateHeading(__('No banned IPs'))
            ->emptyStateDescription(__('No IP addresses are currently banned.'))
            ->emptyStateIcon('heroicon-o-check-circle');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
