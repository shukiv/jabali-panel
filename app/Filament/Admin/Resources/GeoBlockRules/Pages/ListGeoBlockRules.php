<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\GeoBlockRules\Pages;

use App\Filament\Admin\Resources\GeoBlockRules\GeoBlockRuleResource;
use App\Models\DnsSetting;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\CreateAction;
use Filament\Forms\Components\FileUpload;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\ListRecords;
use Illuminate\Support\Facades\Storage;

class ListGeoBlockRules extends ListRecords
{
    protected static string $resource = GeoBlockRuleResource::class;

    protected function getHeaderActions(): array
    {
        return [
            Action::make('updateGeoIpDatabase')
                ->label(__('Update GeoIP Database'))
                ->icon('heroicon-o-arrow-down-tray')
                ->form([
                    TextInput::make('account_id')
                        ->label(__('MaxMind Account ID'))
                        ->required()
                        ->default(fn (): string => (string) (DnsSetting::get('geoip_account_id') ?? '')),
                    TextInput::make('license_key')
                        ->label(__('MaxMind License Key'))
                        ->password()
                        ->revealable()
                        ->required()
                        ->default(fn (): string => (string) (DnsSetting::get('geoip_license_key') ?? '')),
                    TextInput::make('edition_ids')
                        ->label(__('Edition IDs'))
                        ->helperText(__('Comma-separated (default: GeoLite2-Country)'))
                        ->default(fn (): string => (string) (DnsSetting::get('geoip_edition_ids') ?? 'GeoLite2-Country')),
                ])
                ->action(function (array $data): void {
                    $accountId = trim((string) ($data['account_id'] ?? ''));
                    $licenseKey = trim((string) ($data['license_key'] ?? ''));
                    $editionIds = trim((string) ($data['edition_ids'] ?? 'GeoLite2-Country'));

                    if ($accountId === '' || $licenseKey === '') {
                        Notification::make()
                            ->title(__('MaxMind credentials are required'))
                            ->danger()
                            ->send();

                        return;
                    }

                    DnsSetting::set('geoip_account_id', $accountId);
                    DnsSetting::set('geoip_license_key', $licenseKey);
                    DnsSetting::set('geoip_edition_ids', $editionIds);

                    try {
                        $agent = app(AgentClient::class);
                        $result = $agent->geoUpdateDatabase($accountId, $licenseKey, $editionIds);

                        Notification::make()
                            ->title(__('GeoIP database updated'))
                            ->body($result['path'] ?? null)
                            ->success()
                            ->send();
                    } catch (Exception $e) {
                        Notification::make()
                            ->title(__('GeoIP update failed'))
                            ->body(SafeError::message($e))
                            ->danger()
                            ->send();
                    }
                }),
            Action::make('uploadGeoIpDatabase')
                ->label(__('Upload GeoIP Database'))
                ->icon('heroicon-o-arrow-up-tray')
                ->form([
                    FileUpload::make('mmdb_file')
                        ->label(__('GeoIP .mmdb File'))
                        ->required()
                        ->acceptedFileTypes(['application/octet-stream', 'application/x-maxmind-db'])
                        ->helperText(__('Upload a MaxMind GeoIP .mmdb file (GeoLite2 or GeoIP2).')),
                    TextInput::make('edition')
                        ->label(__('Edition ID'))
                        ->helperText(__('Example: GeoLite2-Country'))
                        ->default(fn (): string => (string) (DnsSetting::get('geoip_edition_ids') ?? 'GeoLite2-Country')),
                ])
                ->action(function (array $data): void {
                    if (empty($data['mmdb_file'])) {
                        Notification::make()
                            ->title(__('No file uploaded'))
                            ->danger()
                            ->send();

                        return;
                    }

                    $edition = trim((string) ($data['edition'] ?? 'GeoLite2-Country'));
                    if ($edition === '') {
                        $edition = 'GeoLite2-Country';
                    }

                    $filePath = Storage::disk('local')->path($data['mmdb_file']);
                    if (! file_exists($filePath)) {
                        Notification::make()
                            ->title(__('Uploaded file not found'))
                            ->danger()
                            ->send();

                        return;
                    }

                    $content = base64_encode((string) file_get_contents($filePath));

                    try {
                        $agent = app(AgentClient::class);
                        $result = $agent->geoUploadDatabase($edition, $content);

                        Notification::make()
                            ->title(__('GeoIP database uploaded'))
                            ->body($result['path'] ?? null)
                            ->success()
                            ->send();
                    } catch (Exception $e) {
                        Notification::make()
                            ->title(__('GeoIP upload failed'))
                            ->body(SafeError::message($e))
                            ->danger()
                            ->send();
                    }
                }),
            CreateAction::make(),
        ];
    }
}
