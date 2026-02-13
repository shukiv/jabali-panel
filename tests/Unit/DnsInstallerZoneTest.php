<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class DnsInstallerZoneTest extends TestCase
{
    public function test_install_script_uses_dns_sync_zone_for_records(): void
    {
        $script = file_get_contents(base_path('install.sh'));

        $this->assertIsString($script);
        $this->assertStringContainsString('dns.sync_zone', $script);
    }

    public function test_agent_dns_create_zone_honors_records_payload(): void
    {
        $agent = file_get_contents(base_path('bin/jabali-agent'));

        $this->assertIsString($agent);
        $start = strpos($agent, 'function dnsCreateZone');
        $this->assertNotFalse($start);

        $end = strpos($agent, 'function dnsSyncZone', $start);
        $this->assertNotFalse($end);

        $snippet = substr($agent, (int) $start, (int) $end - (int) $start);
        $this->assertStringContainsString('$records = $params[\'records\'] ?? null;', $snippet);
        $this->assertStringContainsString('dnsSyncZone(', $snippet);
    }
}
