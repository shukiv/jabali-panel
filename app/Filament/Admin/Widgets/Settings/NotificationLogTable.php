<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Settings;

use App\Models\NotificationLog;
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
use Filament\Tables\Filters\SelectFilter;
use Filament\Tables\Table;
use Livewire\Component;

class NotificationLogTable extends Component implements HasTable, HasSchemas, HasActions
{
    use InteractsWithTable;
    use InteractsWithSchemas;
    use InteractsWithActions;

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(
                NotificationLog::query()->latest()
            )
            ->columns([
                TextColumn::make('created_at')
                    ->label(__('Date'))
                    ->dateTime('M d, H:i')
                    ->sortable(),
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => match ($state) {
                        'ssl_errors' => __('SSL'),
                        'backup_failures' => __('Backup'),
                        'disk_quota' => __('Quota'),
                        'login_failures' => __('Login'),
                        'ssh_logins' => __('SSH'),
                        'system_updates' => __('Updates'),
                        'service_health' => __('Health'),
                        'high_load' => __('Load'),
                        'test' => __('Test'),
                        default => ucfirst(str_replace('_', ' ', $state)),
                    })
                    ->color(fn (string $state): string => match ($state) {
                        'ssl_errors' => 'warning',
                        'backup_failures' => 'danger',
                        'disk_quota' => 'warning',
                        'login_failures' => 'danger',
                        'ssh_logins' => 'info',
                        'system_updates' => 'info',
                        'service_health' => 'primary',
                        'high_load' => 'danger',
                        'test' => 'success',
                        default => 'gray',
                    }),
                TextColumn::make('subject')
                    ->label(__('Subject'))
                    ->limit(40)
                    ->tooltip(fn (NotificationLog $record): string => $record->subject)
                    ->searchable(),
                TextColumn::make('recipients')
                    ->label(__('Recipients'))
                    ->formatStateUsing(function ($state): string {
                        if (is_string($state)) {
                            $state = json_decode($state, true) ?? [];
                        }
                        return is_array($state) && count($state) > 0 ? implode(', ', $state) : '-';
                    })
                    ->limit(30)
                    ->color('gray'),
                IconColumn::make('status')
                    ->label(__('Status'))
                    ->icon(fn (string $state): string => match ($state) {
                        'sent' => 'heroicon-o-check-circle',
                        'failed' => 'heroicon-o-x-circle',
                        'skipped' => 'heroicon-o-minus-circle',
                        default => 'heroicon-o-question-mark-circle',
                    })
                    ->color(fn (string $state): string => match ($state) {
                        'sent' => 'success',
                        'failed' => 'danger',
                        'skipped' => 'gray',
                        default => 'gray',
                    })
                    ->tooltip(fn (NotificationLog $record): ?string => $record->error),
            ])
            ->filters([
                SelectFilter::make('status')
                    ->label(__('Status'))
                    ->options([
                        'sent' => __('Sent'),
                        'failed' => __('Failed'),
                        'skipped' => __('Skipped'),
                    ]),
                SelectFilter::make('type')
                    ->label(__('Type'))
                    ->options([
                        'ssl_errors' => __('SSL Certificate'),
                        'backup_failures' => __('Backup'),
                        'disk_quota' => __('Disk Quota'),
                        'login_failures' => __('Login Failure'),
                        'ssh_logins' => __('SSH Login'),
                        'system_updates' => __('System Updates'),
                        'service_health' => __('Service Health'),
                        'high_load' => __('High Load'),
                        'test' => __('Test'),
                    ]),
            ])
            ->actions([
                Action::make('view')
                    ->label(__('View'))
                    ->icon('heroicon-o-eye')
                    ->color('gray')
                    ->modalHeading(fn (NotificationLog $record): string => $record->subject)
                    ->modalContent(fn (NotificationLog $record) => view('filament.admin.components.notification-log-detail', ['record' => $record]))
                    ->modalSubmitAction(false)
                    ->modalCancelActionLabel(__('Close')),
            ])
            ->headerActions([
                Action::make('cleanup')
                    ->label(__('Clear Old Logs'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Clear Old Notification Logs'))
                    ->modalDescription(__('This will delete all notification logs older than 30 days. This action cannot be undone.'))
                    ->action(function (): void {
                        $deleted = NotificationLog::cleanup(30);
                        Notification::make()
                            ->title(__('Logs cleaned up'))
                            ->body(__(':count old log entries deleted', ['count' => $deleted]))
                            ->success()
                            ->send();
                        $this->resetTable();
                    }),
            ])
            ->defaultSort('created_at', 'desc')
            ->paginated([10, 25, 50])
            ->striped()
            ->emptyStateHeading(__('No notification logs'))
            ->emptyStateDescription(__('Sent notifications will appear here.'))
            ->emptyStateIcon('heroicon-o-bell-slash');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
