<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Filament\Admin\Pages\Security;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class TestableSecurity extends Security
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
                        'running' => true,
                        'version' => '1.0',
                    ],
                    'clamav.status_light' => [
                        'installed' => true,
                        'running' => false,
                        'version' => '1.0',
                        'realtime_enabled' => false,
                        'realtime_running' => false,
                        'light_mode' => false,
                    ],
                    'clamav.status' => [
                        'installed' => true,
                        'running' => false,
                        'version' => '1.0',
                        'signature_count' => 100,
                        'last_update' => '2026-02-01 00:00:00',
                        'recent_threats' => [],
                        'quarantined_files' => [],
                        'realtime_enabled' => false,
                        'realtime_running' => false,
                        'light_mode' => false,
                        'signature_databases' => [],
                    ],
                    'ssh.get_settings' => [
                        'success' => true,
                        'password_auth' => true,
                        'pubkey_auth' => true,
                        'port' => 22,
                    ],
                    default => ['success' => true],
                };
            }
        };
    }
}

class AdminSecurityAntivirusToggleTest extends TestCase
{
    use RefreshDatabase;

    public function test_antivirus_tab_renders_enable_toggle(): void
    {
        $admin = User::factory()->admin()->create();

        $this->actingAs($admin);

        Livewire::test(TestableSecurity::class, ['activeTab' => 'antivirus'])
            ->assertStatus(200)
            ->assertSee('ClamAV Status')
            ->assertSee('Enable');
    }
}
