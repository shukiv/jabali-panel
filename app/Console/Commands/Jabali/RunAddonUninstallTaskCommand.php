<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Services\BackgroundTasks\Binaries\TaskLogWriter;
use Illuminate\Console\Command;

/**
 * Spawned binary for the addon_uninstall task type. Mirrors
 * RunAddonInstallTaskCommand but runs the uninstall_command from the
 * addon catalogue and has to clear Filament's cached routes so the
 * panel doesn't 500 on the now-deleted addon classes.
 */
class RunAddonUninstallTaskCommand extends Command
{
    protected $signature = 'jabali:tasks:run-addon-uninstall
        {--task-id= : Background task UUID}
        {--progress-log= : Path to write JSONL events to}
        {--addon-id= : Addon identifier from config/jabali-addons.php}';

    protected $description = 'Spawned binary: uninstall an addon and emit task progress';

    public function handle(): int
    {
        $logPath = (string) $this->option('progress-log');
        if ($logPath === '') {
            $this->error('--progress-log is required');

            return self::FAILURE;
        }

        $writer = new TaskLogWriter($logPath);
        $writer->started();

        $addonId = (string) $this->option('addon-id');
        $addons = config('jabali-addons', []);
        if (! isset($addons[$addonId])) {
            $writer->failed("Unknown addon: {$addonId}");

            return self::FAILURE;
        }

        $addon = $addons[$addonId];
        $uninstallCmd = $addon['uninstall_command'];

        $writer->step('running uninstall');
        $output = null;
        $exitCode = null;
        $this->shellExec($uninstallCmd.' 2>&1', $output, $exitCode);
        if ($exitCode !== 0) {
            $writer->failed("Uninstaller exited {$exitCode}: ".substr((string) $output, -500));

            return self::FAILURE;
        }

        $writer->progress(50, 'clearing caches (pre-autoload)');
        // rm caches BEFORE dumping autoload — Laravel's cache files hold
        // references to classes that have just been deleted. If artisan
        // boots against the stale cache it fatals.
        $this->shellExec(
            "find /var/www/jabali/bootstrap/cache -mindepth 1 -maxdepth 2 -name '*.php' -delete; ".
            'rm -rf /var/www/jabali/bootstrap/cache/filament/*; '.
            'rm -rf /var/www/jabali/storage/framework/views/*',
            $output,
            $exitCode
        );

        $writer->progress(70, 'refreshing autoload');
        $this->shellExec(
            'cd /var/www/jabali && sudo -u www-data composer dump-autoload --optimize 2>&1',
            $output,
            $exitCode
        );

        $writer->progress(90, 'restarting panel');
        $this->shellExec('sudo -u www-data php /var/www/jabali/artisan optimize:clear', $output, $exitCode);
        $this->shellExec('systemctl restart jabali-panel', $output, $exitCode);

        $writer->done(['addon' => $addonId]);

        return self::SUCCESS;
    }

    private function shellExec(string $cmd, ?string &$output, ?int &$exitCode): void
    {
        $descriptors = [1 => ['pipe', 'w'], 2 => ['pipe', 'w']];
        $proc = proc_open(['/bin/bash', '-c', $cmd], $descriptors, $pipes);
        if (! is_resource($proc)) {
            $output = 'proc_open failed';
            $exitCode = -1;

            return;
        }
        $out = stream_get_contents($pipes[1]) ?: '';
        $err = stream_get_contents($pipes[2]) ?: '';
        fclose($pipes[1]);
        fclose($pipes[2]);
        $exitCode = proc_close($proc);
        $combined = $out.$err;
        $output = strlen($combined) > 4096 ? substr($combined, -4096) : $combined;
    }
}
