<?php

declare(strict_types=1);

namespace App\Console\Commands\Cli;

use App\Console\Cli\JabaliCommand;
use Illuminate\Console\Command;

class CgroupCheckCommand extends JabaliCommand
{
    protected $signature = 'jabali:cgroup:check {--json} {--yes}';

    protected $description = 'Check if cgroups v2 is available on this server';

    protected function execute(\Symfony\Component\Console\Input\InputInterface $input, \Symfony\Component\Console\Output\OutputInterface $output): int
    {
        $this->setupFormatter();

        $result = $this->callAgent('cgroup.check');
        if ($result === null) {
            return Command::FAILURE;
        }

        if ($this->option('json')) {
            $this->formatter()->json($result->data);

            return Command::SUCCESS;
        }

        $available = $result->data['available'] ?? false;
        $version = $result->data['version'] ?? 'unknown';
        $controllers = $result->data['controllers'] ?? [];

        if ($available) {
            $this->formatter()->success('cgroups v2 available (controllers: '.implode(', ', $controllers).')');
        } else {
            $this->formatter()->error("cgroups v2 not available (detected: {$version})");
            if ($error = $result->data['error'] ?? null) {
                $this->formatter()->info($error);
            }
        }

        return $available ? Command::SUCCESS : Command::FAILURE;
    }
}
