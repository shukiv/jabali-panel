<?php

declare(strict_types=1);

namespace App\Support;

/**
 * Validates cron commands against an executable allowlist.
 *
 * This class is the single source of truth for cron command validation,
 * shared by the Filament UI, the RunUserCronJobs command, and the agent.
 */
class CronCommandValidator
{
    /** @var list<string> */
    private static array $allowedExecutables = [
        '/usr/bin/php',
        '/usr/bin/php8.2',
        '/usr/bin/php8.3',
        '/usr/bin/php8.4',
        '/usr/bin/php8.5',
        '/usr/local/bin/php',
        '/usr/bin/curl',
        '/usr/bin/wget',
        '/usr/bin/wp',
        '/usr/local/bin/wp',
        '/usr/bin/mysql',
        '/usr/bin/mysqldump',
        '/usr/bin/psql',
        '/usr/bin/pg_dump',
        '/usr/bin/tar',
        '/usr/bin/gzip',
        '/usr/bin/gunzip',
        '/usr/bin/rsync',
        '/usr/bin/find',
        '/usr/bin/python3',
        '/usr/bin/node',
        '/usr/bin/ruby',
        '/usr/bin/perl',
    ];

    /**
     * Validate a cron command against the executable allowlist.
     *
     * @return true|string True on success, error message string on failure.
     */
    public static function validate(string $command): bool|string
    {
        $command = trim($command);

        if ($command === '') {
            return __('Command cannot be empty.');
        }

        // Block dangerous shell operators that could chain arbitrary commands
        $dangerousPatterns = [';', '|', '&&', '||', '`', '$(', '${', '>', '<', "\n", "\r"];
        foreach ($dangerousPatterns as $pattern) {
            if (str_contains($command, $pattern)) {
                return __('Shell operators are not allowed. Create separate cron jobs for each command.');
            }
        }

        // Extract the executable (first token)
        $parts = preg_split('/\s+/', $command, 2);
        $executable = $parts[0];

        // Must be an absolute path
        if (! str_starts_with($executable, '/')) {
            return __('Command must start with an absolute path (e.g., /usr/bin/php).');
        }

        // Check against allowlist
        if (! in_array($executable, self::$allowedExecutables, true)) {
            return __('This executable is not allowed. Permitted: php, curl, wget, wp, mysql, mysqldump, tar, rsync, python3, node, ruby, perl.');
        }

        return true;
    }
}
