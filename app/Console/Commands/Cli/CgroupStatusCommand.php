<?php

declare(strict_types=1);

namespace App\Console\Commands\Cli;

use App\Console\Cli\JabaliCommand;
use Illuminate\Console\Command;

class CgroupStatusCommand extends JabaliCommand
{
    protected $signature = 'jabali:cgroup:status
        {username : Linux username}
        {--json} {--yes}';

    protected $description = 'Show cgroup resource usage for a user';

    protected function execute(\Symfony\Component\Console\Input\InputInterface $input, \Symfony\Component\Console\Output\OutputInterface $output): int
    {
        $this->setupFormatter();

        $username = $this->argument('username');
        $result = $this->callAgent('cgroup.status', ['username' => $username]);

        if ($result === null) {
            return Command::FAILURE;
        }

        if ($this->option('json')) {
            $this->formatter()->json($result->data);

            return Command::SUCCESS;
        }

        if (! ($result->data['active'] ?? false)) {
            $this->formatter()->info("No active cgroup slice for '{$username}'");

            return Command::SUCCESS;
        }

        $this->formatter()->info("Resource usage for {$username} ({$result->data['slice']}):");

        if ($cpu = $result->data['cpu'] ?? null) {
            $line = "  CPU: {$cpu['usage_seconds']}s total";
            if (isset($cpu['quota_percent'])) {
                $line .= " (quota: {$cpu['quota_percent']}%)";
            }
            $this->formatter()->info($line);
        }

        if ($mem = $result->data['memory'] ?? null) {
            $line = "  Memory: {$mem['used_mb']}MB";
            if (isset($mem['limit_mb'])) {
                $line .= " / {$mem['limit_mb']}MB ({$mem['usage_percent']}%)";
            }
            $this->formatter()->info($line);
        }

        if ($procs = $result->data['processes'] ?? null) {
            $line = "  Processes: {$procs['current']}";
            if (isset($procs['limit'])) {
                $line .= " / {$procs['limit']}";
            }
            $this->formatter()->info($line);
        }

        if ($io = $result->data['io'] ?? null) {
            $this->formatter()->info("  I/O: read {$io['read_mb']}MB, write {$io['write_mb']}MB");
        }

        return Command::SUCCESS;
    }
}
