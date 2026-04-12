<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\BackgroundTasks\Pages;

use App\Filament\Admin\Resources\BackgroundTasks\BackgroundTaskResource;
use Filament\Resources\Pages\ListRecords;

class ListBackgroundTasks extends ListRecords
{
    protected static string $resource = BackgroundTaskResource::class;
}
