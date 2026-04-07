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

    public function test_uninstall_button_renders_with_wire_click(): void
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

        $html = Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['activeTab' => 'addons'])
            ->html();

        // Verify wire:click is present (may be HTML-encoded)
        $this->assertTrue(
            str_contains($html, "manageAddon('jabali-backup', 'uninstall')") ||
            str_contains($html, 'manageAddon(&#039;jabali-backup&#039;, &#039;uninstall&#039;)') ||
            str_contains($html, 'manageAddon(&apos;jabali-backup&apos;, &apos;uninstall&apos;)'),
            'Uninstall wire:click not found in HTML. Snippet: '.substr($html, strpos($html, 'Uninstall') ?: 0, 500)
        );
        $this->assertStringContainsString('Uninstall', $html);
    }

    public function test_manage_addon_uninstall(): void
    {
        $mock = Mockery::mock(AgentClient::class);
        $mock->shouldReceive('call')
            ->withAnyArgs()
            ->andReturn(AgentResult::fromResponse(['success' => true, 'addons' => []]))
            ->byDefault();
        $mock->shouldReceive('call')
            ->with('addon.uninstall', ['addon' => 'jabali-backup'])
            ->once()
            ->andReturn(AgentResult::fromResponse(['success' => true]));
        $this->app->instance(AgentClient::class, $mock);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['activeTab' => 'addons'])
            ->call('manageAddon', 'jabali-backup', 'uninstall')
            ->assertNotified();
    }

    public function test_manage_addon_install(): void
    {
        $mock = Mockery::mock(AgentClient::class);
        $mock->shouldReceive('call')
            ->withAnyArgs()
            ->andReturn(AgentResult::fromResponse(['success' => true, 'addons' => []]))
            ->byDefault();
        $mock->shouldReceive('call')
            ->with('addon.install', ['addon' => 'jabali-backup'])
            ->once()
            ->andReturn(AgentResult::fromResponse(['success' => true]));
        $this->app->instance(AgentClient::class, $mock);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['activeTab' => 'addons'])
            ->call('manageAddon', 'jabali-backup', 'install')
            ->assertNotified();
    }

    public function test_manage_addon_install_failure_shows_error(): void
    {
        $mock = Mockery::mock(AgentClient::class);
        $mock->shouldReceive('call')
            ->withAnyArgs()
            ->andReturn(AgentResult::fromResponse(['success' => true, 'addons' => []]))
            ->byDefault();
        $mock->shouldReceive('call')
            ->with('addon.install', ['addon' => 'jabali-backup'])
            ->once()
            ->andReturn(AgentResult::fromResponse([
                'success' => false,
                'error' => 'Install failed (exit 1)',
            ]));
        $this->app->instance(AgentClient::class, $mock);

        Livewire::actingAs($this->admin, 'admin')
            ->test(ServerSettings::class, ['activeTab' => 'addons'])
            ->call('manageAddon', 'jabali-backup', 'install')
            ->assertNotified();
    }

    public function test_manage_addon_agent_exception_shows_error(): void
    {
        $mock = Mockery::mock(AgentClient::class);
        $mock->shouldReceive('call')
            ->withAnyArgs()
            ->andReturn(AgentResult::fromResponse(['success' => true, 'addons' => []]))
            ->byDefault();
        $mock->shouldReceive('call')
            ->with('addon.install', ['addon' => 'jabali-backup'])
            ->once()
            ->andThrow(new \Exception('Connection timed out'));
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
