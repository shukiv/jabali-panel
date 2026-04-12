<?php

namespace App\Filament\Admin\Resources\NotificationChannels\Pages;

use App\Filament\Admin\Resources\NotificationChannels\NotificationChannelResource;
use Filament\Actions\DeleteAction;
use Filament\Resources\Pages\EditRecord;

class EditNotificationChannel extends EditRecord
{
    protected static string $resource = NotificationChannelResource::class;

    protected function getHeaderActions(): array
    {
        return [
            DeleteAction::make(),
        ];
    }
}
