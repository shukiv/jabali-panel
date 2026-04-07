<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Filament\Admin\Pages\ServerSettings;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\Agent\AgentResult;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Mockery;
use Tests\TestCase;

class ServerSettingsAddonsTest extends TestCase
{
    use RefreshDatabase;

    private User $admin;

    protected function setUp(): void
    {
        parent::setUp();
        $this->admin = User::factory()->admin()->create();
    }

    public function test_addons_tab_renders(): void
    {
        // Mock agent to return addon status
        $this->mockAgentAddonStatus();

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['activeTab' => 'addons'])
            ->assertOk()
            ->assertSee('Installed Addons');
    }

    public function test_addons_tab_renders_when_agent_unavailable(): void
    {
        // Mock agent that throws
        $mock = Mockery::mock(AgentClient::class);
        $mock->shouldReceive('call')
            ->with('addon.status', [])
            ->andThrow(new \Exception('Agent unavailable'));
        $this->app->instance(AgentClient::class, $mock);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['activeTab' => 'addons'])
            ->assertOk()
            ->assertSee('Installed Addons');
    }

    public function test_addons_tab_shows_addon_cards(): void
    {
        $this->mockAgentAddonStatus([
            'jabali-backup' => [
                'id' => 'jabali-backup',
                'name' => 'Jabali Backup',
                'description' => 'Restic-based backup',
                'installed' => true,
                'version' => '1.0.0',
                'service_active' => null,
            ],
        ]);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['activeTab' => 'addons'])
            ->assertOk()
            ->assertSee('Jabali Backup');
    }

    public function test_load_addons_method_callable(): void
    {
        $this->mockAgentAddonStatus();

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['activeTab' => 'addons'])
            ->call('loadAddons')
            ->assertOk();
    }

    public function test_manage_addon_install(): void
    {
        $mock = Mockery::mock(AgentClient::class);
        // First call: addon.status for tab load
        $mock->shouldReceive('call')
            ->with('addon.status', [])
            ->andReturn(AgentResult::fromResponse([
                'success' => true,
                'addons' => [],
            ]));
        // Second call: addon.install
        $mock->shouldReceive('call')
            ->with('addon.install', ['addon' => 'jabali-backup'])
            ->andReturn(AgentResult::fromResponse([
                'success' => true,
            ]));
        // Third call: addon.status reload after install
        $mock->shouldReceive('call')
            ->with('addon.status', [])
            ->andReturn(AgentResult::fromResponse([
                'success' => true,
                'addons' => [],
            ]));
        $this->app->instance(AgentClient::class, $mock);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['activeTab' => 'addons'])
            ->call('manageAddon', 'jabali-backup', 'install')
            ->assertNotified();
    }

    private function mockAgentAddonStatus(array $addons = []): void
    {
        $mock = Mockery::mock(AgentClient::class);
        $mock->shouldReceive('call')
            ->with('addon.status', [])
            ->andReturn(AgentResult::fromResponse([
                'success' => true,
                'addons' => $addons,
            ]));
        // Allow any other calls to pass through gracefully
        $mock->shouldReceive('call')
            ->withAnyArgs()
            ->andReturn(AgentResult::fromResponse(['success' => true]))
            ->byDefault();
        $this->app->instance(AgentClient::class, $mock);
    }
}
