<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Models\DnsSetting;
use Illuminate\Console\Command;

class CheckFileIntegrity extends Command
{
    protected $signature = 'jabali:check-integrity
                            {--fix : Restore modified files from Git}
                            {--notify : Send email notification if changes detected}';

    protected $description = 'Check for unauthorized modifications to Jabali core files using Git';

    protected array $protectedPaths = [
        'app/',
        'config/',
        'routes/',
        'database/migrations/',
        'resources/views/',
        'public/index.php',
        'artisan',
        'composer.json',
        'composer.lock',
    ];

    protected array $ignoredPaths = [
        'storage/',
        'bootstrap/cache/',
        'public/build/',
        'public/vendor/',
        'node_modules/',
        '.env',
    ];

    public function handle(): int
    {
        $basePath = base_path();

        // Check if we're in a Git repository
        if (! is_dir("$basePath/.git")) {
            $this->error('Not a Git repository. File integrity checking requires Git.');

            return 1;
        }

        $this->info('Checking file integrity...');

        // Get modified files from Git
        $modifiedFiles = $this->getModifiedFiles($basePath);
        $untrackedFiles = $this->getUntrackedFiles($basePath);

        // Filter to only protected paths
        $modifiedFiles = $this->filterProtectedPaths($modifiedFiles);
        $untrackedFiles = $this->filterProtectedPaths($untrackedFiles);

        $hasChanges = ! empty($modifiedFiles) || ! empty($untrackedFiles);

        if (! $hasChanges) {
            $this->info('All core files are intact. No unauthorized modifications detected.');
            DnsSetting::set('last_integrity_check', now()->toIso8601String());
            DnsSetting::set('last_integrity_status', 'clean');

            return 0;
        }

        // Report modified files
        if (! empty($modifiedFiles)) {
            $this->warn('Modified files detected:');
            $this->table(['File', 'Status'], array_map(fn ($f) => [$f['file'], $f['status']], $modifiedFiles));
        }

        // Report untracked files in protected directories
        if (! empty($untrackedFiles)) {
            $this->warn('Untracked files in protected directories:');
            foreach ($untrackedFiles as $file) {
                $this->line("  - $file");
            }
        }

        // Store status
        DnsSetting::set('last_integrity_check', now()->toIso8601String());
        DnsSetting::set('last_integrity_status', 'modified');
        DnsSetting::set('integrity_modified_files', json_encode($modifiedFiles));
        DnsSetting::set('integrity_untracked_files', json_encode($untrackedFiles));

        // Send notification if requested
        if ($this->option('notify')) {
            $this->sendNotification($modifiedFiles, $untrackedFiles);
        }

        // Restore files if requested
        if ($this->option('fix')) {
            return $this->restoreFiles($basePath, $modifiedFiles);
        }

        $this->newLine();
        $this->warn('Run with --fix to restore modified files from Git.');

        return 1;
    }

    protected function getModifiedFiles(string $basePath): array
    {
        $output = [];
        exec('cd '.escapeshellarg($basePath).' && git status --porcelain 2>/dev/null', $output);

        $files = [];
        foreach ($output as $line) {
            if (strlen($line) < 3) {
                continue;
            }

            $status = trim(substr($line, 0, 2));
            $file = trim(substr($line, 3));

            // Skip untracked files (handled separately)
            if ($status === '??') {
                continue;
            }

            // Map status codes
            $statusMap = [
                'M' => 'Modified',
                'A' => 'Added',
                'D' => 'Deleted',
                'R' => 'Renamed',
                'C' => 'Copied',
                'MM' => 'Modified (staged + unstaged)',
                'AM' => 'Added + Modified',
            ];

            $files[] = [
                'file' => $file,
                'status' => $statusMap[$status] ?? $status,
                'raw_status' => $status,
            ];
        }

        return $files;
    }

    protected function getUntrackedFiles(string $basePath): array
    {
        $output = [];
        exec('cd '.escapeshellarg($basePath)." && git status --porcelain 2>/dev/null | grep '^??' | cut -c4-", $output);

        return $output;
    }

    protected function filterProtectedPaths(array $files): array
    {
        return array_filter($files, function ($item) {
            $file = is_array($item) ? $item['file'] : $item;

            // Check if in ignored paths
            foreach ($this->ignoredPaths as $ignored) {
                if (str_starts_with($file, $ignored)) {
                    return false;
                }
            }

            // Check if in protected paths
            foreach ($this->protectedPaths as $protected) {
                if (str_starts_with($file, $protected)) {
                    return true;
                }
            }

            return false;
        });
    }

    protected function restoreFiles(string $basePath, array $modifiedFiles): int
    {
        if (empty($modifiedFiles)) {
            $this->info('No files to restore.');

            return 0;
        }

        $this->warn('Restoring modified files from Git...');

        foreach ($modifiedFiles as $file) {
            $filePath = $file['file'];
            $status = $file['raw_status'];

            // Skip deleted files - they need to be restored
            if (str_contains($status, 'D')) {
                exec('cd '.escapeshellarg($basePath).' && git checkout HEAD -- '.escapeshellarg($filePath).' 2>&1', $output, $code);
            } else {
                // Reset modifications
                exec('cd '.escapeshellarg($basePath).' && git checkout -- '.escapeshellarg($filePath).' 2>&1', $output, $code);
            }

            if ($code === 0) {
                $this->info("  Restored: $filePath");
            } else {
                $this->error("  Failed to restore: $filePath");
            }
        }

        // Clear caches after restoration
        $this->call('cache:clear');
        $this->call('config:clear');
        $this->call('view:clear');

        DnsSetting::set('last_integrity_status', 'restored');
        $this->info('File restoration complete.');

        return 0;
    }

    protected function sendNotification(array $modifiedFiles, array $untrackedFiles): void
    {
        $recipients = DnsSetting::get('admin_email_recipients');
        if (empty($recipients)) {
            $this->warn('No admin email recipients configured. Skipping notification.');

            return;
        }

        $hostname = gethostname();
        $subject = "[Jabali Security] File integrity alert on $hostname";

        $message = "File integrity check detected unauthorized modifications:\n\n";

        if (! empty($modifiedFiles)) {
            $message .= "MODIFIED FILES:\n";
            foreach ($modifiedFiles as $file) {
                $message .= "  - {$file['file']} ({$file['status']})\n";
            }
            $message .= "\n";
        }

        if (! empty($untrackedFiles)) {
            $message .= "UNTRACKED FILES IN PROTECTED DIRECTORIES:\n";
            foreach ($untrackedFiles as $file) {
                $message .= "  - $file\n";
            }
            $message .= "\n";
        }

        $message .= "To restore files, run: php artisan jabali:check-integrity --fix\n";
        $message .= "\nTime: ".now()->toDateTimeString();

        foreach (explode(',', $recipients) as $email) {
            $email = trim($email);
            if (filter_var($email, FILTER_VALIDATE_EMAIL)) {
                mail($email, $subject, $message);
            }
        }

        $this->info('Notification sent to admin.');
    }
}
