<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Services\SysstatMetrics;
use Carbon\CarbonImmutable;
use ReflectionClass;
use Tests\TestCase;

class SysstatMetricsTest extends TestCase
{
    public function test_parse_csv_timestamp_returns_null_for_empty_input(): void
    {
        $metrics = new SysstatMetrics;
        $reflection = new ReflectionClass($metrics);
        $method = $reflection->getMethod('parseCsvTimestamp');
        $method->setAccessible(true);

        $this->assertNull($method->invoke($metrics, null));
        $this->assertNull($method->invoke($metrics, ''));
    }

    public function test_parse_csv_timestamp_returns_unix_timestamp(): void
    {
        $metrics = new SysstatMetrics;
        $reflection = new ReflectionClass($metrics);
        $method = $reflection->getMethod('parseCsvTimestamp');
        $method->setAccessible(true);

        $value = '2026-02-01 17:00:49 UTC';
        $expected = CarbonImmutable::parse($value, $metrics->timezoneName())->getTimestamp();

        $this->assertSame($expected, $method->invoke($metrics, $value));
    }
}
