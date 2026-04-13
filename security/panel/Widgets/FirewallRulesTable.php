<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use Filament\Actions\Action;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\IconColumn;
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

class FirewallRulesTable extends Component implements HasActions, HasSchemas, HasTable
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
            ->records(fn () => $this->client()->get('/firewall/ufw/status')['rules'] ?? [])
            ->columns([
                TextColumn::make('number')
                    ->label(__('Number')),
                TextColumn::make('to')
                    ->label(__('To')),
                TextColumn::make('action')
                    ->label(__('Action'))
                    ->badge()
                    ->color(fn (string $state): string => match ($state) {
                        'ALLOW' => 'success',
                        'DENY' => 'danger',
                        'REJECT' => 'warning',
                        default => 'gray',
                    }),
                TextColumn::make('from_ip')
                    ->label(__('From')),
                TextColumn::make('direction')
                    ->label(__('Direction')),
                TextColumn::make('v6')
                    ->label(__('IP Version'))
                    ->badge()
                    ->formatStateUsing(fn ($state) => $state ? 'IPv6' : 'IPv4')
                    ->color(fn ($state) => $state ? 'info' : 'gray'),
            ])
            ->headerActions([
                Action::make('add_rule')
                    ->label(__('Add Rule'))
                    ->icon('heroicon-o-plus')
                    ->form([
                        Select::make('action')
                            ->label(__('Action'))
                            ->options([
                                'allow' => __('Allow'),
                                'deny' => __('Deny'),
                                'reject' => __('Reject'),
                                'limit' => __('Limit'),
                            ])
                            ->required(),
                        TextInput::make('port')
                            ->label(__('Port'))
                            ->required()
                            ->numeric()
                            ->minValue(1)
                            ->maxValue(65535),
                        Select::make('protocol')
                            ->label(__('Protocol'))
                            ->options([
                                'tcp' => 'TCP',
                                'udp' => 'UDP',
                                'any' => __('Any'),
                            ])
                            ->required(),
                        TextInput::make('from_ip')
                            ->label(__('From IP')),
                        TextInput::make('comment')
                            ->label(__('Comment')),
                    ])
                    ->action(function (array $data): void {
                        $validated = validator($data, [
                            'port' => 'required|string|max:10',
                            'action' => 'required|in:allow,deny,reject',
                            'protocol' => 'required|in:tcp,udp,any',
                            'from_ip' => 'nullable|ip',
                            'comment' => 'nullable|string|max:255',
                        ])->validate();
                        $result = $this->client()->post('/firewall/ufw/rules', $validated);

                        Notification::make()
                            ->title($result ? __('Rule added') : __('Failed to add rule'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
            ])
            ->recordActions([
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function (array $record): void {
                        $result = $this->client()->delete("/firewall/ufw/rules/{$record['number']}");

                        Notification::make()
                            ->title($result ? __('Rule deleted') : __('Failed to delete rule'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
            ])
            ->bulkActions([
                BulkAction::make('delete_rules')
                    ->label(__('Delete Selected Rules'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->deselectRecordsAfterCompletion()
                    ->action(function (Collection $records): void {
                        $count = 0;
                        // Delete in reverse order (highest number first) because UFW renumbers after each delete
                        $sorted = $records->sortByDesc('number');
                        foreach ($sorted as $record) {
                            $result = $this->client()->delete("/firewall/ufw/rules/{$record['number']}");
                            if ($result) {
                                $count++;
                            }
                        }
                        Notification::make()
                            ->title(__(':count rules deleted', ['count' => $count]))
                            ->success()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No firewall rules'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
