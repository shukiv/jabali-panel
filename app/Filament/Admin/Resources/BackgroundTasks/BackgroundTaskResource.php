<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\BackgroundTasks;

use App\Filament\Admin\Resources\BackgroundTasks\Pages\ListBackgroundTasks;
use App\Filament\Admin\Resources\BackgroundTasks\Tables\BackgroundTasksTable;
use App\Models\BackgroundTask;
use BackedEnum;
use Filament\Resources\Resource;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Table;

class BackgroundTaskResource extends Resource
{
    protected static ?string $model = BackgroundTask::class;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedBolt;

    protected static ?int $navigationSort = 98;

    public static function getNavigationLabel(): string
    {
        return __('Background Tasks');
    }

    public static function canCreate(): bool
    {
        return false; // tasks are dispatched by the system, never by hand
    }

    public static function shouldRegisterNavigation(): bool
    {
        return (bool) (auth('admin')->user()?->is_admin ?? false);
    }

    public static function table(Table $table): Table
    {
        return BackgroundTasksTable::configure($table);
    }

    public static function getPages(): array
    {
        return [
            'index' => ListBackgroundTasks::route('/'),
        ];
    }
}
