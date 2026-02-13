<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class CpanelMailboxPasswordHashTest extends TestCase
{
    public function test_agent_reads_mailbox_password_hashes_from_cpanel_backup(): void
    {
        $agent = file_get_contents(base_path('bin/jabali-agent'));

        $this->assertIsString($agent);
        $this->assertStringContainsString('function cpanelFindMailboxPasswordHash', $agent);
        $this->assertStringContainsString('/homedir/etc/', $agent);
        $this->assertStringContainsString('/shadow', $agent);
        $this->assertStringContainsString('/passwd', $agent);
        $this->assertStringContainsString('function cpanelNormalizeMailboxHash', $agent);
        $this->assertStringContainsString('{CRYPT}', $agent);
    }

    public function test_agent_stores_migrated_mailbox_hashes_in_database(): void
    {
        $agent = file_get_contents(base_path('bin/jabali-agent'));

        $this->assertIsString($agent);
        $this->assertStringContainsString('cpanelFindMailboxPasswordHash', $agent);
        $this->assertStringContainsString('cpanelEnsureMailboxRecord', $agent);
    }
}
