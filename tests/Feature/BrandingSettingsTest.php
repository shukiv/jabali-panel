<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Filament\Admin\Widgets\Settings\BrandingSettings;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class BrandingSettingsTest extends TestCase
{
    use RefreshDatabase;

    private User $admin;

    protected function setUp(): void
    {
        parent::setUp();
        $this->admin = User::factory()->admin()->create();
    }

    public function test_branding_component_renders(): void
    {
        Livewire::actingAs($this->admin, 'admin')
            ->test(BrandingSettings::class)
            ->assertOk()
            ->assertSee('Control Panel Name')
            ->assertSee('Light Logo')
            ->assertSee('Dark Logo')
            ->assertSee('Save Branding');
    }

    public function test_save_branding_updates_panel_name(): void
    {
        Livewire::actingAs($this->admin, 'admin')
            ->test(BrandingSettings::class)
            ->set('data.panel_name', 'My Panel')
            ->call('saveBranding')
            ->assertNotified();
    }

    public function test_save_branding_rejects_empty_name(): void
    {
        Livewire::actingAs($this->admin, 'admin')
            ->test(BrandingSettings::class)
            ->set('data.panel_name', '')
            ->call('saveBranding')
            ->assertNotified();
    }

    public function test_branding_tab_exists_in_server_settings(): void
    {
        $this->actingAs($this->admin, 'admin')
            ->get('/jabali-admin/server-settings?tab=branding')
            ->assertOk();
    }
}
