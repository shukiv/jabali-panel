<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class HealthMonitorServicesTest extends TestCase
{
    public function test_health_monitor_includes_opendkim(): void
    {
        $script = file_get_contents(__DIR__.'/../../bin/jabali-health-monitor');

        $this->assertIsString($script);
        $this->assertStringContainsString("'opendkim' => [", $script);
        $this->assertStringContainsString("'description' => 'OpenDKIM'", $script);
    }
}
