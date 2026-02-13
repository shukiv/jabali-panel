<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\HostingPackages;

use App\Filament\Admin\Resources\HostingPackages\Pages\CreateHostingPackage;
use App\Filament\Admin\Resources\HostingPackages\Pages\EditHostingPackage;
use App\Filament\Admin\Resources\HostingPackages\Pages\ListHostingPackages;
use App\Filament\Admin\Resources\HostingPackages\Schemas\HostingPackageForm;
use App\Filament\Admin\Resources\HostingPackages\Tables\HostingPackagesTable;
use App\Models\HostingPackage;
use BackedEnum;
use Filament\Resources\Resource;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Table;

class HostingPackageResource extends Resource
{
    protected static ?string $model = HostingPackage::class;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedCube;

    protected static ?int $navigationSort = 2;

    public static function getNavigationLabel(): string
    {
        return __('Hosting Packages');
    }

    public static function getModelLabel(): string
    {
        return __('Hosting Package');
    }

    public static function getPluralModelLabel(): string
    {
        return __('Hosting Packages');
    }

    public static function form(Schema $schema): Schema
    {
        return HostingPackageForm::configure($schema);
    }

    public static function table(Table $table): Table
    {
        return HostingPackagesTable::configure($table);
    }

    public static function getPages(): array
    {
        return [
            'index' => ListHostingPackages::route('/'),
            'create' => CreateHostingPackage::route('/create'),
            'edit' => EditHostingPackage::route('/{record}/edit'),
        ];
    }
}
