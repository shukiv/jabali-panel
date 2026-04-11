<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Filament\Admin\Pages\ServerSettings;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Mockery;
use Tests\TestCase;

class ServerSettingsAddonTest extends TestCase
{
    use RefreshDatabase;

    private User $admin;

    protected function setUp(): void
    {
        parent::setUp();
        $this->admin = User::factory()->admin()->create();
    }

    public function test_addons_tab_loads(): void
    {
        $response = $this->actingAs($this->admin, 'admin')
            ->get('/jabali-admin/server-settings?tab=addons');

        $response->assertOk();
    }

    public function test_uninstall_addon_calls_agent_and_returns_success(): void
    {
        $agent = Mockery::mock(AgentClient::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('addon.uninstall', ['addon' => 'jabali-backup'])
            ->andReturn([
                'success' => true,
                'message' => 'Backup uninstall started. The page will update shortly.',
            ]);
        $this->app->instance(AgentClient::class, $agent);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class)
            ->call('uninstallAddon', 'jabali-backup')
            ->assertNotified('Backup uninstall started. The page will update shortly.');
    }

    public function test_uninstall_addon_shows_error_on_failure(): void
    {
        $agent = Mockery::mock(AgentClient::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('addon.uninstall', ['addon' => 'jabali-backup'])
            ->andReturn([
                'success' => false,
                'error' => 'Uninstall failed',
                'output' => 'Permission denied',
            ]);
        $this->app->instance(AgentClient::class, $agent);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class)
            ->call('uninstallAddon', 'jabali-backup')
            ->assertNotified('Uninstall failed');
    }

    public function test_install_addon_calls_agent_and_returns_success(): void
    {
        $agent = Mockery::mock(AgentClient::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('addon.install', ['addon' => 'jabali-backup'])
            ->andReturn([
                'success' => true,
                'message' => 'Backup installation started. The page will update shortly.',
            ]);
        $this->app->instance(AgentClient::class, $agent);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class)
            ->call('installAddon', 'jabali-backup')
            ->assertNotified('Backup installation started. The page will update shortly.');
    }

    public function test_install_addon_shows_error_on_failure(): void
    {
        $agent = Mockery::mock(AgentClient::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('addon.install', ['addon' => 'jabali-backup'])
            ->andReturn([
                'success' => false,
                'error' => 'Installation failed',
                'output' => 'curl: network error',
            ]);
        $this->app->instance(AgentClient::class, $agent);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class)
            ->call('installAddon', 'jabali-backup')
            ->assertNotified('Installation failed');
    }

    public function test_uninstall_addon_handles_agent_exception(): void
    {
        $agent = Mockery::mock(AgentClient::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('addon.uninstall', ['addon' => 'jabali-backup'])
            ->andThrow(new \RuntimeException('Connection refused'));
        $this->app->instance(AgentClient::class, $agent);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class)
            ->call('uninstallAddon', 'jabali-backup')
            ->assertNotified('Uninstall failed: Connection refused');
    }
}
