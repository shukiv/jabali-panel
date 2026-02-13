<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\GeoBlockRules\Pages;

use App\Filament\Admin\Resources\GeoBlockRules\GeoBlockRuleResource;
use App\Services\System\GeoBlockService;
use Exception;
use Filament\Actions\DeleteAction;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\EditRecord;

class EditGeoBlockRule extends EditRecord
{
    protected static string $resource = GeoBlockRuleResource::class;

    protected function afterSave(): void
    {
        $this->applyGeoRules();
    }

    protected function getHeaderActions(): array
    {
        return [
            DeleteAction::make()
                ->after(fn () => $this->applyGeoRules()),
        ];
    }

    protected function applyGeoRules(): void
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
