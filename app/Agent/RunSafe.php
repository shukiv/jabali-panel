<?php

declare(strict_types=1);

namespace App\Agent;

use InvalidArgumentException;
use RuntimeException;

/**
 * Safer command execution for the jabali-agent.
 *
 * Unlike exec() / shell_exec() / passthru(), this does NOT invoke a shell.
 * Arguments are passed directly to execve() via proc_open()'s array form,
 * so there is no shell interpretation, no PATH lookup, no variable expansion,
 * no globbing, no command substitution, no operator parsing.
 *
 * Hardening rules enforced here:
 *   - First argument must be an absolute path (no PATH lookup).
 *   - All arguments must be strings without null bytes.
 *   - The child process gets the provided env only (empty by default).
 *   - A timeout is always enforced; the child is SIGTERM'd then SIGKILL'd.
 *
 * See docs/adr/0007-agent-v2-control-plane.md.
 */
final class RunSafe
{
    /**
     * @param  list<string>  $argv  First element must be an absolute path.
     * @param  array<string, string>  $env  Explicit environment for the child.
     *                                      Empty by default (no inheritance).
     */
    public static function run(
        array $argv,
        ?string $stdin = null,
        int $timeout = 30,
        array $env = [],
        ?string $cwd = null,
    ): RunResult {
        self::validateArgv($argv);

        $descriptors = [
            0 => ['pipe', 'r'],
            1 => ['pipe', 'w'],
            2 => ['pipe', 'w'],
        ];

        $start = microtime(true);

        $proc = proc_open(
            $argv,
            $descriptors,
            $pipes,
            $cwd,
            $env, // Empty array = explicit empty env (no PATH inheritance).
        );

        if (! is_resource($proc)) {
            throw new RuntimeException('Failed to spawn process: '.$argv[0]);
        }

        try {
            if ($stdin !== null) {
                fwrite($pipes[0], $stdin);
            }
            fclose($pipes[0]);

            // Non-blocking reads so we can enforce our own timeout.
            stream_set_blocking($pipes[1], false);
            stream_set_blocking($pipes[2], false);

            $stdout = '';
            $stderr = '';
            $deadline = microtime(true) + $timeout;

            while (true) {
                $elapsed = microtime(true) - $start;
                if ($elapsed > $timeout) {
                    self::terminate($proc);
                    throw new RuntimeException(
                        sprintf('Process timeout after %d seconds: %s', $timeout, $argv[0])
                    );
                }

                $status = proc_get_status($proc);
                $stdout .= stream_get_contents($pipes[1]) ?: '';
                $stderr .= stream_get_contents($pipes[2]) ?: '';

                if (! $status['running']) {
                    break;
                }

                // Short sleep to avoid busy-looping.
                $readable = [$pipes[1], $pipes[2]];
                $write = null;
                $except = null;
                $remaining = max(0.05, $deadline - microtime(true));
                $sec = (int) $remaining;
                $usec = (int) (($remaining - $sec) * 1_000_000);
                @stream_select($readable, $write, $except, $sec, $usec);
            }

            fclose($pipes[1]);
            fclose($pipes[2]);

            $exitCode = $status['exitcode'];
            if ($exitCode === -1) {
                // Fallback: sometimes proc_get_status reports -1 after close.
                $exitCode = proc_close($proc);
            } else {
                proc_close($proc);
            }

            $durationMs = (int) ((microtime(true) - $start) * 1000);

            return new RunResult(
                exitCode: $exitCode,
                stdout: $stdout,
                stderr: $stderr,
                durationMs: $durationMs,
            );
        } catch (\Throwable $e) {
            // Make sure we don't leak the process on any error path.
            if (is_resource($proc)) {
                @self::terminate($proc);
            }
            throw $e;
        }
    }

    /**
     * @param  list<string>  $argv
     */
    private static function validateArgv(array $argv): void
    {
        if (count($argv) === 0) {
            throw new InvalidArgumentException('argv must not be empty');
        }

        foreach ($argv as $i => $arg) {
            if (! is_string($arg)) {
                throw new InvalidArgumentException("argv[{$i}] must be a string, got ".gettype($arg));
            }
            if (str_contains($arg, "\0")) {
                throw new InvalidArgumentException("argv[{$i}] contains null byte");
            }
        }

        if (! str_starts_with($argv[0], '/')) {
            throw new InvalidArgumentException(
                "argv[0] must be an absolute path, got: {$argv[0]}"
            );
        }
    }

    /**
     * @param  resource  $proc
     */
    private static function terminate($proc): void
    {
        @proc_terminate($proc, 15); // SIGTERM

        // Give it up to 2 seconds to exit cleanly.
        $deadline = microtime(true) + 2.0;
        while (microtime(true) < $deadline) {
            $status = @proc_get_status($proc);
            if (! $status || ! $status['running']) {
                break;
            }
            usleep(50_000);
        }

        $status = @proc_get_status($proc);
        if ($status && $status['running']) {
            @proc_terminate($proc, 9); // SIGKILL
        }

        @proc_close($proc);
    }
}
