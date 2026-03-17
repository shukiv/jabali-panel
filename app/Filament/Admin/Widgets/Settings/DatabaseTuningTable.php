<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Settings;

use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Contracts\TranslatableContentDriver;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class DatabaseTuningTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $variables = [];

    public function makeFilamentTranslatableContentDriver(): ?TranslatableContentDriver
    {
        return null;
    }

    public function mount(): void
    {
        $this->loadVariables();
    }

    protected function getAgent(): AgentClient
    {
        return app(AgentClient::class);
    }

    public function loadVariables(): void
    {
        $names = [
            'innodb_buffer_pool_size',
            'max_connections',
            'tmp_table_size',
            'max_heap_table_size',
            'innodb_log_file_size',
            'innodb_flush_log_at_trx_commit',
        ];

        try {
            $result = $this->getAgent()->databaseGetVariables($names);
            if (! ($result['success'] ?? false)) {
                throw new \RuntimeException($result['error'] ?? __('Unable to load variables'));
            }

            $this->variables = collect($result['variables'] ?? [])->map(function (array $row) {
                return [
                    'name' => $row['name'] ?? '',
                    'value' => $row['value'] ?? '',
                ];
            })->toArray();
        } catch (\Exception $e) {
            $this->variables = [];
            Notification::make()
                ->title(__('Unable to load database variables'))
                ->body(SafeError::message($e))
                ->warning()
                ->send();
        }

        $this->resetTable();
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->variables)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Variable'))
                    ->fontFamily('mono'),
                TextColumn::make('value')
                    ->label(__('Current Value'))
                    ->fontFamily('mono'),
            ])
            ->recordActions([
                Action::make('update')
                    ->label(__('Update'))
                    ->icon('heroicon-o-pencil-square')
                    ->form([
                        TextInput::make('value')
                            ->label(__('New Value'))
                            ->required(),
                    ])
                    ->action(function (array $record, array $data): void {
                        try {
                            $agent = $this->getAgent();
                            $setResult = $agent->databaseSetGlobal($record['name'], (string) $data['value']);
                            if (! ($setResult['success'] ?? false)) {
                                throw new \RuntimeException($setResult['error'] ?? __('Update failed'));
                            }

                            $persistResult = $agent->databasePersistTuning($record['name'], (string) $data['value']);
                            if (! ($persistResult['success'] ?? false)) {
                                Notification::make()
                                    ->title(__('Variable updated, but not persisted'))
                                    ->body($persistResult['error'] ?? __('Unable to persist value'))
                                    ->warning()
                                    ->send();
                            } else {
                                Notification::make()
                                    ->title(__('Variable updated'))
                                    ->success()
                                    ->send();
                            }
                        } catch (\Exception $e) {
                            Notification::make()
                                ->title(__('Update failed'))
                                ->body(SafeError::message($e))
                                ->danger()
                                ->send();
                        }

                        $this->loadVariables();
                    }),
            ])
            ->headerActions([
                Action::make('refresh')
                    ->label(__('Refresh'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('gray')
                    ->action(fn () => $this->loadVariables()),
            ])
            ->striped()
            ->emptyStateHeading(__('No variables found'))
            ->emptyStateDescription(__('Database variables could not be loaded.'));
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
