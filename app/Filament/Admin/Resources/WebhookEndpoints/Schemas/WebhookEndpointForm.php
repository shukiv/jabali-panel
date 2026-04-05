<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\WebhookEndpoints\Schemas;

use Filament\Forms\Components\CheckboxList;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Illuminate\Support\Str;

class WebhookEndpointForm
{
    public static function configure(Schema $schema): Schema
    {
        return $schema
            ->columns(1)
            ->components([
                Section::make(__('Webhook Details'))
                    ->schema([
                        TextInput::make('name')
                            ->label(__('Name'))
                            ->required()
                            ->maxLength(120),
                        TextInput::make('url')
                            ->label(__('URL'))
                            ->url()
                            ->required(),
                        CheckboxList::make('events')
                            ->label(__('Events'))
                            ->options([
                                'ssl.expiring' => __('SSL Expiring'),
                                'migration.completed' => __('Migration Completed'),
                                'user.suspended' => __('User Suspended'),
                            ])
                            ->columns(2),
                        TextInput::make('secret_token')
                            ->label(__('Secret Token'))
                            ->helperText(__('Used to sign webhook payloads'))
                            ->default(fn () => Str::random(32))
                            ->password()
                            ->revealable(),
                        Toggle::make('is_active')
                            ->label(__('Active'))
                            ->default(true),
                    ])
                    ->columns(2),
            ]);
    }
}
