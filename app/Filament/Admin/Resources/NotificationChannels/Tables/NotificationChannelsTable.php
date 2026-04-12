<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\NotificationChannels\Tables;

use App\Models\NotificationChannel;
use App\Notifications\Channels\ChannelDriverFactory;
use App\Notifications\NotificationPayload;
use Filament\Actions\Action;
use Filament\Actions\BulkActionGroup;
use Filament\Actions\DeleteAction;
use Filament\Actions\DeleteBulkAction;
use Filament\Actions\EditAction;
use Filament\Notifications\Notification as FilamentNotification;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Table;

class NotificationChannelsTable
{
    public static function configure(Table $table): Table
    {
        return $table
            ->columns([
                TextColumn::make('name')->searchable()->sortable(),
                TextColumn::make('type')
                    ->badge()
                    ->colors([
                        'info' => 'email',
                        'success' => 'telegram',
                        'warning' => 'ntfy',
                        'danger' => 'web_push',
                    ]),
                IconColumn::make('enabled')->boolean()->label('Enabled'),
                TextColumn::make('updated_at')->dateTime()->since()->label('Updated'),
            ])
            ->recordActions([
                Action::make('test')
                    ->label('Send test')
                    ->icon('heroicon-o-paper-airplane')
                    ->requiresConfirmation()
                    ->action(function (NotificationChannel $record) {
                        $factory = app(ChannelDriverFactory::class);
                        if (! $factory->supports($record->type)) {
                            FilamentNotification::make()
                                ->title('Unknown channel type')->danger()->send();

                            return;
                        }

                        $driver = $factory->for($record->type);
                        $payload = new NotificationPayload(
                            type: 'test',
                            severity: 'info',
                            subject: 'Jabali — test notification',
                            message: 'This is a test from Server Settings → Notifications.',
                            context: ['admin' => auth()->user()?->email ?? 'unknown'],
                            hostname: gethostname() ?: 'unknown',
                        );

                        $result = $driver->send($record, $payload);

                        if ($result->success) {
                            FilamentNotification::make()
                                ->title("Test sent via {$record->type}")->success()->send();
                        } else {
                            FilamentNotification::make()
                                ->title('Test failed')
                                ->body((string) $result->error)
                                ->danger()
                                ->persistent()
                                ->send();
                        }
                    }),
                EditAction::make(),
                DeleteAction::make(),
            ])
            ->toolbarActions([
                BulkActionGroup::make([
                    DeleteBulkAction::make(),
                ]),
            ]);
    }
}
