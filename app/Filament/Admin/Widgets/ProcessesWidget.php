<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Services\Agent\AgentClient;
use Filament\Widgets\Widget;

class ProcessesWidget extends Widget
{
    protected string $view = 'filament.admin.widgets.processes';

    protected int|string|array $columnSpan = 'full';

    public function getData(): array
    {
        try {
            $agent = app(AgentClient::class);

            return $agent->metricsProcesses(10)['data'] ?? [];
        } catch (\Exception $e) {
            return [];
        }
    }
}
