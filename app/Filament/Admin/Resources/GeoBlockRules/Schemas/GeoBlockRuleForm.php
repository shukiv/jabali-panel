<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\GeoBlockRules\Schemas;

use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;

class GeoBlockRuleForm
{
    public static function configure(Schema $schema): Schema
    {
        return $schema
            ->columns(1)
            ->components([
                Section::make(__('Geo Rule'))
                    ->schema([
                        TextInput::make('country_code')
                            ->label(__('Country Code'))
                            ->maxLength(2)
                            ->minLength(2)
                            ->required()
                            ->helperText(__('Use ISO-3166 alpha-2 code (e.g., US, DE, FR).')),
                        Select::make('action')
                            ->label(__('Action'))
                            ->options([
                                'block' => __('Block'),
                                'allow' => __('Allow'),
                            ])
                            ->default('block')
                            ->required(),
                        TextInput::make('notes')
                            ->label(__('Notes')),
                        Toggle::make('is_active')
                            ->label(__('Active'))
                            ->default(true),
                    ])
                    ->columns(2),
            ]);
    }
}
