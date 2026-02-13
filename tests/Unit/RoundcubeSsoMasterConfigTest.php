<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class RoundcubeSsoMasterConfigTest extends TestCase
{
    public function test_install_script_configures_master_sso(): void
    {
        $script = file_get_contents(base_path('install.sh'));

        $this->assertIsString($script);
        $this->assertStringContainsString('/etc/jabali/roundcube-sso.conf', $script);
        $this->assertStringContainsString('/etc/dovecot/master-users', $script);
        $this->assertStringContainsString('auth-master.conf.ext', $script);
        $this->assertStringContainsString('auth_master_user_separator', $script);
        $this->assertStringContainsString('use_master', $script);
    }
}
