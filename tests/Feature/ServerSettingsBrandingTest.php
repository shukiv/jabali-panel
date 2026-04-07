<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class ServerSettingsBrandingTest extends TestCase
{
    use RefreshDatabase;

    private User $admin;

    protected function setUp(): void
    {
        parent::setUp();
        $this->admin = User::factory()->admin()->create();
    }

    public function test_server_settings_page_loads(): void
    {
        $response = $this->actingAs($this->admin, 'admin')
            ->get('/jabali-admin/server-settings');

        $response->assertOk();
    }

    public function test_branding_tab_loads(): void
    {
        $response = $this->actingAs($this->admin, 'admin')
            ->get('/jabali-admin/server-settings?tab=branding');

        $response->assertOk();
    }
}
