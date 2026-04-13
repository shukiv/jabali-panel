<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use Filament\Actions\Action;
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

class QuarantineTable extends Component implements HasActions, HasSchemas, HasTable
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
            ->records(fn () => $this->client()->get('/quarantine') ?? [])
            ->columns([
                TextColumn::make('original_path')
                    ->label(__('Original Path'))
                    ->limit(50),
                TextColumn::make('username')
                    ->label(__('Username')),
                TextColumn::make('reason')
                    ->label(__('Reason'))
                    ->limit(40),
                TextColumn::make('timestamp')
                    ->label(__('Time'))
                    ->since(),
            ])
            ->recordActions([
                Action::make('restore')
                    ->label(__('Restore'))
                    ->icon('heroicon-o-arrow-uturn-left')
                    ->requiresConfirmation()
                    ->action(function (array $record): void {
                        $result = $this->client()->post("/quarantine/{$record['id']}/restore");

                        Notification::make()
                            ->title($result ? __('File restored') : __('Failed to restore file'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function (array $record): void {
                        $result = $this->client()->delete("/quarantine/{$record['id']}");

                        Notification::make()
                            ->title($result ? __('File deleted') : __('Failed to delete file'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
            ])
            ->bulkActions([
                BulkAction::make('restore')
                    ->label(__('Restore Selected'))
                    ->icon('heroicon-o-arrow-uturn-left')
                    ->color('success')
                    ->requiresConfirmation()
                    ->deselectRecordsAfterCompletion()
                    ->action(function (Collection $records): void {
                        $count = 0;
                        foreach ($records as $record) {
                            $result = $this->client()->post("/quarantine/{$record['id']}/restore");
                            if ($result) {
                                $count++;
                            }
                        }
                        Notification::make()
                            ->title(__(':count files restored', ['count' => $count]))
                            ->success()
                            ->send();
                    }),
                BulkAction::make('delete')
                    ->label(__('Delete Selected'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->deselectRecordsAfterCompletion()
                    ->action(function (Collection $records): void {
                        $count = 0;
                        foreach ($records as $record) {
                            $result = $this->client()->delete("/quarantine/{$record['id']}");
                            if ($result) {
                                $count++;
                            }
                        }
                        Notification::make()
                            ->title(__(':count files deleted', ['count' => $count]))
                            ->success()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No quarantined files'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
