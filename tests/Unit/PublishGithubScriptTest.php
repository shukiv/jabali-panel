<?php

declare(strict_types=1);

namespace Tests\Unit;

use PHPUnit\Framework\TestCase;

class PublishGithubScriptTest extends TestCase
{
    public function test_publish_github_script_exists_and_protects_claude(): void
    {
        $scriptPath = dirname(__DIR__, 2).'/bin/publish-github';

        $this->assertFileExists($scriptPath);
        $this->assertTrue(is_executable($scriptPath));

        $content = file_get_contents($scriptPath);

        $this->assertNotFalse($content);
        $this->assertStringContainsString('CLAUDE.md', $content);
        $this->assertStringContainsString('github-main', $content);
        $this->assertStringContainsString('github', $content);
    }
}
