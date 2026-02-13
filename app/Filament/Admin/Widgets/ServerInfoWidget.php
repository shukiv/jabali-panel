<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use Filament\Widgets\Widget;

class ServerInfoWidget extends Widget
{
    protected string $view = 'filament.admin.widgets.server-info';

    protected int|string|array $columnSpan = 'full';

    protected static ?int $sort = 2;
}
