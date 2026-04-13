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

class WhitelistTable extends Component implements HasActions, HasSchemas, HasTable
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
                fn ($ip) => ['ip' => $ip],
                $this->client()->get('/bruteforce/whitelist')['whitelist'] ?? [],
            ))
            ->columns([
                TextColumn::make('ip')
                    ->label(__('IP Address')),
            ])
            ->actions([
                Action::make('block')
                    ->label(__('Block'))
                    ->icon('heroicon-o-no-symbol')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalDescription(__('This will remove the IP from the whitelist and block it.'))
                    ->action(function ($record): void {
                        $ip = $record['ip'] ?? '';
                        if ($ip) {
                            $this->client()->delete('/bruteforce/whitelist/' . urlencode($ip));
                            $this->client()->post('/block', ['ip' => $ip, 'reason' => 'Moved from whitelist']);
                            Notification::make()->title(__('IP blocked: :ip', ['ip' => $ip]))->success()->send();
                            $this->redirect(Security::tabUrl('defense', 'bruteforce'), navigate: true);
                        }
                    }),
                Action::make('remove')
                    ->label(__('Remove'))
                    ->icon('heroicon-o-trash')
                    ->color('gray')
                    ->requiresConfirmation()
                    ->action(function ($record): void {
                        $ip = $record['ip'] ?? '';
                        if ($ip) {
                            $this->client()->delete('/bruteforce/whitelist/' . urlencode($ip));
                            Notification::make()->title(__('Removed :ip from whitelist', ['ip' => $ip]))->success()->send();
                            $this->redirect(Security::tabUrl('defense', 'bruteforce'), navigate: true);
                        }
                    }),
            ])
            ->headerActions([
                Action::make('add')
                    ->label(__('Add IP'))
                    ->icon('heroicon-o-plus')
                    ->form([
                        TextInput::make('ip')
                            ->label(__('IP Address'))
                            ->required()
                            ->ip(),
                    ])
                    ->action(function (array $data): void {
                        $validated = validator($data, ['ip' => 'required|ip'])->validate();
                        $result = $this->client()->post('/bruteforce/whitelist', $validated);
                        Notification::make()
                            ->title($result ? __('IP whitelisted') : __('Failed to whitelist IP'))
                            ->{($result ? 'success' : 'danger')}()
                            ->send();
                        $this->redirect(Security::tabUrl('defense', 'bruteforce'), navigate: true);
                    }),
            ])
            ->bulkActions([
                BulkAction::make('remove')
                    ->label(__('Remove Selected'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->deselectRecordsAfterCompletion()
                    ->action(function (Collection $records): void {
                        $count = 0;
                        foreach ($records as $record) {
                            $ip = $record['ip'] ?? '';
                            if ($ip) {
                                $this->client()->delete('/bruteforce/whitelist/' . urlencode($ip));
                                $count++;
                            }
                        }
                        Notification::make()->title(__(':count IPs removed from whitelist', ['count' => $count]))->success()->send();
                    }),
            ])
            ->emptyStateHeading(__('No whitelisted IPs'))
            ->emptyStateDescription(__('Add IPs that should never be blocked'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
