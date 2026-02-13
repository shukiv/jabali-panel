<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\WebhookEndpoints;

use App\Filament\Admin\Resources\WebhookEndpoints\Pages\CreateWebhookEndpoint;
use App\Filament\Admin\Resources\WebhookEndpoints\Pages\EditWebhookEndpoint;
use App\Filament\Admin\Resources\WebhookEndpoints\Pages\ListWebhookEndpoints;
use App\Filament\Admin\Resources\WebhookEndpoints\Schemas\WebhookEndpointForm;
use App\Filament\Admin\Resources\WebhookEndpoints\Tables\WebhookEndpointsTable;
use App\Models\WebhookEndpoint;
use BackedEnum;
use Filament\Resources\Resource;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Table;

class WebhookEndpointResource extends Resource
{
    protected static ?string $model = WebhookEndpoint::class;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedBellAlert;

    protected static ?int $navigationSort = 18;

    public static function getNavigationLabel(): string
    {
        return __('Webhook Notifications');
    }

    public static function getModelLabel(): string
    {
        return __('Webhook');
    }

    public static function getPluralModelLabel(): string
    {
        return __('Webhooks');
    }

    public static function form(Schema $schema): Schema
    {
        return WebhookEndpointForm::configure($schema);
    }

    public static function table(Table $table): Table
    {
        return WebhookEndpointsTable::configure($table);
    }

    public static function getPages(): array
    {
        return [
            'index' => ListWebhookEndpoints::route('/'),
            'create' => CreateWebhookEndpoint::route('/create'),
            'edit' => EditWebhookEndpoint::route('/{record}/edit'),
        ];
    }
}
