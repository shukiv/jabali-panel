<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class ReadmeScreenshotsTest extends TestCase
{
    public function test_readme_does_not_reference_screenshot_assets(): void
    {
        $readmePath = dirname(__DIR__, 2).'/README.md';
        $content = file_get_contents($readmePath);

        $this->assertNotFalse($content);
        $this->assertStringNotContainsString('docs/screenshots/', $content);
    }
}
