<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class AgentResolversTest extends TestCase
{
    public function test_agent_resolver_update_handles_systemd_and_immutable(): void
    {
        $script = file_get_contents(__DIR__.'/../../bin/jabali-agent');

        $this->assertIsString($script);
        $this->assertStringContainsString('serverSetResolvers', $script);
        $this->assertStringContainsString('resolvectl dns', $script);
        $this->assertStringContainsString('chattr -i /etc/resolv.conf', $script);
    }
}
