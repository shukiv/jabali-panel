<?php

declare(strict_types=1);

namespace Tests\Feature\Cli;

use Symfony\Component\Process\Process;
use Tests\TestCase;

class JabaliCliHelpTest extends TestCase
{
    public function test_help_full_includes_backup_and_cpanel_commands(): void
    {
        $process = new Process([PHP_BINARY, base_path('bin/jabali'), '--help-full']);
        $process->run();

        $this->assertSame(0, $process->getExitCode(), $process->getErrorOutput());
        $output = $process->getOutput();

        $this->assertStringContainsString('backup create', $output);
        $this->assertStringContainsString('backup restore', $output);
        $this->assertStringContainsString('cpanel analyze', $output);
        $this->assertStringContainsString('cpanel restore', $output);
    }

    public function test_backup_help_mentions_restore_options(): void
    {
        $process = new Process([PHP_BINARY, base_path('bin/jabali'), 'backup', 'help']);
        $process->run();

        $this->assertSame(0, $process->getExitCode(), $process->getErrorOutput());
        $output = $process->getOutput();

        $this->assertStringContainsString('--no-files', $output);
        $this->assertStringContainsString('backup verify', $output);
    }
}
