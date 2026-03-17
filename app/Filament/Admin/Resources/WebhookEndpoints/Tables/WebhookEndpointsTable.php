<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\WebhookEndpoints\Tables;

use App\Models\WebhookEndpoint;
use App\Support\SafeError;
use Filament\Actions\Action;
use Filament\Actions\DeleteAction;
use Filament\Actions\EditAction;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Table;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Str;

class WebhookEndpointsTable
{
    public static function configure(Table $table): Table
    {
        return $table
            ->query(WebhookEndpoint::query())
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->searchable(),
                TextColumn::make('url')
                    ->label(__('URL'))
                    ->limit(40)
                    ->tooltip(fn (WebhookEndpoint $record) => $record->url),
                TextColumn::make('events')
                    ->label(__('Events'))
                    ->formatStateUsing(function ($state): string {
                        if (is_array($state)) {
                            return implode(', ', $state);
                        }

                        return (string) $state;
                    })
                    ->wrap(),
                IconColumn::make('is_active')
                    ->label(__('Active'))
                    ->boolean(),
                TextColumn::make('last_triggered_at')
                    ->label(__('Last Triggered'))
                    ->since(),
                TextColumn::make('last_response_code')
                    ->label(__('Last Response')),
            ])
            ->recordActions([
                Action::make('test')
                    ->label(__('Test'))
                    ->icon('heroicon-o-signal')
                    ->color('info')
                    ->action(function (WebhookEndpoint $record): void {
                        $payload = [
                            'event' => 'test',
                            'timestamp' => now()->toIso8601String(),
                            'request_id' => Str::uuid()->toString(),
                        ];

                        $headers = [];
                        if (! empty($record->secret_token)) {
                            $signature = hash_hmac('sha256', json_encode($payload), $record->secret_token);
                            $headers['X-Jabali-Signature'] = $signature;
                        }

                        try {
                            $response = Http::withHeaders($headers)->post($record->url, $payload);
                            $record->update([
                                'last_response_code' => $response->status(),
                                'last_triggered_at' => now(),
                            ]);

                            Notification::make()
                                ->title(__('Webhook delivered'))
                                ->body(__('Status: :status', ['status' => $response->status()]))
                                ->success()
                                ->send();
                        } catch (\Exception $e) {
                            $record->update([
                                'last_response_code' => null,
                                'last_triggered_at' => now(),
                            ]);

                            Notification::make()
                                ->title(__('Webhook failed'))
                                ->body(SafeError::message($e))
                                ->danger()
                                ->send();
                        }
                    }),
                EditAction::make(),
                DeleteAction::make(),
            ])
            ->emptyStateHeading(__('No webhooks configured'))
            ->emptyStateDescription(__('Add a webhook to receive system notifications.'));
    }
}
