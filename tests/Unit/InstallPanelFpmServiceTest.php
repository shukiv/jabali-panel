<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class InstallPanelFpmServiceTest extends TestCase
{
    public function test_install_script_configures_panel_fpm_service(): void
    {
        $script = file_get_contents(__DIR__.'/../../install.sh');

        $this->assertIsString($script);
        $this->assertStringContainsString('php8.4-fpm-panel', $script);
        $this->assertStringContainsString('/run/php/php${PHP_VERSION}-fpm-panel.sock', $script);
    }
}
