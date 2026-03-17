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
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class JailsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $jails = [];

    protected function reloadJails(): void
    {
        try {
            $agent = app(AgentClient::class);
            $result = $agent->send('fail2ban.list_jails', []);
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

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->jails)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Service'))
                    ->formatStateUsing(fn (string $state): string => ucfirst($state))
                    ->searchable(),
                TextColumn::make('description')
                    ->label(__('Description'))
                    ->limit(50)
                    ->color('gray'),
                IconColumn::make('active')
                    ->label(__('Active'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-x-circle')
                    ->trueColor('success')
                    ->falseColor('gray'),
                IconColumn::make('enabled')
                    ->label(__('Enabled'))
                    ->boolean()
                    ->trueIcon('heroicon-o-shield-check')
                    ->falseIcon('heroicon-o-shield-exclamation')
                    ->trueColor('success')
                    ->falseColor('gray'),
            ])
            ->actions([
                Action::make('toggle')
                    ->label(fn (array $record): string => ($record['enabled'] ?? false) ? __('Disable') : __('Enable'))
                    ->icon(fn (array $record): string => ($record['enabled'] ?? false) ? 'heroicon-o-x-circle' : 'heroicon-o-check-circle')
                    ->color(fn (array $record): string => ($record['enabled'] ?? false) ? 'danger' : 'success')
                    ->disabled(fn (array $record): bool => ($record['name'] ?? '') === 'sshd')
                    ->action(function (array $record): void {
                        $name = $record['name'] ?? '';
                        $enabled = $record['enabled'] ?? false;

                        try {
                            $agent = app(AgentClient::class);
                            $action = $enabled ? 'fail2ban.disable_jail' : 'fail2ban.enable_jail';
                            $result = $agent->send($action, ['jail' => $name]);

                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title($enabled ? __('Jail disabled') : __('Jail enabled'))
                                    ->success()
                                    ->send();

                                // Reload jails data directly
                                $this->reloadJails();
                            } else {
                                throw new \Exception($result['error'] ?? __('Operation failed'));
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
            ->emptyStateHeading(__('No protection modules'))
            ->emptyStateDescription(__('No Fail2ban jails are configured.'))
            ->emptyStateIcon('heroicon-o-shield-exclamation');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
