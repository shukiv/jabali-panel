<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Filament\Admin\Pages\DnsZones;
use App\Filament\Jabali\Pages\DnsRecords;
use ReflectionMethod;
use Tests\TestCase;

class DnsPendingAddsTest extends TestCase
{
    public function test_dns_zones_queue_pending_add_adds_key(): void
    {
        $page = app(DnsZones::class);

        $method = new ReflectionMethod($page, 'queuePendingAdd');
        $method->setAccessible(true);
        $method->invoke($page, [
            'type' => 'A',
            'name' => '@',
            'content' => '127.0.0.1',
            'ttl' => 3600,
            'priority' => null,
        ]);

        $this->assertCount(1, $page->pendingAdds);
        $this->assertArrayHasKey('key', $page->pendingAdds[0]);
        $this->assertIsString($page->pendingAdds[0]['key']);
    }

    public function test_dns_records_queue_pending_add_adds_key(): void
    {
        $page = app(DnsRecords::class);

        $method = new ReflectionMethod($page, 'queuePendingAdd');
        $method->setAccessible(true);
        $method->invoke($page, [
            'type' => 'A',
            'name' => '@',
            'content' => '127.0.0.1',
            'ttl' => 3600,
            'priority' => null,
        ]);

        $this->assertCount(1, $page->pendingAdds);
        $this->assertArrayHasKey('key', $page->pendingAdds[0]);
        $this->assertIsString($page->pendingAdds[0]['key']);
    }

    public function test_dns_zones_remove_pending_add_by_key(): void
    {
        $page = app(DnsZones::class);
        $page->pendingAdds = [
            ['key' => 'first', 'type' => 'A', 'name' => '@', 'content' => '127.0.0.1', 'ttl' => 3600, 'priority' => null],
            ['key' => 'second', 'type' => 'A', 'name' => 'www', 'content' => '127.0.0.2', 'ttl' => 3600, 'priority' => null],
        ];

        $page->removePendingAdd('first');

        $this->assertCount(1, $page->pendingAdds);
        $this->assertSame('second', $page->pendingAdds[0]['key']);
    }

    public function test_dns_zones_pending_adds_table_component_is_used(): void
    {
        $page = file_get_contents(base_path('app/Filament/Admin/Pages/DnsZones.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString('DnsPendingAddsTable::class', $page);
        $this->assertStringContainsString('EmbeddedTable::make(DnsPendingAddsTable::class, fn () => [', $page);
        $this->assertStringContainsString('saveChanges(false)', $page);
    }

    public function test_dns_records_remove_pending_add_by_key(): void
    {
        $page = app(DnsRecords::class);
        $page->pendingAdds = [
            ['key' => 'first', 'type' => 'A', 'name' => '@', 'content' => '127.0.0.1', 'ttl' => 3600, 'priority' => null],
            ['key' => 'second', 'type' => 'A', 'name' => 'www', 'content' => '127.0.0.2', 'ttl' => 3600, 'priority' => null],
        ];

        $page->removePendingAdd('first');

        $this->assertCount(1, $page->pendingAdds);
        $this->assertSame('second', $page->pendingAdds[0]['key']);
    }

    public function test_dns_records_pending_adds_table_component_is_used(): void
    {
        $page = file_get_contents(base_path('app/Filament/Jabali/Pages/DnsRecords.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString('DnsPendingAddsTable::class', $page);
        $this->assertStringContainsString('EmbeddedTable::make(DnsPendingAddsTable::class, fn () => [', $page);
        $this->assertStringContainsString('saveChanges(false)', $page);
    }

    public function test_dns_zones_sanitize_pending_add_removes_key(): void
    {
        $page = app(DnsZones::class);

        $method = new ReflectionMethod($page, 'sanitizePendingAdd');
        $method->setAccessible(true);
        $payload = $method->invoke($page, [
            'key' => 'abc',
            'type' => 'A',
            'name' => '@',
            'content' => '127.0.0.1',
            'ttl' => 3600,
            'priority' => null,
        ]);

        $this->assertArrayNotHasKey('key', $payload);
    }

    public function test_dns_records_sanitize_pending_add_removes_key(): void
    {
        $page = app(DnsRecords::class);

        $method = new ReflectionMethod($page, 'sanitizePendingAdd');
        $method->setAccessible(true);
        $payload = $method->invoke($page, [
            'key' => 'abc',
            'type' => 'A',
            'name' => '@',
            'content' => '127.0.0.1',
            'ttl' => 3600,
            'priority' => null,
        ]);

        $this->assertArrayNotHasKey('key', $payload);
    }
}
