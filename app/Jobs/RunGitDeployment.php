<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\GitDeployment;
use App\Services\Agent\AgentClient;
use App\Services\Agent\AgentException;
use Exception;
use Illuminate\Bus\Queueable;
use Illuminate\Contracts\Queue\ShouldBeUnique;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Bus\Dispatchable;
use Illuminate\Queue\InteractsWithQueue;
use Illuminate\Queue\SerializesModels;
use Illuminate\Support\Facades\Log;
use Throwable;

class RunGitDeployment implements ShouldBeUnique, ShouldQueue
{
    use Dispatchable;
    use InteractsWithQueue;
    use Queueable;
    use SerializesModels;

    public int $uniqueFor = 600;

    public function __construct(public int $deploymentId) {}

    public function uniqueId(): string
    {
        return "git-deploy-{$this->deploymentId}";
    }

    public function handle(): void
    {
        $deployment = GitDeployment::find($this->deploymentId);
        if (! $deployment) {
            return;
        }

        // Agent v2 path (ADR-0007): dispatch a BackgroundTask. The existing
        // deploy orchestration runs inside the spawned binary via
        // jabali:tasks:run-git-deploy, emitting JSONL progress to the
        // unified dashboard.
        if (config('jabali.agent_v2.git_deploy_enabled')) {
            $dispatcher = app(\App\Services\BackgroundTasks\BackgroundTaskDispatcher::class);
            $task = $dispatcher->dispatch(
                type: \App\Enums\BackgroundTaskType::GitDeploy,
                argv: [
                    '/usr/bin/php',
                    base_path('artisan'),
                    'jabali:tasks:run-git-deploy',
                    '--deployment-id='.$deployment->id,
                ],
                payload: ['deployment_id' => $deployment->id],
                dedupeKey: 'git-deploy:'.$deployment->id,
                targetType: GitDeployment::class,
                targetId: (string) $deployment->id,
                limits: ['cpu' => 50, 'memory' => '512M', 'io' => 100],
            );
            if ($task === null) {
                Log::info("RunGitDeployment: dedupe — deploy already running for {$deployment->id}");
            }

            return;
        }

        $deployment->update([
            'last_status' => 'running',
            'last_error' => null,
        ]);

        $agent = app(AgentClient::class);

        try {
            $agent->call('git.deploy', [
                'username' => $deployment->user->username,
                'repo_url' => $deployment->repo_url,
                'branch' => $deployment->branch,
                'deploy_path' => $deployment->deploy_path,
                'deploy_script' => $deployment->deploy_script,
            ])->orFail();

            $deployment->update([
                'last_status' => 'success',
                'last_deployed_at' => now(),
                'last_error' => null,
            ]);
        } catch (AgentException|Exception $e) {
            $deployment->update([
                'last_status' => 'failed',
                'last_error' => $e->getMessage(),
            ]);

            Log::error('Git deployment failed', [
                'deployment_id' => $deployment->id,
                'error' => $e->getMessage(),
            ]);
        }
    }

    public function failed(Throwable $exception): void
    {
        $deployment = GitDeployment::find($this->deploymentId);

        if ($deployment) {
            $deployment->update([
                'last_status' => 'failed',
                'last_error' => $exception->getMessage(),
            ]);
        }

        Log::error('RunGitDeployment job failed', [
            'deployment_id' => $this->deploymentId,
            'error' => $exception->getMessage(),
        ]);
    }
}
