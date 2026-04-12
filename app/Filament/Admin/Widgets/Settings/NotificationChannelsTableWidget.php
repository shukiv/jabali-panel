<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Settings;

use App\Filament\Admin\Resources\NotificationChannels\Schemas\NotificationChannelForm;
use App\Filament\Admin\Resources\NotificationChannels\Tables\NotificationChannelsTable;
use App\Models\NotificationChannel;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Actions\CreateAction;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

/**
 * Embeddable channels table — same columns/actions as the standalone
 * resource but rendered inside the Server Settings > Notifications tab
 * via `\Filament\Schemas\Components\Livewire::make(...)`.
 *
 * Uses Filament modal actions for create/edit/delete so the operator
 * never leaves the Server Settings page.
 */
class NotificationChannelsTableWidget extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        // Re-use the resource table configuration verbatim, then layer on
        // modal-form create/edit/delete so this behaves as a self-contained
        // management panel inside the tab.
        return NotificationChannelsTable::configure(
            $table->query(NotificationChannel::query())
        )
            ->headerActions([
                CreateAction::make()
                    ->label(__('New notification channel'))
                    ->icon('heroicon-o-plus')
                    ->model(NotificationChannel::class)
                    ->schema(fn (\Filament\Schemas\Schema $schema) => NotificationChannelForm::configure($schema))
                    ->successNotification(
                        Notification::make()
                            ->title(__('Channel created'))
                            ->success(),
                    ),
            ])
            ->recordActions([
                Action::make('test')
                    ->label(__('Send test'))
                    ->icon('heroicon-o-paper-airplane')
                    ->requiresConfirmation()
                    ->action(function (NotificationChannel $record) {
                        $factory = app(\App\Notifications\Channels\ChannelDriverFactory::class);
                        if (! $factory->supports($record->type)) {
                            Notification::make()->title(__('Unknown channel type'))->danger()->send();

                            return;
                        }
                        $driver = $factory->for($record->type);
                        $payload = new \App\Notifications\NotificationPayload(
                            type: 'test',
                            severity: 'info',
                            subject: 'Jabali — test notification',
                            message: 'Test from Server Settings → Notifications.',
                            context: ['admin' => auth()->user()?->email ?? 'unknown'],
                            hostname: gethostname() ?: 'unknown',
                        );
                        $result = $driver->send($record, $payload);
                        if ($result->success) {
                            Notification::make()
                                ->title(__('Test sent via :type', ['type' => $record->type]))
                                ->success()
                                ->send();
                        } else {
                            Notification::make()
                                ->title(__('Test failed'))
                                ->body((string) $result->error)
                                ->danger()
                                ->persistent()
                                ->send();
                        }
                    }),
                \Filament\Actions\EditAction::make()
                    ->schema(fn (\Filament\Schemas\Schema $schema) => NotificationChannelForm::configure($schema))
                    ->successNotification(
                        Notification::make()->title(__('Channel updated'))->success(),
                    ),
                \Filament\Actions\DeleteAction::make(),
            ]);
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
