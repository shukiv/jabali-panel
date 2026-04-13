<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use App\JabaliSecurity\Pages\Security;
use Filament\Actions\Action;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Livewire\Component;

class GeoBlockTable extends Component implements HasActions, HasSchemas, HasTable
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
            ->records(function () {
                try {
                    $data = $this->client()->get('/webshield/geo-rules');
                    return $data['rules'] ?? [];
                } catch (\Exception) {
                    return [];
                }
            })
            ->columns([
                TextColumn::make('country_code')
                    ->label(__('Code'))
                    ->searchable(),
                TextColumn::make('country_name')
                    ->label(__('Country'))
                    ->searchable(),
                TextColumn::make('action')
                    ->label(__('Action'))
                    ->badge()
                    ->color(fn (string $state): string => match ($state) {
                        'block' => 'danger',
                        'challenge' => 'warning',
                        'allow' => 'success',
                        'log' => 'info',
                        default => 'gray',
                    }),
            ])
            ->actions([
                Action::make('remove')
                    ->label(__('Remove'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function ($record): void {
                        $cc = $record['country_code'] ?? '';
                        if (! $cc) {
                            return;
                        }
                        $this->client()->delete('/webshield/geo-rules/' . urlencode($cc));
                        Notification::make()
                            ->title(__('Country removed: :cc', ['cc' => $cc]))
                            ->success()
                            ->send();
                        $this->redirect(Security::tabUrl('defense', 'geoip'), navigate: true);
                    }),
            ])
            ->headerActions([
                Action::make('add_country')
                    ->label(__('Block Country'))
                    ->icon('heroicon-o-globe-alt')
                    ->form([
                        TextInput::make('country_codes')
                            ->label(__('Country Codes'))
                            ->helperText(__('Comma-separated ISO codes (e.g., CN,RU,KP)'))
                            ->required(),
                        Select::make('action')
                            ->label(__('Action'))
                            ->options([
                                'block' => __('Block (403)'),
                                'challenge' => __('Challenge (JS page)'),
                                'log' => __('Log only'),
                            ])
                            ->default('block'),
                    ])
                    ->action(function (array $data): void {
                        $codes = array_map('trim', explode(',', strtoupper($data['country_codes'] ?? '')));
                        $codes = array_filter($codes, fn ($c) => preg_match('/^[A-Z]{2}$/', $c));
                        if (empty($codes)) {
                            Notification::make()->title(__('Invalid country codes'))->danger()->send();
                            return;
                        }

                        // Get existing blocked countries and merge
                        $existing = $this->client()->get('/webshield/geo-rules');
                        $existingCodes = array_column($existing['rules'] ?? [], 'country_code');
                        $merged = array_unique(array_merge($existingCodes, $codes));

                        $this->client()->post('/webshield/geo-rules', [
                            'countries' => array_values($merged),
                            'action' => $data['action'] ?? 'block',
                            'mode' => 'blocklist',
                        ]);

                        Notification::make()
                            ->title(__('Countries blocked: :codes', ['codes' => implode(', ', $codes)]))
                            ->success()
                            ->send();
                        $this->redirect(Security::tabUrl('defense', 'geoip'), navigate: true);
                    }),
                Action::make('update_db')
                    ->label(__('Update GeoIP DB'))
                    ->icon('heroicon-o-arrow-path')
                    ->requiresConfirmation()
                    ->modalDescription(__('Download the latest MaxMind GeoLite2-Country database. Requires a license key configured below.'))
                    ->action(function (): void {
                        $result = $this->client()->post('/webshield/geo-update-db');
                        Notification::make()
                            ->title($result ? __('GeoIP database updated') : __('GeoIP update failed'))
                            ->{($result ? 'success' : 'danger')}()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No country rules'))
            ->emptyStateDescription(__('Block or allow traffic by country using MaxMind GeoIP database'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
