<?php

declare(strict_types=1);

namespace Tests\Feature\Cli;

use App\Console\Commands\Cli\SslIssueCommand;
use App\Console\Commands\Cli\SslListCommand;
use App\Console\Commands\Cli\SslRenewCommand;
use App\Console\Commands\Cli\SslStatusCommand;
use App\Services\Agent\AgentClientInterface;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class SslCommandTest extends TestCase
{
    use RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();
        $kernel = $this->app[\Illuminate\Contracts\Console\Kernel::class];
        $kernel->registerCommand(new SslListCommand);
        $kernel->registerCommand(new SslIssueCommand);
        $kernel->registerCommand(new SslRenewCommand);
        $kernel->registerCommand(new SslStatusCommand);
    }

    public function test_ssl_list_shows_certificates(): void
    {
        $agent = $this->createMock(AgentClientInterface::class);
        $agent->method('send')->willReturn([
            'success' => true,
            'certificates' => [
                [
                    'domain' => 'example.com',
                    'valid' => true,
                    'issuer' => "Let's Encrypt",
                    'valid_to' => '2026-06-01',
                    'days_remaining' => 67,
                ],
                [
                    'domain' => 'expired.com',
                    'valid' => false,
                    'issuer' => "Let's Encrypt",
                    'valid_to' => '2026-03-01',
                    'days_remaining' => 0,
                ],
            ],
        ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:ssl:list')
            ->assertExitCode(0)
            ->expectsOutputToContain('example.com')
            ->expectsOutputToContain('expired.com');
    }

    public function test_ssl_list_json_output(): void
    {
        $agent = $this->createMock(AgentClientInterface::class);
        $agent->method('send')->willReturn([
            'success' => true,
            'certificates' => [
                [
                    'domain' => 'example.com',
                    'valid' => true,
                    'issuer' => "Let's Encrypt",
                    'valid_to' => '2026-06-01',
                    'days_remaining' => 67,
                ],
            ],
        ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:ssl:list', ['--json' => true])
            ->assertExitCode(0)
            ->expectsOutputToContain('example.com');
    }

    public function test_ssl_issue_calls_agent(): void
    {
        $user = \App\Models\User::factory()->create();
        $domain = \App\Models\Domain::create(['domain' => 'ssl-test-'.$user->id.'.example.com', 'user_id' => $user->id, 'document_root' => "/home/{$user->username}/domains/ssl-test/public_html"]);

        $agent = $this->createMock(AgentClientInterface::class);
        $agent->method('send')->willReturn([
            'success' => true,
            'valid_to' => '2026-06-01',
        ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:ssl:issue', ['domain' => $domain->domain])
            ->assertExitCode(0)
            ->expectsOutputToContain($domain->domain);

        $domain->delete();
        $user->delete();
    }

    public function test_ssl_renew_calls_agent(): void
    {
        $agent = $this->createMock(AgentClientInterface::class);
        $agent->method('send')->willReturn([
            'success' => true,
            'valid_to' => '2026-06-01',
        ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:ssl:renew', ['domain' => 'example.com'])
            ->assertExitCode(0)
            ->expectsOutputToContain('example.com');
    }

    public function test_ssl_status_shows_details(): void
    {
        $agent = $this->createMock(AgentClientInterface::class);
        $agent->method('send')->willReturn([
            'success' => true,
            'valid' => true,
            'issuer' => "Let's Encrypt",
            'valid_from' => '2026-03-01',
            'valid_to' => '2026-06-01',
            'days_remaining' => 67,
        ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:ssl:status', ['domain' => 'example.com'])
            ->assertExitCode(0)
            ->expectsOutputToContain('example.com')
            ->expectsOutputToContain("Let's Encrypt");
    }

    public function test_ssl_issue_failed_returns_exit_code_1(): void
    {
        $agent = $this->createMock(AgentClientInterface::class);
        $agent->method('send')->willReturn([
            'success' => false,
            'error' => 'DNS validation failed',
        ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:ssl:issue', ['domain' => 'example.com'])
            ->assertExitCode(1);
    }
}
