<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
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

class BlocklistTable extends Component implements HasActions, HasSchemas, HasTable
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
            ->records(function () {
                try {
                    $response = $this->client()->get('/blocklist');
                    return $response['blocked_ips'] ?? $response ?? [];
                } catch (\Exception) {
                    return [];
                }
            })
            ->columns([
                TextColumn::make('ip')
                    ->label(__('IP Address'))
                    ->copyable(),
                TextColumn::make('reason')
                    ->label(__('Reason')),
                TextColumn::make('blocked_at')
                    ->label(__('Blocked'))
                    ->since(),
                TextColumn::make('expires_at')
                    ->label(__('Expires'))
                    ->placeholder(__('permanent')),
                TextColumn::make('blocked_by')
                    ->label(__('Blocked By'))
                    ->badge()
                    ->color('gray'),
            ])
            ->headerActions([
                Action::make('block')
                    ->label(__('Block IP'))
                    ->icon('heroicon-o-no-symbol')
                    ->form([
                        TextInput::make('ip')
                            ->label(__('IP Address'))
                            ->required()
                            ->ip(),
                        TextInput::make('reason')
                            ->label(__('Reason'))
                            ->default('manual'),
                        TextInput::make('duration')
                            ->label(__('Duration (seconds)'))
                            ->numeric(),
                    ])
                    ->action(function (array $data): void {
                        $validated = validator($data, [
                            'ip' => 'required|ip',
                            'reason' => 'nullable|string|max:255',
                            'duration' => 'nullable|numeric|min:1',
                        ])->validate();
                        $result = $this->client()->post('/block', $validated);

                        Notification::make()
                            ->title($result ? __('IP blocked') : __('Failed to block IP'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
            ])
            ->recordActions([
                Action::make('unblock')
                    ->label(__('Unblock'))
                    ->icon('heroicon-o-lock-open')
                    ->requiresConfirmation()
                    ->action(function (array $record): void {
                        $result = $this->client()->delete('/block/'.urlencode($record['ip']));

                        Notification::make()
                            ->title($result ? __('IP unblocked') : __('Failed to unblock IP'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
            ])
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
                            $result = $this->client()->delete('/block/' . urlencode($record['ip']));
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
