<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use App\JabaliSecurity\Pages\Security;
use Filament\Actions\Action;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\TextColumn;
use Filament\Actions\BulkAction;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Support\Collection;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Livewire\Component;

class BruteforceBlockedTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    protected function client(): JabaliSecurityClient
    {
        return JabaliSecurityClient::getInstance();
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => array_map(
                fn ($ip) => is_string($ip) ? ['ip' => $ip] : $ip,
                $this->client()->get('/bruteforce/blocked')['blocked_ips'] ?? [],
            ))
            ->columns([
                TextColumn::make('ip')
                    ->label(__('IP Address')),
            ])
            ->actions([
                \Filament\Actions\Action::make('whitelist')
                    ->label(__('Whitelist'))
                    ->icon('heroicon-o-shield-check')
                    ->color('success')
                    ->requiresConfirmation()
                    ->modalDescription(__('This will unblock the IP and add it to the whitelist so it is never blocked again.'))
                    ->action(function ($record): void {
                        $ip = $record['ip'] ?? '';
                        if ($ip) {
                            $this->client()->delete('/block/' . urlencode($ip));
                            $this->client()->post('/bruteforce/whitelist', ['ip' => $ip]);
                            Notification::make()->title(__('IP whitelisted: :ip', ['ip' => $ip]))->success()->send();
                            $this->redirect(Security::tabUrl('defense', 'bruteforce'), navigate: true);
                        }
                    }),
                \Filament\Actions\Action::make('unblock')
                    ->label(__('Unblock'))
                    ->icon('heroicon-o-lock-open')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function ($record): void {
                        $ip = $record['ip'] ?? '';
                        if ($ip) {
                            $this->client()->delete('/block/' . urlencode($ip));
                            Notification::make()->title(__('IP unblocked: :ip', ['ip' => $ip]))->success()->send();
                            $this->redirect(Security::tabUrl('defense', 'bruteforce'), navigate: true);
                        }
                    }),
            ])
            ->headerActions([])
            ->bulkActions([
                BulkAction::make('unblock')
                    ->label(__('Unblock Selected'))
                    ->icon('heroicon-o-lock-open')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->deselectRecordsAfterCompletion()
                    ->action(function (Collection $records): void {
                        $count = 0;
                        foreach ($records as $record) {
                            $ip = $record['ip'] ?? '';
                            if (! $ip) {
                                continue;
                            }
                            $result = $this->client()->delete('/block/' . urlencode($ip));
                            if ($result) {
                                $count++;
                            }
                        }
                        Notification::make()
                            ->title(__(':count IPs unblocked', ['count' => $count]))
                            ->success()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No blocked IPs'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
