<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class NginxVhostRetryTest extends TestCase
{
    public function test_vhost_templates_include_fastcgi_retry(): void
    {
        $agent = file_get_contents(base_path('bin/jabali-agent'));

        $this->assertIsString($agent);
        $this->assertStringContainsString('fastcgi_next_upstream', $agent);
        $this->assertStringContainsString('fastcgi_next_upstream_tries', $agent);
        $this->assertStringContainsString('fastcgi_next_upstream_timeout', $agent);
    }

    public function test_vhost_stats_alias_uses_document_root(): void
    {
        $agent = file_get_contents(base_path('bin/jabali-agent'));

        $this->assertIsString($agent);
        $this->assertStringContainsString('$stats = rtrim($publicHtml, \'/\') . \'/stats\';', $agent);
        $this->assertStringContainsString('location /stats/', $agent);
        $this->assertStringContainsString('alias STATS_PLACEHOLDER/', $agent);
    }
}
