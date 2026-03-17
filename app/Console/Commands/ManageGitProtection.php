<?php

declare(strict_types=1);

namespace App\Console\Commands;

use Illuminate\Console\Command;
use Illuminate\Support\Str;

class ManageGitProtection extends Command
{
    protected $signature = 'jabali:git-protection
                            {action : enable|disable|status|add-committer|remove-committer|list-committers}
                            {--email= : Email address for add/remove committer}';

    protected $description = 'Manage Git commit protection for Jabali';

    protected string $authFile;

    protected string $hookFile;

    protected string $deployKeyFile;

    public function __construct()
    {
        parent::__construct();
        $this->authFile = base_path('.git-authorized-committers');
        $this->hookFile = base_path('.git/hooks/pre-commit');
        $this->deployKeyFile = base_path('.deploy-key');
    }

    public function handle(): int
    {
        $action = $this->argument('action');

        return match ($action) {
            'enable' => $this->enableProtection(),
            'disable' => $this->disableProtection(),
            'status' => $this->showStatus(),
            'add-committer' => $this->addCommitter(),
            'remove-committer' => $this->removeCommitter(),
            'list-committers' => $this->listCommitters(),
            default => $this->showHelp(),
        };
    }

    protected function enableProtection(): int
    {
        // Ensure hook exists and is executable
        if (! file_exists($this->hookFile)) {
            $this->error('Pre-commit hook not found. Please reinstall Jabali.');

            return 1;
        }

        chmod($this->hookFile, 0755);

        // Create authorized committers file if not exists
        if (! file_exists($this->authFile)) {
            // Add current git user as first authorized committer
            $email = trim(shell_exec('git config user.email') ?? '');
            if ($email) {
                file_put_contents($this->authFile, $email."\n");
            } else {
                touch($this->authFile);
            }
            chmod($this->authFile, 0600);
        }

        // Generate deploy key for automated deployments
        if (! file_exists($this->deployKeyFile)) {
            $key = Str::random(64);
            file_put_contents($this->deployKeyFile, $key);
            chmod($this->deployKeyFile, 0600);
            $this->info("Deploy key generated. Use JABALI_DEPLOY_KEY=$key for automated deployments.");
        }

        $this->info('Git commit protection ENABLED.');
        $this->line('Only authorized committers can now make commits.');
        $this->line('');
        $this->line('Authorized committers file: '.$this->authFile);

        return 0;
    }

    protected function disableProtection(): int
    {
        if (file_exists($this->authFile)) {
            unlink($this->authFile);
        }

        $this->info('Git commit protection DISABLED.');
        $this->line('Anyone can now make commits to this repository.');

        return 0;
    }

    protected function showStatus(): int
    {
        $isEnabled = file_exists($this->authFile);

        $this->line('Git Protection Status');
        $this->line('=====================');
        $this->line('');

        if ($isEnabled) {
            $this->info('Status: ENABLED');
            $this->line('');

            $committers = $this->getCommitters();
            if (count($committers) > 0) {
                $this->line('Authorized committers:');
                foreach ($committers as $email) {
                    $this->line("  - $email");
                }
            } else {
                $this->warn('No authorized committers configured!');
                $this->line('No one will be able to commit.');
            }

            if (file_exists($this->deployKeyFile)) {
                $this->line('');
                $this->line('Deploy key: Configured');
            }
        } else {
            $this->warn('Status: DISABLED');
            $this->line('Anyone can make commits.');
        }

        // Show last integrity check status
        $lastCheck = \App\Models\DnsSetting::get('last_integrity_check');
        $lastStatus = \App\Models\DnsSetting::get('last_integrity_status');

        if ($lastCheck) {
            $this->line('');
            $this->line('Last integrity check: '.$lastCheck);
            $this->line('Status: '.($lastStatus === 'clean' ? 'Clean' : 'Modified files detected'));
        }

        return 0;
    }

    protected function addCommitter(): int
    {
        $email = $this->option('email');

        if (! $email) {
            $email = $this->ask('Enter committer email address');
        }

        if (! filter_var($email, FILTER_VALIDATE_EMAIL)) {
            $this->error('Invalid email address.');

            return 1;
        }

        $committers = $this->getCommitters();

        if (in_array($email, $committers)) {
            $this->warn("$email is already an authorized committer.");

            return 0;
        }

        $committers[] = $email;
        file_put_contents($this->authFile, implode("\n", $committers)."\n");
        chmod($this->authFile, 0600);

        $this->info("Added $email to authorized committers.");

        return 0;
    }

    protected function removeCommitter(): int
    {
        $email = $this->option('email');

        if (! $email) {
            $email = $this->ask('Enter committer email to remove');
        }

        $committers = $this->getCommitters();
        $committers = array_filter($committers, fn ($e) => $e !== $email);

        file_put_contents($this->authFile, implode("\n", $committers)."\n");

        $this->info("Removed $email from authorized committers.");

        return 0;
    }

    protected function listCommitters(): int
    {
        $committers = $this->getCommitters();

        if (empty($committers)) {
            $this->warn('No authorized committers configured.');

            return 0;
        }

        $this->info('Authorized committers:');
        foreach ($committers as $email) {
            $this->line("  - $email");
        }

        return 0;
    }

    protected function getCommitters(): array
    {
        if (! file_exists($this->authFile)) {
            return [];
        }

        $content = file_get_contents($this->authFile);
        $lines = array_filter(array_map('trim', explode("\n", $content)));

        return $lines;
    }

    protected function showHelp(): int
    {
        $this->line('Usage: php artisan jabali:git-protection <action>');
        $this->line('');
        $this->line('Actions:');
        $this->line('  enable           Enable commit protection');
        $this->line('  disable          Disable commit protection');
        $this->line('  status           Show protection status');
        $this->line('  add-committer    Add an authorized committer');
        $this->line('  remove-committer Remove an authorized committer');
        $this->line('  list-committers  List all authorized committers');
        $this->line('');
        $this->line('Options:');
        $this->line('  --email=<email>  Email address for add/remove actions');

        return 0;
    }
}
