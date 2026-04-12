<?php

declare(strict_types=1);

namespace Tests\Feature\Agent;

use App\Agent\RunResult;
use App\Agent\RunSafe;
use InvalidArgumentException;
use PHPUnit\Framework\TestCase;
use RuntimeException;

class RunSafeTest extends TestCase
{
    public function test_runs_absolute_path_command(): void
    {
        $result = RunSafe::run(['/bin/echo', 'hello world']);

        $this->assertInstanceOf(RunResult::class, $result);
        $this->assertEquals(0, $result->exitCode);
        $this->assertEquals("hello world\n", $result->stdout);
        $this->assertEquals('', $result->stderr);
        $this->assertGreaterThanOrEqual(0, $result->durationMs);
    }

    public function test_captures_stderr(): void
    {
        // /bin/sh -c would interpret; we use a real binary that writes to stderr
        // `/usr/bin/env` prints to stderr when given an unknown flag. But we want
        // a cross-distro reliable way. Use `/bin/ls` on a nonexistent path.
        $result = RunSafe::run(['/bin/ls', '/this/path/does/not/exist/abc123']);

        $this->assertNotEquals(0, $result->exitCode);
        $this->assertNotEmpty($result->stderr);
    }

    public function test_captures_exit_code(): void
    {
        $result = RunSafe::run(['/bin/sh', '-c', 'exit 7']);
        // Even though this uses /bin/sh, we're invoking it explicitly — the
        // shell is the command, not a wrapper. This documents that runSafe
        // doesn't prevent you from *choosing* the shell; it prevents the
        // accidental shell interpretation that exec() brings.
        $this->assertEquals(7, $result->exitCode);
    }

    public function test_rejects_relative_path(): void
    {
        $this->expectException(InvalidArgumentException::class);
        $this->expectExceptionMessageMatches('/absolute path/i');
        RunSafe::run(['echo', 'hello']);
    }

    public function test_rejects_empty_argv(): void
    {
        $this->expectException(InvalidArgumentException::class);
        RunSafe::run([]);
    }

    public function test_rejects_non_string_args(): void
    {
        $this->expectException(InvalidArgumentException::class);
        /** @phpstan-ignore-next-line intentional bad input */
        RunSafe::run(['/bin/echo', 123]);
    }

    public function test_passes_stdin(): void
    {
        $result = RunSafe::run(['/bin/cat'], stdin: 'hello from stdin');
        $this->assertEquals(0, $result->exitCode);
        $this->assertEquals('hello from stdin', $result->stdout);
    }

    public function test_respects_timeout(): void
    {
        $start = microtime(true);

        try {
            RunSafe::run(['/bin/sleep', '10'], timeout: 1);
            $this->fail('Expected timeout exception');
        } catch (RuntimeException $e) {
            $this->assertStringContainsString('timeout', strtolower($e->getMessage()));
        }

        $elapsed = microtime(true) - $start;
        $this->assertLessThan(3.0, $elapsed, 'runSafe must kill the process within a few seconds of timeout');
    }

    public function test_does_not_inherit_parent_path_by_default(): void
    {
        // When no env is passed, PATH should be empty — so bare 'echo' should fail
        // inside the child. But we give it /usr/bin/env -- PATH -- which would print
        // the env. Use printenv instead.
        $result = RunSafe::run(['/usr/bin/printenv', 'PATH']);

        $this->assertEquals('', trim($result->stdout), 'PATH must not leak from parent');
    }

    public function test_explicit_env_is_respected(): void
    {
        $result = RunSafe::run(
            ['/usr/bin/printenv', 'MY_VAR'],
            env: ['MY_VAR' => 'hello']
        );

        $this->assertEquals('hello', trim($result->stdout));
    }

    public function test_rejects_null_byte_in_args(): void
    {
        $this->expectException(InvalidArgumentException::class);
        RunSafe::run(['/bin/echo', "hello\0world"]);
    }

    public function test_result_to_array(): void
    {
        $result = RunSafe::run(['/bin/echo', 'x']);
        $arr = $result->toArray();
        $this->assertArrayHasKey('exit_code', $arr);
        $this->assertArrayHasKey('stdout', $arr);
        $this->assertArrayHasKey('stderr', $arr);
        $this->assertArrayHasKey('duration_ms', $arr);
    }
}
