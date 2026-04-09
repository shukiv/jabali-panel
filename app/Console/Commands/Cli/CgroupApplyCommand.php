<?php

declare(strict_types=1);

namespace App\Console\Commands\Cli;

use App\Console\Cli\JabaliCommand;
use App\Models\User;
use Illuminate\Console\Command;

class CgroupApplyCommand extends JabaliCommand
{
    protected $signature = 'jabali:cgroup:apply
        {username : Linux username}
        {--all : Apply to all users with resource limits}
        {--json} {--yes}';

    protected $description = 'Apply cgroup v2 resource limits for a user';

    protected function execute(\Symfony\Component\Console\Input\InputInterface $input, \Symfony\Component\Console\Output\OutputInterface $output): int
    {
        $this->setupFormatter();

        if ($this->option('all')) {
            return $this->applyAll();
        }

        $username = $this->argument('username');
        $user = User::where('username', $username)->first();

        if (! $user) {
            $this->formatter()->error("User '{$username}' not found in database");

            return Command::FAILURE;
        }

        $limits = $user->getEffectiveResourceLimits();

        if (empty($limits)) {
            $this->formatter()->info("No resource limits configured for '{$username}'");

            return Command::SUCCESS;
        }

        $result = $this->callAgent('cgroup.apply', array_merge(['username' => $username], $limits));
        if ($result === null) {
            return Command::FAILURE;
        }

        if ($this->option('json')) {
            $this->formatter()->json($result->data);

            return Command::SUCCESS;
        }

        $applied = $result->data['applied'] ?? [];
        $parts = [];
        if (isset($applied['cpu_quota'])) {
            $parts[] = "CPU {$applied['cpu_quota']}%";
        }
        if (isset($applied['memory_limit_mb'])) {
            $parts[] = "Memory {$applied['memory_limit_mb']}MB";
        }
        if (isset($applied['io_read_mbps'])) {
            $parts[] = "IO-R {$applied['io_read_mbps']}MB/s";
        }
        if (isset($applied['io_write_mbps'])) {
            $parts[] = "IO-W {$applied['io_write_mbps']}MB/s";
        }
        if (isset($applied['max_processes'])) {
            $parts[] = "Tasks {$applied['max_processes']}";
        }

        $movedPids = $result->data['fpm_pids_moved'] ?? 0;
        $this->formatter()->success("Limits applied for {$username}: ".implode(', ', $parts)." ({$movedPids} FPM workers moved)");

        return Command::SUCCESS;
    }

    private function applyAll(): int
    {
        $users = User::whereNotNull('hosting_package_id')
            ->orWhereNotNull('cpu_quota')
            ->orWhereNotNull('memory_limit_mb')
            ->orWhereNotNull('max_processes')
            ->get();

        $applied = 0;
        $skipped = 0;

        foreach ($users as $user) {
            $limits = $user->getEffectiveResourceLimits();
            if (empty($limits)) {
                $skipped++;

                continue;
            }

            $result = $this->callAgent('cgroup.apply', array_merge(['username' => $user->username], $limits));
            if ($result !== null) {
                $applied++;
            }
        }

        if ($this->option('json')) {
            $this->formatter()->json(['applied' => $applied, 'skipped' => $skipped]);

            return Command::SUCCESS;
        }

        $this->formatter()->success("Applied cgroup limits: {$applied} users ({$skipped} skipped — no limits)");

        return Command::SUCCESS;
    }
}
