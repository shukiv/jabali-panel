<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class VersionFileTest extends TestCase
{
    public function test_version_file_contains_version_without_build(): void
    {
        $versionPath = dirname(__DIR__, 2).'/VERSION';
        $content = file_get_contents($versionPath);

        $this->assertNotFalse($content);
        $this->assertMatchesRegularExpression('/^VERSION=0\\.9-rc\\d*$/m', trim($content));
        $this->assertStringNotContainsString('BUILD=', $content);
    }
}
