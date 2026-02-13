<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class ServerSettingsPhpFpmServiceTest extends TestCase
{
    public function test_save_hostname_skips_php_fpm_reload(): void
    {
        $source = file_get_contents(__DIR__.'/../../app/Filament/Admin/Pages/ServerSettings.php');

        $this->assertIsString($source);
        $this->assertStringNotContainsString('resolvePhpFpmService', $source);
        $this->assertStringNotContainsString('php8.3-fpm', $source);
    }
}
