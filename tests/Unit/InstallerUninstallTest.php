<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class InstallerUninstallTest extends TestCase
{
    public function test_uninstall_stops_and_disables_services(): void
    {
        $script = file_get_contents(base_path('install.sh'));

        $this->assertIsString($script);

        $uninstallStart = strpos($script, 'header "Stopping Services"');
        $this->assertNotFalse($uninstallStart);

        $start = strpos($script, 'local services=(', $uninstallStart);
        $this->assertNotFalse($start);

        $end = strpos($script, 'for service in "${services[@]}"', $start);
        $this->assertNotFalse($end);

        $snippet = substr($script, (int) $start, (int) $end - (int) $start);

        $expected = [
            'nginx',
            'php-fpm',
            'php8.4-fpm',
            'mariadb',
            'redis-server',
            'postfix',
            'dovecot',
            'rspamd',
            'opendkim',
            'bind9',
            'named',
            'fail2ban',
            'clamav-daemon',
            'clamav-freshclam',
        ];

        foreach ($expected as $service) {
            $this->assertStringContainsString($service, $snippet);
        }

        $this->assertStringContainsString('systemctl stop "$service"', $script);
        $this->assertStringContainsString('systemctl disable "$service"', $script);
    }
}
