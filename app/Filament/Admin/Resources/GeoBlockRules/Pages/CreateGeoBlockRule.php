<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\GeoBlockRules\Pages;

use App\Filament\Admin\Resources\GeoBlockRules\GeoBlockRuleResource;
use App\Services\System\GeoBlockService;
use Exception;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\CreateRecord;

class CreateGeoBlockRule extends CreateRecord
{
    protected static string $resource = GeoBlockRuleResource::class;

    protected function afterCreate(): void
    {
        try {
            app(GeoBlockService::class)->applyCurrentRules();

            Notification::make()
                ->title(__('Geo rules applied'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Geo rules apply failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }
}
