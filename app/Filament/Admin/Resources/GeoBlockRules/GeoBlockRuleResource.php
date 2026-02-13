<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\GeoBlockRules;

use App\Filament\Admin\Resources\GeoBlockRules\Pages\CreateGeoBlockRule;
use App\Filament\Admin\Resources\GeoBlockRules\Pages\EditGeoBlockRule;
use App\Filament\Admin\Resources\GeoBlockRules\Pages\ListGeoBlockRules;
use App\Filament\Admin\Resources\GeoBlockRules\Schemas\GeoBlockRuleForm;
use App\Filament\Admin\Resources\GeoBlockRules\Tables\GeoBlockRulesTable;
use App\Models\GeoBlockRule;
use BackedEnum;
use Filament\Resources\Resource;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Table;

class GeoBlockRuleResource extends Resource
{
    protected static ?string $model = GeoBlockRule::class;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedGlobeAlt;

    protected static ?int $navigationSort = 21;

    public static function getNavigationLabel(): string
    {
        return __('Geographic Blocking');
    }

    public static function getModelLabel(): string
    {
        return __('Geo Rule');
    }

    public static function getPluralModelLabel(): string
    {
        return __('Geo Rules');
    }

    public static function form(Schema $schema): Schema
    {
        return GeoBlockRuleForm::configure($schema);
    }

    public static function table(Table $table): Table
    {
        return GeoBlockRulesTable::configure($table);
    }

    public static function getPages(): array
    {
        return [
            'index' => ListGeoBlockRules::route('/'),
            'create' => CreateGeoBlockRule::route('/create'),
            'edit' => EditGeoBlockRule::route('/{record}/edit'),
        ];
    }
}
