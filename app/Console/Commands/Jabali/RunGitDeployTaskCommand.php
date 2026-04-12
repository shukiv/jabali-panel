<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Jobs\RunGitDeployment;
use App\Models\GitDeployment;
use App\Services\BackgroundTasks\Binaries\TaskLogWriter;
use Illuminate\Console\Command;

/**
 * Spawned binary for the git_deploy task type.
 *
 * Calls the legacy RunGitDeployment path (with the agent v2 feature flag
 * temporarily bypassed to avoid recursion) and emits started/done/failed
 * JSONL events. Granular progress within the deploy is not yet reported —
 * the existing service doesn't instrument itself. Future work.
 */
class RunGitDeployTaskCommand extends Command
{
    protected $signature = 'jabali:tasks:run-git-deploy
        {--task-id= : Background task UUID}
        {--progress-log= : Path to write JSONL events to}
        {--deployment-id= : GitDeployment id}';

    protected $description = 'Spawned binary: run a git deploy and emit task progress';

    public function handle(): int
    {
        $logPath = (string) $this->option('progress-log');
        if ($logPath === '') {
            $this->error('--progress-log is required');

            return self::FAILURE;
        }

        $writer = new TaskLogWriter($logPath);
        $writer->started();

        $deploymentId = (int) $this->option('deployment-id');
        $deployment = GitDeployment::find($deploymentId);
        if ($deployment === null) {
            $writer->failed("GitDeployment {$deploymentId} not found");

            return self::FAILURE;
        }

        $writer->step('deploying '.$deployment->repository_url);

        try {
            // Disable the flag inside the spawned binary so we fall through
            // to the legacy synchronous path instead of recursing.
            config(['jabali.agent_v2.git_deploy_enabled' => false]);
            (new RunGitDeployment($deployment->id))->handle();
        } catch (\Throwable $e) {
            $writer->failed($e->getMessage());

            return self::FAILURE;
        }

        $fresh = $deployment->fresh();
        if (($fresh->last_status ?? '') === 'failed') {
            $writer->failed((string) ($fresh->last_error ?? 'deploy failed'));

            return self::FAILURE;
        }

        $writer->done(['status' => $fresh->last_status]);

        return self::SUCCESS;
    }
}
