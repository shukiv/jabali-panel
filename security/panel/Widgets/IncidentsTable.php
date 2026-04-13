<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use Filament\Actions\Action;
use Filament\Forms\Components\Textarea;
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

class IncidentsTable extends Component implements HasActions, HasSchemas, HasTable
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
            ->records(fn () => $this->client()->get('/incidents', ['limit' => 100]) ?? [])
            ->columns([
                TextColumn::make('path')
                    ->label(__('Path'))
                    ->limit(50),
                TextColumn::make('username')
                    ->label(__('Username')),
                TextColumn::make('severity')
                    ->label(__('Severity'))
                    ->badge()
                    ->color(fn (string $state): string => match (true) {
                        in_array($state, ['critical', 'high']) => 'danger',
                        $state === 'medium' => 'warning',
                        default => 'info',
                    }),
                TextColumn::make('total_score')
                    ->label(__('Score')),
                TextColumn::make('action_taken')
                    ->label(__('Action'))
                    ->badge()
                    ->color('gray'),
                IconColumn::make('resolved')
                    ->label(__('Resolved'))
                    ->boolean(),
                TextColumn::make('timestamp')
                    ->label(__('Time'))
                    ->since(),
            ])
            ->recordActions([
                Action::make('resolve')
                    ->label(__('Resolve'))
                    ->icon('heroicon-o-check-circle')
                    ->form([
                        Textarea::make('notes')
                            ->label(__('Notes')),
                    ])
                    ->action(function (array $record, array $data): void {
                        $result = $this->client()->post("/incidents/{$record['id']}/resolve", $data);

                        Notification::make()
                            ->title($result ? __('Incident resolved') : __('Failed to resolve incident'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
            ])
            ->bulkActions([
                BulkAction::make('resolve')
                    ->label(__('Resolve Selected'))
                    ->icon('heroicon-o-check')
                    ->color('success')
                    ->requiresConfirmation()
                    ->deselectRecordsAfterCompletion()
                    ->action(function (Collection $records): void {
                        $count = 0;
                        foreach ($records as $record) {
                            $result = $this->client()->post("/incidents/{$record['id']}/resolve");
                            if ($result) {
                                $count++;
                            }
                        }
                        Notification::make()
                            ->title(__(':count incidents resolved', ['count' => $count]))
                            ->success()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No incidents'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
