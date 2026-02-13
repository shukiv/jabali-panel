<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class SslStatsOverviewViewTest extends TestCase
{
    public function test_ssl_stats_overview_uses_ssl_view(): void
    {
        $source = file_get_contents(__DIR__.'/../../app/Filament/Admin/Widgets/SslStatsOverview.php');

        $this->assertIsString($source);
        $this->assertStringContainsString('filament.admin.widgets.ssl-stats', $source);
    }
}
