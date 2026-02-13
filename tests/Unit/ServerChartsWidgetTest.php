<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Filament\Admin\Widgets\ServerChartsWidget;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class ServerChartsWidgetTest extends TestCase
{
    use RefreshDatabase;

    public function test_history_seeding_includes_swap_and_disk_series(): void
    {
        $widget = app(ServerChartsWidget::class);
        $widget->range = '5m';

        $history = $widget->getHistory(1.25, 2.5, 42.5, 12.3, [
            ['mount' => '/', 'used' => 1024 * 1024 * 1024],
            ['mount' => '/boot', 'used' => 512 * 1024 * 1024],
        ]);

        $this->assertArrayHasKey('swap', $history);
        $this->assertArrayHasKey('iowait', $history);
        $this->assertArrayHasKey('disk', $history);
        $this->assertArrayHasKey('/', $history['disk']);
        $this->assertArrayHasKey('/boot', $history['disk']);
        $this->assertCount(30, $history['labels']);
        $this->assertCount(count($history['labels']), $history['swap']);
        $this->assertCount(count($history['labels']), $history['iowait']);
    }
}
