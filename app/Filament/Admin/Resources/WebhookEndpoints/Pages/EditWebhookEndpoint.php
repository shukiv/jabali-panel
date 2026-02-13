<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\WebhookEndpoints\Pages;

use App\Filament\Admin\Resources\WebhookEndpoints\WebhookEndpointResource;
use Filament\Actions\DeleteAction;
use Filament\Resources\Pages\EditRecord;

class EditWebhookEndpoint extends EditRecord
{
    protected static string $resource = WebhookEndpointResource::class;

    protected function getHeaderActions(): array
    {
        return [
            DeleteAction::make(),
        ];
    }
}
