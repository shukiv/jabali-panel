<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\WebhookEndpoints\Pages;

use App\Filament\Admin\Resources\WebhookEndpoints\WebhookEndpointResource;
use Filament\Actions\CreateAction;
use Filament\Resources\Pages\ListRecords;

class ListWebhookEndpoints extends ListRecords
{
    protected static string $resource = WebhookEndpointResource::class;

    protected function getHeaderActions(): array
    {
        return [
            CreateAction::make(),
        ];
    }
}
