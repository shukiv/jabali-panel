<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Models\GitDeployment;
use App\Services\Agent\AgentClient;
use Exception;
use Illuminate\Bus\Queueable;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Bus\Dispatchable;
use Illuminate\Queue\InteractsWithQueue;
use Illuminate\Queue\SerializesModels;
use Illuminate\Support\Facades\Log;

class RunGitDeployment implements ShouldQueue
{
    use Dispatchable;
    use InteractsWithQueue;
    use Queueable;
    use SerializesModels;

    public function __construct(public int $deploymentId) {}

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
            $result = $agent->send('git.deploy', [
                'username' => $deployment->user->username,
                'repo_url' => $deployment->repo_url,
                'branch' => $deployment->branch,
                'deploy_path' => $deployment->deploy_path,
                'deploy_script' => $deployment->deploy_script,
            ]);

            if (! ($result['success'] ?? false)) {
                throw new Exception($result['error'] ?? 'Deployment failed');
            }

            $deployment->update([
                'last_status' => 'success',
                'last_deployed_at' => now(),
                'last_error' => null,
            ]);
        } catch (Exception $e) {
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
}
