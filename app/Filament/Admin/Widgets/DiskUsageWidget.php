<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Services\Agent\AgentClient;
use Filament\Widgets\Widget;

class DiskUsageWidget extends Widget
{
    protected string $view = 'filament.admin.widgets.disk-usage';

    protected int|string|array $columnSpan = 1;

    public function getData(): array
    {
        try {
            $agent = app(AgentClient::class);

            return $agent->metricsDisk()['data'] ?? [];
        } catch (\Exception $e) {
            return [];
        }
    }
}
