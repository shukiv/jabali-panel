<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class AgentAllowedServicesTest extends TestCase
{
    public function test_php83_fpm_is_allowed(): void
    {
        $script = file_get_contents(__DIR__.'/../../bin/jabali-agent');

        $this->assertIsString($script);
        $this->assertStringContainsString('php8.3-fpm', $script);
    }
}
