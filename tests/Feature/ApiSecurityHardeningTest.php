<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Jobs\RunGitDeployment;
use App\Models\Domain;
use App\Models\GitDeployment;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Bus;
use Tests\TestCase;

class ApiSecurityHardeningTest extends TestCase
{
    use RefreshDatabase;

    public function test_git_webhook_rejects_unsigned_request_on_tokenless_route(): void
    {
        Bus::fake();
        $deployment = $this->createDeployment();

        $response = $this->postJson("/api/webhooks/git/{$deployment->id}", ['ref' => 'refs/heads/main']);

        $response->assertStatus(403);
        Bus::assertNotDispatched(RunGitDeployment::class);
    }

    public function test_git_webhook_accepts_hmac_signature(): void
    {
        Bus::fake();
        $deployment = $this->createDeployment();
        $payload = ['ref' => 'refs/heads/main'];
        $signature = hash_hmac('sha256', (string) json_encode($payload), $deployment->secret_token);

        $response = $this
            ->withHeader('X-Jabali-Signature', $signature)
            ->postJson("/api/webhooks/git/{$deployment->id}", $payload);

        $response->assertStatus(200);
        Bus::assertDispatched(RunGitDeployment::class);
    }

    public function test_git_webhook_accepts_legacy_token_route(): void
    {
        Bus::fake();
        $deployment = $this->createDeployment();

        $response = $this->postJson(
            "/api/webhooks/git/{$deployment->id}/{$deployment->secret_token}",
            ['ref' => 'refs/heads/main']
        );

        $response->assertStatus(200);
        Bus::assertDispatched(RunGitDeployment::class);
    }

    public function test_internal_api_rejects_non_local_without_internal_token(): void
    {
        config()->set('app.internal_api_token', null);

        $response = $this
            ->withServerVariables(['REMOTE_ADDR' => '203.0.113.10'])
            ->postJson('/api/internal/page-cache', []);

        $response->assertStatus(403);
    }

    public function test_internal_api_allows_non_local_with_internal_token(): void
    {
        config()->set('app.internal_api_token', 'test-internal-token');

        $response = $this
            ->withServerVariables(['REMOTE_ADDR' => '203.0.113.10'])
            ->withHeader('X-Jabali-Internal-Token', 'test-internal-token')
            ->postJson('/api/internal/page-cache', []);

        $response->assertStatus(400);
        $response->assertJson(['error' => 'Domain is required']);
    }

    private function createDeployment(): GitDeployment
    {
        $user = User::factory()->create();
        $domain = Domain::factory()->for($user)->create();

        return GitDeployment::create([
            'user_id' => $user->id,
            'domain_id' => $domain->id,
            'repo_url' => 'https://example.com/repo.git',
            'branch' => 'main',
            'deploy_path' => '/home/'.$user->username.'/domains/'.$domain->domain.'/public_html',
            'auto_deploy' => true,
            'secret_token' => 'test-secret-token-1234567890',
        ]);
    }
}
