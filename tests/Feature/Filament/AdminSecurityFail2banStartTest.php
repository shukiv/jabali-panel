<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Filament\Admin\Pages\Security;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class TestableSecurityFail2ban extends Security
{
    protected function getAgent(): AgentClient
    {
        return new class extends AgentClient
        {
            public function send(string $action, array $params = []): array
            {
                return match ($action) {
                    'ufw.status' => [
                        'active' => true,
                        'default_incoming' => 'deny',
                        'default_outgoing' => 'allow',
                        'status_text' => 'active',
                    ],
                    'ufw.list_rules' => [
                        'rules' => [],
                    ],
                    'fail2ban.status_light' => [
                        'installed' => true,
                        'running' => false,
                        'version' => '1.0',
                    ],
                    'fail2ban.status' => [
                        'installed' => true,
                        'running' => false,
                        'version' => '1.0',
                        'jails' => [],
                        'total_banned' => 0,
                        'max_retry' => 5,
                        'ban_time' => 600,
                        'find_time' => 600,
                    ],
                    'fail2ban.list_jails' => [
                        'jails' => [],
                    ],
                    'fail2ban.logs' => [
                        'logs' => [],
                    ],
                    'clamav.status_light' => [
                        'installed' => false,
                        'running' => false,
                        'version' => 'Unknown',
                        'realtime_enabled' => false,
                        'realtime_running' => false,
                        'light_mode' => false,
                    ],
                    'ssh.get_settings' => [
                        'success' => true,
                        'password_auth' => true,
                        'pubkey_auth' => true,
                        'port' => 22,
                    ],
                    'service.enable', 'service.start' => [
                        'success' => true,
                    ],
                    default => ['success' => true],
                };
            }
        };
    }
}

class AdminSecurityFail2banStartTest extends TestCase
{
    use RefreshDatabase;

    public function test_fail2ban_start_action_is_callable(): void
    {
        $admin = User::factory()->admin()->create();

        $this->actingAs($admin);

        Livewire::test(TestableSecurityFail2ban::class, ['activeTab' => 'fail2ban'])
            ->assertStatus(200)
            ->call('startFail2ban')
            ->assertStatus(200);
    }
}
