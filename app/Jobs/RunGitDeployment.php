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
