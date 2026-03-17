<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\CloudflareZone;
use App\Models\Domain;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Http;

class CdnIntegration extends Page implements HasActions, HasTable
{
    use InteractsWithActions;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-cloud';

    protected static ?int $navigationSort = 20;

    protected static ?string $slug = 'cdn-integration';

    protected string $view = 'filament.jabali.pages.cdn-integration';

    public function getTitle(): string|Htmlable
    {
        return __('CDN Integration');
    }

    public static function getNavigationLabel(): string
    {
        return __('CDN Integration');
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(Domain::query()->where('user_id', Auth::id()))
            ->columns([
                TextColumn::make('domain')
                    ->label(__('Domain'))
                    ->searchable()
                    ->sortable(),
                IconColumn::make('cloudflare')
                    ->label(__('Cloudflare'))
                    ->boolean()
                    ->state(fn (Domain $record) => $this->getZone($record) !== null)
                    ->trueColor('success')
                    ->falseColor('gray'),
            ])
            ->recordActions([
                Action::make('connect')
                    ->label(__('Connect'))
                    ->icon('heroicon-o-link')
                    ->color('primary')
                    ->visible(fn (Domain $record) => $this->getZone($record) === null)
                    ->form([
                        TextInput::make('api_token')
                            ->label(__('Cloudflare API Token'))
                            ->password()
                            ->revealable()
                            ->required(),
                    ])
                    ->action(function (Domain $record, array $data): void {
                        $token = $data['api_token'];
                        try {
                            $zone = $this->fetchCloudflareZone($record->domain, $token);
                            CloudflareZone::updateOrCreate(
                                [
                                    'user_id' => Auth::id(),
                                    'domain_id' => $record->id,
                                ],
                                [
                                    'zone_id' => $zone['id'],
                                    'account_id' => $zone['account']['id'] ?? null,
                                    'api_token' => $token,
                                ]
                            );

                            Notification::make()->title(__('Cloudflare connected'))->success()->send();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Connection failed'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
                Action::make('purge')
                    ->label(__('Purge Cache'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('warning')
                    ->visible(fn (Domain $record) => $this->getZone($record) !== null)
                    ->requiresConfirmation()
                    ->action(function (Domain $record): void {
                        $zone = $this->getZone($record);
                        if (! $zone) {
                            return;
                        }

                        try {
                            $response = Http::withToken($zone->api_token)
                                ->post("https://api.cloudflare.com/client/v4/zones/{$zone->zone_id}/purge_cache", [
                                    'purge_everything' => true,
                                ]);

                            if (! ($response->json('success') ?? false)) {
                                throw new Exception($response->json('errors.0.message') ?? 'Cloudflare request failed');
                            }

                            Notification::make()->title(__('Cache purged'))->success()->send();
                        } catch (Exception $e) {
                            Notification::make()->title(__('Purge failed'))->body(SafeError::message($e))->danger()->send();
                        }
                    }),
                Action::make('disconnect')
                    ->label(__('Disconnect'))
                    ->icon('heroicon-o-x-mark')
                    ->color('gray')
                    ->visible(fn (Domain $record) => $this->getZone($record) !== null)
                    ->requiresConfirmation()
                    ->action(function (Domain $record): void {
                        $zone = $this->getZone($record);
                        if ($zone) {
                            $zone->delete();
                        }
                        Notification::make()->title(__('Cloudflare disconnected'))->success()->send();
                    }),
            ])
            ->emptyStateHeading(__('No domains found'))
            ->emptyStateDescription(__('Add a domain before connecting to a CDN'));
    }

    protected function getZone(Domain $record): ?CloudflareZone
    {
        return CloudflareZone::query()
            ->where('user_id', Auth::id())
            ->where('domain_id', $record->id)
            ->first();
    }

    protected function fetchCloudflareZone(string $domain, string $token): array
    {
        $response = Http::withToken($token)->get('https://api.cloudflare.com/client/v4/zones', [
            'name' => $domain,
        ]);

        if (! ($response->json('success') ?? false)) {
            throw new Exception($response->json('errors.0.message') ?? 'Cloudflare request failed');
        }

        $zone = $response->json('result.0');
        if (! $zone) {
            throw new Exception(__('Zone not found for :domain', ['domain' => $domain]));
        }

        return $zone;
    }
}
