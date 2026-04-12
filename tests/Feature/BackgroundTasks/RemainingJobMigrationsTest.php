<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskType;
use App\Jobs\RunGitDeployment;
use App\Jobs\RunImapSync;
use App\Models\BackgroundTask;
use App\Models\GitDeployment;
use App\Models\ImapSyncTask;
use App\Models\User;
use App\Services\Agent\AgentClientInterface;
use Mockery;
use Tests\TestCase;

class RemainingJobMigrationsTest extends TestCase
{
    protected function tearDown(): void
    {
        Mockery::close();
        BackgroundTask::query()->delete();
        parent::tearDown();
    }

    public function test_git_deployment_dispatches_via_agent_v2_when_flag_on(): void
    {
        config(['jabali.agent_v2.git_deploy_enabled' => true]);

        $user = User::factory()->create();
        $domain = \App\Models\Domain::factory()->for($user)->create();
        $deployment = new GitDeployment([
            'user_id' => $user->id,
            'domain_id' => $domain->id,
            'repo_url' => 'git@example.com:x/y.git',
            'branch' => 'main',
            'deploy_path' => '/home/u/test',
        ]);
        $deployment->secret_token = 'secret';
        $deployment->save();

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($p) use ($deployment) {
                return $p['type'] === BackgroundTaskType::GitDeploy->value
                    && $p['payload']['deployment_id'] === $deployment->id;
            }))
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        (new RunGitDeployment($deployment->id))->handle();

        $this->assertEquals(1, BackgroundTask::query()->where('type', BackgroundTaskType::GitDeploy->value)->count());

        $deployment->delete();
        $user->delete();
    }

    public function test_imap_sync_dispatches_via_agent_v2_when_flag_on(): void
    {
        config(['jabali.agent_v2.imap_sync_enabled' => true]);

        // Skip if Mailbox factory or mailbox infrastructure isn't available
        // in the test DB — this is an integration-level concern beyond the
        // scope of the migration test.
        if (! class_exists(\Database\Factories\MailboxFactory::class)) {
            $this->markTestSkipped('Mailbox factory not available — covered by e2e tests');
        }

        $user = User::factory()->create();
        /** @var \App\Models\Mailbox $mailbox */
        $mailbox = \App\Models\Mailbox::factory()->create();
        $syncTask = ImapSyncTask::create([
            'user_id' => $user->id,
            'mailbox_id' => $mailbox->id,
            'source_host' => 'imap.example.com',
            'source_port' => 993,
            'source_encryption' => 'ssl',
            'source_email' => 'user@example.com',
            'source_password_encrypted' => \Illuminate\Support\Facades\Crypt::encryptString('pw'),
            'folders' => ['INBOX'],
            'batch_id' => 'batch-123',
            'status' => 'pending',
        ]);

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($p) use ($syncTask) {
                return $p['type'] === BackgroundTaskType::ImapSync->value
                    && $p['payload']['sync_task_id'] === $syncTask->id;
            }))
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        (new RunImapSync($syncTask->id))->handle(Mockery::mock(\App\Services\Agent\AgentClient::class));

        $this->assertEquals(1, BackgroundTask::query()->where('type', BackgroundTaskType::ImapSync->value)->count());

        $syncTask->delete();
        $user->delete();
    }

    public function test_git_deployment_takes_legacy_path_when_flag_off(): void
    {
        config(['jabali.agent_v2.git_deploy_enabled' => false]);

        $user = User::factory()->create();
        $domain = \App\Models\Domain::factory()->for($user)->create();
        $deployment = new GitDeployment([
            'user_id' => $user->id,
            'domain_id' => $domain->id,
            'repo_url' => 'git@example.com:x/y.git',
            'branch' => 'main',
            'deploy_path' => '/home/u/test',
        ]);
        $deployment->secret_token = 'secret';
        $deployment->save();

        $agent = Mockery::mock(AgentClientInterface::class);
        // No job.start expected
        $agent->shouldReceive('send')->never()->with('job.start', Mockery::any());
        $this->app->instance(AgentClientInterface::class, $agent);

        try {
            (new RunGitDeployment($deployment->id))->handle();
        } catch (\Throwable) {
            // Legacy path may throw (needs real git/filesystem); we only care
            // that it didn't go through agent v2.
        }

        $this->assertEquals(0, BackgroundTask::query()->count());

        $deployment->delete();
        $user->delete();
    }
}
