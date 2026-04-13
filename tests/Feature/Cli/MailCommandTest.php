<?php

declare(strict_types=1);

namespace Tests\Feature\Cli;

use App\Console\Commands\Cli\MailCreateCommand;
use App\Console\Commands\Cli\MailDeleteCommand;
use App\Console\Commands\Cli\MailListCommand;
use App\Console\Commands\Cli\MailPasswordCommand;
use App\Console\Commands\Cli\MailQuotaCommand;
use App\Services\Agent\AgentClientInterface;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class MailCommandTest extends TestCase
{
    use RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();
        $kernel = $this->app[\Illuminate\Contracts\Console\Kernel::class];
        $kernel->registerCommand(new MailListCommand);
        $kernel->registerCommand(new MailCreateCommand);
        $kernel->registerCommand(new MailDeleteCommand);
        $kernel->registerCommand(new MailPasswordCommand);
        $kernel->registerCommand(new MailQuotaCommand);
    }

    public function test_mail_list_runs_successfully(): void
    {
        $this->artisan('jabali:mail:list')
            ->assertExitCode(0);
    }

    public function test_mail_create_calls_agent(): void
    {
        $agent = $this->createMock(AgentClientInterface::class);
        $agent->expects($this->once())
            ->method('send')
            ->with('email.mailbox_create', $this->callback(function (array $params) {
                return $params['email'] === 'info@example.com'
                    && $params['domain'] === 'example.com';
            }))
            ->willReturn(['success' => true]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:mail:create', ['email' => 'info@example.com'])
            ->assertExitCode(0);
    }

    public function test_mail_delete_requires_confirmation(): void
    {
        $agent = $this->createMock(AgentClientInterface::class);
        $agent->method('send')->willReturn(['success' => true]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:mail:delete', ['email' => 'info@example.com'])
            ->expectsConfirmation("Are you sure you want to delete mailbox 'info@example.com'?", 'no')
            ->assertExitCode(0);
    }

    public function test_mail_password_calls_agent(): void
    {
        $agent = $this->createMock(AgentClientInterface::class);
        $agent->expects($this->once())
            ->method('send')
            ->with('email.mailbox_change_password', $this->callback(function (array $params) {
                return $params['email'] === 'info@example.com'
                    && $params['password'] === 'NewPass123'
                    && $params['domain'] === 'example.com';
            }))
            ->willReturn(['success' => true]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:mail:password', ['email' => 'info@example.com', '--password' => 'NewPass123'])
            ->assertExitCode(0);
    }

    public function test_mail_agent_failure_returns_exit_1(): void
    {
        $agent = $this->createMock(AgentClientInterface::class);
        $agent->method('send')->willReturn([
            'success' => false,
            'error' => 'Mailbox operation failed',
        ]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $this->artisan('jabali:mail:password', ['email' => 'info@example.com', '--password' => 'Test1234'])
            ->assertExitCode(1);
    }
}
