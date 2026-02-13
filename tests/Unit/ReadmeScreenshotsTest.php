<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class ReadmeScreenshotsTest extends TestCase
{
    public function test_readme_references_screenshot_assets(): void
    {
        $readmePath = dirname(__DIR__, 2).'/README.md';
        $content = file_get_contents($readmePath);

        $this->assertNotFalse($content);
        $this->assertStringContainsString('docs/screenshots/admin-dashboard.png', $content);
        $this->assertStringContainsString('docs/screenshots/admin-server-status.png', $content);
        $this->assertStringContainsString('docs/screenshots/admin-server-settings.png', $content);
        $this->assertStringContainsString('docs/screenshots/admin-security.png', $content);
        $this->assertStringContainsString('docs/screenshots/admin-users.png', $content);
        $this->assertStringContainsString('docs/screenshots/admin-ssl-manager.png', $content);
        $this->assertStringContainsString('docs/screenshots/admin-dns-zones.png', $content);
        $this->assertStringContainsString('docs/screenshots/admin-backups.png', $content);
        $this->assertStringContainsString('docs/screenshots/admin-services.png', $content);
        $this->assertStringContainsString('docs/screenshots/user-dashboard.png', $content);
        $this->assertStringContainsString('docs/screenshots/user-domains.png', $content);
        $this->assertStringContainsString('docs/screenshots/user-backups.png', $content);
        $this->assertStringContainsString('docs/screenshots/user-cpanel-migration.png', $content);
    }
}
