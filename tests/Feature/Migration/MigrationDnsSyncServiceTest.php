<?php

declare(strict_types=1);

namespace Tests\Feature\Migration;

use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\Domain;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Migration\MigrationDnsSyncService;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Mockery\Adapter\Phpunit\MockeryPHPUnitIntegration;
use Tests\TestCase;

class MigrationDnsSyncServiceTest extends TestCase
{
    use MockeryPHPUnitIntegration;
    use RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();

        Domain::unsetEventDispatcher();
    }

    protected function tearDown(): void
    {
        Domain::setEventDispatcher(app('events'));

        parent::tearDown();
    }

    public function test_sync_domains_creates_default_dns_records_and_syncs_zone(): void
    {
        DnsSetting::set('default_ipv6', '2001:db8::1');
        DnsSetting::clearCache();
        $user = User::factory()->create([
            'username' => 'migrateduser',
        ]);

        $domain = Domain::factory()->for($user)->create([
            'domain' => 'example.com',
        ]);

        $this->assertSame(0, DnsRecord::where('domain_id', $domain->id)->count());

        $agent = $this->mock(AgentClient::class);
        $agent->shouldReceive('send')
            ->once()
            ->withArgs(function (string $action, array $payload) use ($domain): bool {
                $this->assertSame('dns.sync_zone', $action);
                $this->assertSame($domain->domain, $payload['domain'] ?? null);
                $this->assertNotEmpty($payload['records'] ?? []);

                return true;
            })
            ->andReturn(['success' => true]);

        $service = new MigrationDnsSyncService($agent);
        $service->syncDomainsForUser($user);

        $this->assertDatabaseHas('dns_records', [
            'domain_id' => $domain->id,
            'name' => '@',
            'type' => 'A',
        ]);

        $this->assertDatabaseHas('dns_records', [
            'domain_id' => $domain->id,
            'name' => '@',
            'type' => 'AAAA',
        ]);

        $this->assertDatabaseHas('dns_records', [
            'domain_id' => $domain->id,
            'name' => '@',
            'type' => 'MX',
        ]);

        $this->assertDatabaseHas('dns_records', [
            'domain_id' => $domain->id,
            'name' => '_dmarc',
            'type' => 'TXT',
        ]);
    }

    public function test_sync_domains_uses_existing_dns_records(): void
    {
        $user = User::factory()->create([
            'username' => 'customdns',
        ]);

        $domain = Domain::factory()->for($user)->create([
            'domain' => 'custom.test',
        ]);

        DnsRecord::create([
            'domain_id' => $domain->id,
            'name' => 'app',
            'type' => 'A',
            'content' => '203.0.113.10',
            'ttl' => 3600,
            'priority' => null,
        ]);

        $agent = $this->mock(AgentClient::class);
        $agent->shouldReceive('send')
            ->once()
            ->withArgs(function (string $action, array $payload): bool {
                $this->assertSame('dns.sync_zone', $action);
                $records = collect($payload['records'] ?? []);
                $this->assertTrue($records->contains(fn (array $record): bool => $record['name'] === 'app'));
                $this->assertTrue($records->contains(fn (array $record): bool => $record['name'] === '@' && $record['type'] === 'A'));

                return true;
            })
            ->andReturn(['success' => true]);

        $service = new MigrationDnsSyncService($agent);
        $service->syncDomainsForUser($user);

        $this->assertDatabaseHas('dns_records', [
            'domain_id' => $domain->id,
            'name' => 'app',
            'type' => 'A',
        ]);

        $this->assertDatabaseHas('dns_records', [
            'domain_id' => $domain->id,
            'name' => '@',
            'type' => 'A',
        ]);
    }
}
