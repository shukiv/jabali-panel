<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Services\BackgroundTasks\Binaries\TaskLogWriter;
use Illuminate\Console\Command;

/**
 * Spawned binary for the addon_install task type.
 *
 * Runs the addon's install URL via bash, refreshes composer autoload,
 * clears Laravel/Filament caches, and restarts jabali-panel. Emits JSONL
 * progress events throughout.
 *
 * Because this runs in its own transient systemd unit (spawned by the
 * agent via job.start), it survives the final `systemctl restart
 * jabali-panel`, which would otherwise kill itself if the installer
 * ran inside the panel's cgroup.
 */
class RunAddonInstallTaskCommand extends Command
{
    protected $signature = 'jabali:tasks:run-addon-install
        {--task-id= : Background task UUID}
        {--progress-log= : Path to write JSONL events to}
        {--addon-id= : Addon identifier from config/jabali-addons.php}';

    protected $description = 'Spawned binary: install an addon and emit task progress';

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
        $installUrl = $addon['install_url'];

        $writer->step('downloading installer');

        // curl | bash pattern — the installer is trusted (comes from the
        // panel's addon catalogue, not user input). The URL is under
        // jabali's control.
        $cmd = 'curl -fsSL '.escapeshellarg($installUrl).' | bash';
        $output = null;
        $exitCode = null;
        $this->shellExec($cmd, $output, $exitCode);
        if ($exitCode !== 0) {
            $writer->failed("Installer exited {$exitCode}: ".substr((string) $output, -500));

            return self::FAILURE;
        }

        $writer->progress(60, 'refreshing autoload');
        $this->shellExec(
            'cd /var/www/jabali && sudo -u www-data composer dump-autoload --optimize 2>&1',
            $output,
            $exitCode
        );

        $writer->progress(80, 'clearing caches');
        $this->shellExec(
            "find /var/www/jabali/bootstrap/cache -mindepth 1 -maxdepth 2 -name '*.php' -delete; ".
            'rm -rf /var/www/jabali/bootstrap/cache/filament/*; '.
            'rm -rf /var/www/jabali/storage/framework/views/*; '.
            'sudo -u www-data php /var/www/jabali/artisan optimize:clear 2>&1',
            $output,
            $exitCode
        );

        $writer->progress(95, 'restarting panel');
        // Fire-and-forget: panel restart will disconnect us if we're under
        // the panel's cgroup, but we're in our own transient unit so we
        // survive. Subsequent events still reach the agent via the log file.
        $this->shellExec('systemctl restart jabali-panel 2>&1', $output, $exitCode);

        $writer->done(['addon' => $addonId]);

        return self::SUCCESS;
    }

    /**
     * Run a shell command, capturing output and exit code.
     *
     * @param  string|null  $output  Combined stdout + stderr, last 4KB.
     */
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
