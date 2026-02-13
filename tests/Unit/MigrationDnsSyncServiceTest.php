<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Models\DnsSetting;
use App\Models\Domain;
use App\Services\Agent\AgentClient;
use App\Services\Migration\MigrationDnsSyncService;
use Illuminate\Foundation\Testing\RefreshDatabase;
use ReflectionMethod;
use Tests\TestCase;

class MigrationDnsSyncServiceTest extends TestCase
{
    use RefreshDatabase;

    public function test_default_records_include_nameserver_glue_records(): void
    {
        DnsSetting::set('ns1', 'ns1.jabali-panel.com');
        DnsSetting::set('ns2', 'ns2.jabali-panel.com');
        DnsSetting::set('default_ip', '182.54.236.100');
        DnsSetting::set('default_ipv6', '2001:db8::1');
        DnsSetting::clearCache();

        $domain = null;

        Domain::withoutEvents(function () use (&$domain): void {
            $domain = Domain::factory()->create([
                'domain' => 'jabali-panel.com',
                'ip_address' => null,
                'ipv6_address' => null,
            ]);
        });

        $this->assertNotNull($domain);

        $service = new MigrationDnsSyncService($this->createMock(AgentClient::class));
        $method = new ReflectionMethod($service, 'getDefaultRecords');
        $method->setAccessible(true);

        $records = $method->invoke($service, $domain);

        $this->assertTrue($this->recordExists($records, 'ns1', 'A', '182.54.236.100'));
        $this->assertTrue($this->recordExists($records, 'ns2', 'A', '182.54.236.100'));
        $this->assertTrue($this->recordExists($records, 'ns1', 'AAAA', '2001:db8::1'));
        $this->assertTrue($this->recordExists($records, 'ns2', 'AAAA', '2001:db8::1'));
    }

    /**
     * @param  array<int, array<string, mixed>>  $records
     */
    private function recordExists(array $records, string $name, string $type, string $content): bool
    {
        foreach ($records as $record) {
            if (($record['name'] ?? null) !== $name) {
                continue;
            }

            if (($record['type'] ?? null) !== $type) {
                continue;
            }

            if (($record['content'] ?? null) !== $content) {
                continue;
            }

            return true;
        }

        return false;
    }
}
